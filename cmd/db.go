package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"cloud/internal/api"
	"cloud/internal/output"
	"cloud/internal/wait"
)

/*
Managed databases — phase 4.

PostgreSQL and MariaDB clusters, their backups, and restore-to-a-new-database.
Two restrictions come from the platform rather than from here: automatic
backups and restore are PostgreSQL-only, and the platform answers those with
an `unsupported-engine` problem, which this file turns into one plain sentence.

Statuses are the worker's to write. Nothing below decides a database is ready;
--wait polls and, when nothing moves, reports the worker heartbeat age through
the same healthProbe the app commands use.
*/

var (
	dbCreateEngine  string
	dbCreateVersion string
	dbCreateSize    string
	dbCreateDesc    string
	dbCreateRegion  string
	dbDeleteYes     bool
	dbCredFormat    string
	dbLogsFollow    bool
	dbLogsTail      int
	dbBackupKeep    string
	dbRestoreTo     string
	dbRestoreAt     string
)

// ── table shapes ────────────────────────────────────────────────────────────

type dbRows []api.Database

func (r dbRows) Columns() []string {
	return []string{"NAME", "ENGINE", "VERSION", "STATUS", "SIZE", "REGION"}
}
func (r dbRows) Rows() [][]string {
	out := make([][]string, len(r))
	for i, d := range r {
		out[i] = []string{d.Name, d.Engine, d.Version, d.Status, d.Size, d.Region}
	}
	return out
}
func (r dbRows) IDs() []string {
	out := make([]string, len(r))
	for i, d := range r {
		out[i] = d.Name
	}
	return out
}

type backupRows []api.Backup

func (r backupRows) Columns() []string { return []string{"NAME", "STATUS", "SIZE", "COMPLETED"} }
func (r backupRows) Rows() [][]string {
	out := make([][]string, len(r))
	for i, b := range r {
		completed := ""
		if !b.CompletedAt.IsZero() {
			completed = b.CompletedAt.Local().Format("2006-01-02 15:04")
		}
		out[i] = []string{b.Name, b.Status, b.Size, completed}
	}
	return out
}
func (r backupRows) IDs() []string {
	out := make([]string, len(r))
	for i, b := range r {
		out[i] = b.Name
	}
	return out
}

type engineRows []api.EngineVersion

func (r engineRows) Columns() []string { return []string{"ENGINE", "VERSION", "DEFAULT"} }
func (r engineRows) Rows() [][]string {
	out := make([][]string, len(r))
	for i, e := range r {
		def := ""
		if e.Default {
			def = "yes"
		}
		out[i] = []string{string(e.Engine), e.Version, def}
	}
	return out
}
func (r engineRows) IDs() []string {
	out := make([]string, len(r))
	for i, e := range r {
		out[i] = string(e.Engine) + ":" + e.Version
	}
	return out
}

// ── credentials ─────────────────────────────────────────────────────────────

// dbCredential is one database's credentials as the CLI renders them. It is a
// plain struct rather than the generated type so the rendering, which is the
// only place in the CLI that formats a password, can be tested on its own.
//
// Nothing here is logged, and none of it reaches argv.
type dbCredential struct {
	Engine   string // postgresql | mariadb, inferred from the URI scheme
	Host     string
	Port     int
	Username string
	Password string
	Database string
	// URI is the platform's own connection URI. When empty, one is built.
	URI string
}

// credentialFrom maps the API response onto the render struct.
//
// The engine is taken from the URI scheme rather than by fetching the database
// first: the platform already spells it there, and one round trip is enough.
func credentialFrom(c api.Credentials) dbCredential {
	engine := "postgresql"
	if strings.HasPrefix(c.ConnectionStringUri, "mysql://") {
		engine = "mariadb"
	}
	return dbCredential{
		Engine:   engine,
		Host:     c.Host,
		Port:     c.Port,
		Username: c.Username,
		Password: c.Password,
		Database: c.Database,
		URI:      c.ConnectionStringUri,
	}
}

// connectionURL returns the credential as a URI, building one when the platform
// did not supply it.
//
// A built password is percent-encoded: real passwords contain @, / and : often
// enough that naive concatenation is a bug waiting for a bad night.
func (c dbCredential) connectionURL() string {
	if c.URI != "" {
		return c.URI
	}
	scheme := "postgresql"
	if c.Engine == "mariadb" {
		scheme = "mysql"
	}
	host := c.Host
	if c.Port != 0 {
		host = c.Host + ":" + strconv.Itoa(c.Port)
	}
	u := url.URL{Scheme: scheme, User: url.UserPassword(c.Username, c.Password), Host: host, Path: "/" + c.Database}
	return u.String()
}

// render writes the credential in one of the formats `cloud db credentials`
// offers. Env values are single-quoted so a password containing a space, a $
// or a quote survives being eval'd.
func (c dbCredential) render(format string) (string, error) {
	switch format {
	case "url":
		return c.connectionURL(), nil
	case "env":
		var b strings.Builder
		for _, kv := range c.envPairs() {
			fmt.Fprintf(&b, "%s='%s'\n", kv[0], shellEscape(kv[1]))
		}
		return b.String(), nil
	default:
		return "", &UsageError{fmt.Errorf("--format %q is not env or url", format)}
	}
}

// envPairs names the variables each engine's own tooling reads, so that
// `eval "$(cloud db credentials mydb --format env)"` leaves psql or mysql able
// to connect with no further arguments.
func (c dbCredential) envPairs() [][2]string {
	port := ""
	if c.Port != 0 {
		port = strconv.Itoa(c.Port)
	}
	if c.Engine == "mariadb" {
		return [][2]string{
			{"MYSQL_HOST", c.Host},
			{"MYSQL_TCP_PORT", port},
			{"MYSQL_USER", c.Username},
			{"MYSQL_PWD", c.Password},
			{"MYSQL_DATABASE", c.Database},
			{"DATABASE_URL", c.connectionURL()},
		}
	}
	return [][2]string{
		{"PGHOST", c.Host},
		{"PGPORT", port},
		{"PGUSER", c.Username},
		{"PGPASSWORD", c.Password},
		{"PGDATABASE", c.Database},
		{"DATABASE_URL", c.connectionURL()},
	}
}

// shellEscape makes a value safe inside single quotes.
func shellEscape(s string) string { return strings.ReplaceAll(s, `'`, `'\''`) }

// ── retention ───────────────────────────────────────────────────────────────

// retentionPattern is a count and a unit: 3d, 2w, 1m — barman's spelling, which
// the platform passes through to CNPG.
var retentionPattern = regexp.MustCompile(`^([1-9][0-9]*)([dwm])$`)

// parseRetention validates a --retention window without translating it.
// Reading a bare "3" as three days would be a promise the platform has not
// made, so it is refused.
func parseRetention(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", &UsageError{errors.New("--retention needs a window, for example 7d")}
	}
	if !retentionPattern.MatchString(s) {
		return "", &UsageError{fmt.Errorf("--retention %q is not a window like 7d, 2w or 1m", s)}
	}
	return s, nil
}

// ── waiting ─────────────────────────────────────────────────────────────────

// waitForDatabase polls GET database until its status is terminal, printing
// each change to stderr. It shares terminalStatus and healthProbe with the app
// commands, so "ready" means the same thing everywhere and a stalled poll
// blames the worker rather than the database.
func waitForDatabase(cmd *cobra.Command, c *api.ClientWithResponses, org, name string, timeout time.Duration) (*api.Database, error) {
	var last *api.Database
	poll := func(ctx context.Context) (string, bool, error) {
		res, err := c.GetOrgsOrgDatabasesDbWithResponse(ctx, org, name)
		if err != nil {
			return "", false, err
		}
		if err := apiErr(res.StatusCode(), res.Body); err != nil {
			return "", false, err
		}
		if res.JSON200 == nil {
			return "", false, fmt.Errorf("unexpected response from %s", cfg.APIURL)
		}
		last = res.JSON200
		done, failed := terminalStatus(last.Status)
		if failed {
			return last.Status, false, &wait.ErrFailed{Status: last.Status}
		}
		return last.Status, done, nil
	}
	_, err := wait.Until(cmd.Context(), poll, healthProbe(c), wait.Options{
		Interval: 3 * time.Second,
		Timeout:  timeout,
		OnStatus: func(s string) {
			if !flagQuiet {
				fmt.Fprintf(cmd.ErrOrStderr(), "  %s\n", s)
			}
		},
	})
	return last, err
}

// printDatabase renders one database, waiting first when --wait was given.
func printDatabase(cmd *cobra.Command, c *api.ClientWithResponses, org string, db *api.Database, verb string) error {
	if flagWait {
		waited, err := waitForDatabase(cmd, c, org, db.Name, flagTimeout)
		if err != nil {
			return err
		}
		if waited != nil {
			db = waited
		}
	}
	if !flagQuiet && printer.Format == output.Table {
		fmt.Fprintf(cmd.ErrOrStderr(), "%s %s — status: %s\n", verb, db.Name, db.Status)
		if db.Host != "" {
			fmt.Fprintf(cmd.ErrOrStderr(), "Host: %s:%d\n", db.Host, db.Port)
		}
		if db.ErrorMessage != "" {
			fmt.Fprintf(cmd.ErrOrStderr(), "%s\n", db.ErrorMessage)
		}
		return nil
	}
	return printer.Print(dbRows{*db})
}

// ── commands ────────────────────────────────────────────────────────────────

var dbCmd = &cobra.Command{
	Use:   "db",
	Short: "Managed PostgreSQL and MariaDB databases",
	Long: `Managed databases: a cluster the platform runs, backs up and keeps
patched, reachable over TLS from anywhere and privately from apps in the same
region.

Every command below except "list" and "engines" takes the database's name as
its first argument, before any flags. Automatic backups and restore are
PostgreSQL-only; the platform refuses them for MariaDB.`,
	Example: `  cloud db engines
  cloud db create orders --engine postgresql --wait
  cloud db list
  eval "$(cloud db credentials orders --format env)" && psql
  cloud db backup create orders
  cloud db restore orders --to orders-recovered --at 2026-09-04T10:15:00Z`,
}

var dbEnginesCmd = &cobra.Command{
	Use:   "engines",
	Short: "List the engines and versions available for new databases",
	Long: `List every engine and version a new database can be created with, and
which version is the default when --version is left out.`,
	Example: `  cloud db engines
  cloud db engines -o json`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, _, err := apiClient()
		if err != nil {
			return err
		}
		res, err := c.GetDatabaseEnginesWithResponse(cmd.Context())
		if err != nil {
			return reachErr(err)
		}
		if err := apiErr(res.StatusCode(), res.Body); err != nil {
			return err
		}
		list, err := decoded(res.JSON200)
		if err != nil {
			return err
		}
		return printer.Print(engineRows(list.Items))
	},
}

var dbListCmd = &cobra.Command{
	Use:   "list",
	Short: "List databases in the organisation",
	Example: `  cloud db list
  cloud db list -o json
  cloud db list --quiet            # names only, one per line, for scripts`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		org, err := requireOrg()
		if err != nil {
			return err
		}
		c, _, err := apiClient()
		if err != nil {
			return err
		}
		res, err := c.GetOrgsOrgDatabasesWithResponse(cmd.Context(), org)
		if err != nil {
			return reachErr(err)
		}
		if err := apiErr(res.StatusCode(), res.Body); err != nil {
			return err
		}
		list, err := decoded(res.JSON200)
		if err != nil {
			return err
		}
		return printer.Print(dbRows(list.Items))
	},
}

var dbCreateCmd = &cobra.Command{
	Use:   "create <name> --engine postgresql|mariadb",
	Short: "Create a managed database",
	Long: `Create a database. The name is yours to choose and is how every other
command refers to it; --engine is the only required flag.

Without --version you get the engine's default (see "cloud db engines").
Provisioning takes a few minutes: the row exists immediately, but the host and
credentials appear only once the worker has built the cluster, so pass --wait
if the next thing you do needs to connect.`,
	Example: `  cloud db create orders --engine postgresql
  cloud db create orders --engine postgresql --version 16 --size db-2 --wait
  cloud db create cache --engine mariadb --region zm-lusaka-central-1`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		org, err := requireOrg()
		if err != nil {
			return err
		}
		engine := api.DatabaseCreateEngine(dbCreateEngine)
		if !engine.Valid() {
			return &UsageError{fmt.Errorf("--engine %q is not postgresql or mariadb (see `cloud db engines`)", dbCreateEngine)}
		}
		region := dbCreateRegion
		if region == "" {
			region = cfg.Region
		}
		if region == "" {
			return &UsageError{errors.New("no region given — pass --region, set CLOUD_REGION, or add one to your context (see `cloud region list`)")}
		}
		c, _, err := apiClient()
		if err != nil {
			return err
		}
		body := api.PostOrgsOrgDatabasesJSONRequestBody{Name: args[0], Engine: engine, Region: region}
		if dbCreateVersion != "" {
			body.Version = &dbCreateVersion
		}
		if dbCreateSize != "" {
			body.Size = &dbCreateSize
		}
		if dbCreateDesc != "" {
			body.Description = &dbCreateDesc
		}
		res, err := c.PostOrgsOrgDatabasesWithResponse(cmd.Context(), org, body)
		if err != nil {
			return reachErr(err)
		}
		if err := apiErr(res.StatusCode(), res.Body); err != nil {
			return err
		}
		db, err := decoded(res.JSON201)
		if err != nil {
			return err
		}
		return printDatabase(cmd, c, org, db, "Creating")
	},
}

var dbGetCmd = &cobra.Command{
	Use:   "get <name>",
	Short: "Show a database",
	Example: `  cloud db get orders
  cloud db get orders -o yaml
  cloud db get orders -o json | jq -r .status`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		org, err := requireOrg()
		if err != nil {
			return err
		}
		c, _, err := apiClient()
		if err != nil {
			return err
		}
		res, err := c.GetOrgsOrgDatabasesDbWithResponse(cmd.Context(), org, args[0])
		if err != nil {
			return reachErr(err)
		}
		if err := apiErr(res.StatusCode(), res.Body); err != nil {
			return err
		}
		db, err := decoded(res.JSON200)
		if err != nil {
			return err
		}
		return printer.Print(dbRows{*db})
	},
}

var dbDeleteCmd = &cobra.Command{
	Use:   "delete <name>",
	Short: "Delete a database and its backups",
	Long: `Delete a database, its data and its backups. This cannot be undone.

You are asked to type the name to confirm, because the name is the only
safeguard: a deleted name becomes available again. Use --yes in scripts.`,
	Example: `  cloud db delete orders
  cloud db delete orders --yes         # no prompt, for scripts`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		org, err := requireOrg()
		if err != nil {
			return err
		}
		if err := confirm(cmd, dbDeleteYes, "database", args[0]); err != nil {
			return err
		}
		c, _, err := apiClient()
		if err != nil {
			return err
		}
		res, err := c.DeleteOrgsOrgDatabasesDbWithResponse(cmd.Context(), org, args[0])
		if err != nil {
			return reachErr(err)
		}
		if err := apiErr(res.StatusCode(), res.Body); err != nil {
			return err
		}
		if !flagQuiet {
			fmt.Fprintf(cmd.ErrOrStderr(), "Deleting %s\n", args[0])
		}
		return nil
	},
}

var dbCredentialsCmd = &cobra.Command{
	Use:   "credentials <name>",
	Short: "Print the connection credentials",
	Long: `Print the database's credentials. Requires write-level permission,
because these grant full access to the data.

--format env prints the variables the engine's own client reads, safely quoted,
so "eval" leaves psql or mysql able to connect with no arguments. --format url
prints the connection URI on its own line, for a tool that takes one. Use
-o json for every field, including the private in-region host that apps should
prefer.

Credentials exist only once the cluster is provisioned; before that the
platform answers with a conflict rather than an empty result.`,
	Example: `  cloud db credentials orders
  eval "$(cloud db credentials orders --format env)" && psql
  psql "$(cloud db credentials orders --format url)"
  cloud db credentials orders -o json | jq -r .internalConnectionString`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		org, err := requireOrg()
		if err != nil {
			return err
		}
		c, _, err := apiClient()
		if err != nil {
			return err
		}
		res, err := c.GetOrgsOrgDatabasesDbCredentialsWithResponse(cmd.Context(), org, args[0])
		if err != nil {
			return reachErr(err)
		}
		if err := apiErr(res.StatusCode(), res.Body); err != nil {
			return err
		}
		cred, err := decoded(res.JSON200)
		if err != nil {
			return err
		}
		// json/yaml get every field, including the internal host; the shell
		// formats are for pasting into a client.
		if printer.Format != output.Table {
			return printer.Print(cred)
		}
		text, err := credentialFrom(*cred).render(dbCredFormat)
		if err != nil {
			return err
		}
		fmt.Fprint(cmd.OutOrStdout(), text)
		if !strings.HasSuffix(text, "\n") {
			fmt.Fprintln(cmd.OutOrStdout())
		}
		return nil
	},
}

// powerCmd builds start, stop and restart, which differ only in the call they
// make and the word they print.
func powerCmd(verb, short, long, example string, call func(context.Context, *api.ClientWithResponses, string, string) (*api.Database, int, []byte, error)) *cobra.Command {
	return &cobra.Command{
		Use:     verb + " <name>",
		Short:   short,
		Long:    long,
		Example: example,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			org, err := requireOrg()
			if err != nil {
				return err
			}
			c, _, err := apiClient()
			if err != nil {
				return err
			}
			db, status, body, err := call(cmd.Context(), c, org, args[0])
			if err != nil {
				return reachErr(err)
			}
			if err := apiErr(status, body); err != nil {
				return err
			}
			db, err = decoded(db)
			if err != nil {
				return err
			}
			return printDatabase(cmd, c, org, db, strings.ToUpper(verb[:1])+verb[1:]+"ing")
		},
	}
}

var dbStartCmd = powerCmd("start", "Start a stopped database",
	`Start a database that was stopped. Storage is kept while a database is
stopped, so the data is exactly as it was.`,
	`  cloud db start orders
  cloud db start orders --wait`,
	func(ctx context.Context, c *api.ClientWithResponses, org, name string) (*api.Database, int, []byte, error) {
		res, err := c.PostOrgsOrgDatabasesDbStartWithResponse(ctx, org, name)
		if err != nil {
			return nil, 0, nil, err
		}
		return res.JSON202, res.StatusCode(), res.Body, nil
	})

var dbStopCmd = powerCmd("stop", "Stop a database, keeping its data",
	`Stop a database. The cluster is scaled to zero and its storage kept, so
starting it again brings the same data back.`,
	`  cloud db stop orders
  cloud db stop orders --wait`,
	func(ctx context.Context, c *api.ClientWithResponses, org, name string) (*api.Database, int, []byte, error) {
		res, err := c.PostOrgsOrgDatabasesDbStopWithResponse(ctx, org, name)
		if err != nil {
			return nil, 0, nil, err
		}
		return res.JSON202, res.StatusCode(), res.Body, nil
	})

var dbRestartCmd = powerCmd("restart", "Restart a database",
	`Restart a database. Connections are dropped, so expect a short outage.`,
	`  cloud db restart orders
  cloud db restart orders --wait`,
	func(ctx context.Context, c *api.ClientWithResponses, org, name string) (*api.Database, int, []byte, error) {
		res, err := c.PostOrgsOrgDatabasesDbRestartWithResponse(ctx, org, name)
		if err != nil {
			return nil, 0, nil, err
		}
		return res.JSON202, res.StatusCode(), res.Body, nil
	})

var dbLogsCmd = &cobra.Command{
	Use:   "logs <name>",
	Short: "Print recent database logs; -f to follow",
	Example: `  cloud db logs orders
  cloud db logs orders -f              # keep streaming until Ctrl-C
  cloud db logs orders --tail 1000
  cloud db logs orders --tail 2000 | grep -i error`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		org, err := requireOrg()
		if err != nil {
			return err
		}
		c, _, err := apiClient()
		if err != nil {
			return err
		}
		params := &api.GetOrgsOrgDatabasesDbLogsParams{Tail: &dbLogsTail}
		if dbLogsFollow {
			params.Follow = &dbLogsFollow
		}
		// Raw response: the body is a stream copied line by line, not JSON.
		res, err := c.GetOrgsOrgDatabasesDbLogs(cmd.Context(), org, args[0], params)
		if err != nil {
			return reachErr(err)
		}
		defer res.Body.Close()
		if res.StatusCode != 200 {
			body, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
			return apiErr(res.StatusCode, body)
		}
		sc := bufio.NewScanner(res.Body)
		sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
		out := cmd.OutOrStdout()
		for sc.Scan() {
			fmt.Fprintln(out, sc.Text())
		}
		if err := sc.Err(); err != nil && !errors.Is(err, io.EOF) && cmd.Context().Err() == nil {
			return err
		}
		return nil
	},
}

var dbBackupCmd = &cobra.Command{
	Use:   "backup",
	Short: "Backups of a database",
	Long: `Backups are PostgreSQL-only; the platform refuses them for MariaDB.

"enable" attaches continuous WAL archiving and a daily schedule — do that once
per database. "create" takes a base backup now. The platform records the
schedule on the cluster rather than in the row, so "list" is how you see
whether backups are running.`,
	Example: `  cloud db backup enable orders --retention 7d
  cloud db backup create orders
  cloud db backup list orders`,
}

var dbBackupEnableCmd = &cobra.Command{
	Use:   "enable <db>",
	Short: "Turn on continuous backups and a daily schedule",
	Long: `Turn on backups for a database: continuous WAL archiving plus a daily
base backup, kept for --retention.

Retention is a window, not a count of backups: with a daily schedule, 3d keeps
two or three base backups and the WAL between them, which is what makes
point-in-time restore possible across that window.`,
	Example: `  cloud db backup enable orders
  cloud db backup enable orders --retention 7d
  cloud db backup enable orders --retention 2w`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		org, err := requireOrg()
		if err != nil {
			return err
		}
		body := api.PostOrgsOrgDatabasesDbBackupsEnableJSONRequestBody{}
		if dbBackupKeep != "" {
			keep, err := parseRetention(dbBackupKeep)
			if err != nil {
				return err
			}
			body.Retention = &keep
		}
		c, _, err := apiClient()
		if err != nil {
			return err
		}
		res, err := c.PostOrgsOrgDatabasesDbBackupsEnableWithResponse(cmd.Context(), org, args[0], body)
		if err != nil {
			return reachErr(err)
		}
		if err := apiErr(res.StatusCode(), res.Body); err != nil {
			return err
		}
		enabled, err := decoded(res.JSON202)
		if err != nil {
			return err
		}
		if !flagQuiet && printer.Format == output.Table {
			fmt.Fprintf(cmd.ErrOrStderr(), "Backups enabled on %s, keeping %s. First base backup runs on the next schedule; `cloud db backup create %s` takes one now.\n", args[0], enabled.Retention, args[0])
			return nil
		}
		return printer.Print(enabled)
	},
}

var dbBackupCreateCmd = &cobra.Command{
	Use:   "create <db>",
	Short: "Take a base backup now",
	Long: `Take a base backup immediately, in addition to the schedule.

The backup is requested, not finished, when this returns: watch it move to
"completed" with "cloud db backup list".`,
	Example: `  cloud db backup create orders
  cloud db backup list orders`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		org, err := requireOrg()
		if err != nil {
			return err
		}
		c, _, err := apiClient()
		if err != nil {
			return err
		}
		res, err := c.PostOrgsOrgDatabasesDbBackupsWithResponse(cmd.Context(), org, args[0])
		if err != nil {
			return reachErr(err)
		}
		if err := apiErr(res.StatusCode(), res.Body); err != nil {
			return err
		}
		b, err := decoded(res.JSON201)
		if err != nil {
			return err
		}
		if !flagQuiet && printer.Format == output.Table {
			fmt.Fprintf(cmd.ErrOrStderr(), "Backup %s requested — status: %s\n", b.Name, b.Status)
			return nil
		}
		return printer.Print(backupRows{*b})
	},
}

var dbBackupListCmd = &cobra.Command{
	Use:   "list <db>",
	Short: "List a database's backups",
	Long: `List the backups taken for a database, newest first, with the status
the backup system reports.

An empty list on a database you enabled backups for means the first scheduled
base backup has not run yet.`,
	Example: `  cloud db backup list orders
  cloud db backup list orders -o json`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		org, err := requireOrg()
		if err != nil {
			return err
		}
		c, _, err := apiClient()
		if err != nil {
			return err
		}
		res, err := c.GetOrgsOrgDatabasesDbBackupsWithResponse(cmd.Context(), org, args[0])
		if err != nil {
			return reachErr(err)
		}
		if err := apiErr(res.StatusCode(), res.Body); err != nil {
			return err
		}
		list, err := decoded(res.JSON200)
		if err != nil {
			return err
		}
		return printer.Print(backupRows(list.Items))
	},
}

var dbRestoreCmd = &cobra.Command{
	Use:   "restore <name> --to <new-name>",
	Short: "Restore a database into a new one",
	Long: `Restore from backup into a NEW database. The source is never touched,
which is what makes this safe to run while the original is serving traffic.

With --at, recovery stops at that point in time, using the WAL archive; the
moment must fall inside the retention window backups were enabled with. Without
it, you get the latest backup. PostgreSQL only.`,
	Example: `  cloud db restore orders --to orders-recovered
  cloud db restore orders --to orders-at-ten --at 2026-09-04T10:15:00Z --wait`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		org, err := requireOrg()
		if err != nil {
			return err
		}
		if dbRestoreTo == "" {
			return &UsageError{errors.New("--to <new-name> is required: a restore always creates a new database")}
		}
		if dbRestoreTo == args[0] {
			return &UsageError{errors.New("--to must differ from the source database")}
		}
		body := api.PostOrgsOrgDatabasesDbRestoreJSONRequestBody{Name: dbRestoreTo}
		if dbRestoreAt != "" {
			at, err := time.Parse(time.RFC3339, dbRestoreAt)
			if err != nil {
				return &UsageError{fmt.Errorf("--at %q is not an RFC 3339 time like 2026-09-04T10:15:00Z", dbRestoreAt)}
			}
			body.At = &at
		}
		c, _, err := apiClient()
		if err != nil {
			return err
		}
		res, err := c.PostOrgsOrgDatabasesDbRestoreWithResponse(cmd.Context(), org, args[0], body)
		if err != nil {
			return reachErr(err)
		}
		if err := apiErr(res.StatusCode(), res.Body); err != nil {
			return err
		}
		db, err := decoded(res.JSON201)
		if err != nil {
			return err
		}
		return printDatabase(cmd, c, org, db, "Restoring into")
	},
}

func init() {
	f := dbCreateCmd.Flags()
	f.StringVar(&dbCreateEngine, "engine", "", "postgresql or mariadb (required)")
	f.StringVar(&dbCreateVersion, "version", "", "engine version (default: the engine's default, see `cloud db engines`)")
	f.StringVar(&dbCreateSize, "size", "", "pricing tier (see /pricing)")
	f.StringVar(&dbCreateDesc, "description", "", "short description")
	f.StringVar(&dbCreateRegion, "region", "", "region name (default from context / CLOUD_REGION)")
	_ = dbCreateCmd.MarkFlagRequired("engine")

	dbDeleteCmd.Flags().BoolVarP(&dbDeleteYes, "yes", "y", false, "skip the confirmation (scripts)")

	dbCredentialsCmd.Flags().StringVar(&dbCredFormat, "format", "env", "env or url (ignored for --output json|yaml)")

	dbLogsCmd.Flags().BoolVarP(&dbLogsFollow, "follow", "f", false, "keep streaming")
	dbLogsCmd.Flags().IntVar(&dbLogsTail, "tail", 200, "number of recent lines")

	dbBackupEnableCmd.Flags().StringVar(&dbBackupKeep, "retention", "", "how long to keep backups: 7d, 2w, 1m (default: the platform's own)")

	dbRestoreCmd.Flags().StringVar(&dbRestoreTo, "to", "", "name for the new database (required)")
	dbRestoreCmd.Flags().StringVar(&dbRestoreAt, "at", "", "RFC 3339 point in time to recover to (default: the latest backup)")
	_ = dbRestoreCmd.MarkFlagRequired("to")

	for _, c := range []*cobra.Command{dbCreateCmd, dbRestoreCmd, dbStartCmd, dbStopCmd, dbRestartCmd} {
		c.Flags().BoolVar(&flagWait, "wait", false, "wait until the database reaches a terminal status")
		c.Flags().DurationVar(&flagTimeout, "timeout", 10*time.Minute, "how long --wait waits")
	}

	dbBackupCmd.AddCommand(dbBackupEnableCmd, dbBackupCreateCmd, dbBackupListCmd)
	dbCmd.AddCommand(dbListCmd, dbCreateCmd, dbGetCmd, dbDeleteCmd, dbCredentialsCmd,
		dbStartCmd, dbStopCmd, dbRestartCmd, dbLogsCmd, dbBackupCmd, dbRestoreCmd, dbEnginesCmd)
	rootCmd.AddCommand(dbCmd)
}
