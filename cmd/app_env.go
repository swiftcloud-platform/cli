package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"cloud/internal/api"
	"cloud/internal/output"
)

/*
Environment variables, and the other settings that were create-only.

Two things about the platform shape this file.

The API replaces the whole map: PATCH with envVars sets exactly what you send,
so adding one variable means reading the current set, merging, and writing it
back. That read-modify-write is done here rather than asked of the user, and
it is why two people running `env set` on the same app at the same moment can
lose one of the two changes — the last write wins, as it would in the
dashboard.

Every update redeploys the app, because the platform rolls a new Knative
revision when the row changes. So `env set` costs a rollout; it is not a
free metadata edit, and the command says so.
*/

var (
	envShowValues bool
	envFile       string
	envFromStdin  string
	updatePort    int
	updateDesc    string
	updateRegSrv  string
	updateRegUser string
	updateRegPass bool
	updateRegClr  bool
)

// envRows lists variables. Values are masked unless asked for: these hold API
// keys in practice, whatever the platform calls them, and a listing is the
// thing most likely to be read over someone's shoulder or pasted into a chat.
type envRows struct {
	vars map[string]string
	show bool
}

func (r envRows) Columns() []string { return []string{"NAME", "VALUE"} }
func (r envRows) Rows() [][]string {
	keys := r.keys()
	out := make([][]string, len(keys))
	for i, k := range keys {
		v := r.vars[k]
		if !r.show {
			v = maskValue(v)
		}
		out[i] = []string{k, v}
	}
	return out
}
func (r envRows) IDs() []string { return r.keys() }

func (r envRows) keys() []string {
	keys := make([]string, 0, len(r.vars))
	for k := range r.vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// maskValue keeps enough to recognise a value without disclosing it. Short
// values are hidden completely, since revealing three of four characters is
// not a mask.
func maskValue(v string) string {
	switch {
	case v == "":
		return ""
	case len(v) <= 8:
		return strings.Repeat("•", len(v))
	default:
		return v[:2] + strings.Repeat("•", len(v)-4) + v[len(v)-2:]
	}
}

// appEnvVars fetches an app's current variables, which every write needs
// first because the platform replaces the whole map.
func appEnvVars(ctx context.Context, c *api.ClientWithResponses, org, app string) (map[string]string, error) {
	res, err := c.GetOrgsOrgAppsAppWithResponse(ctx, org, app)
	if err != nil {
		return nil, reachErr(err)
	}
	if err := apiErr(res.StatusCode(), res.Body); err != nil {
		return nil, err
	}
	a, err := decoded(res.JSON200)
	if err != nil {
		return nil, err
	}
	// Copied rather than returned directly: callers mutate it before writing
	// the whole map back, and the response's map should not be the thing they
	// edit in place.
	vars := make(map[string]string, len(a.EnvVars))
	for k, v := range a.EnvVars {
		vars[k] = v
	}
	return vars, nil
}

// patchEnvVars writes the whole map back and reports the redeploy.
func patchEnvVars(cmd *cobra.Command, c *api.ClientWithResponses, org, app string, vars map[string]string) error {
	body := api.PatchOrgsOrgAppsAppJSONRequestBody{EnvVars: &vars}
	res, err := c.PatchOrgsOrgAppsAppWithResponse(cmd.Context(), org, app, body)
	if err != nil {
		return reachErr(err)
	}
	if err := apiErr(res.StatusCode(), res.Body); err != nil {
		return err
	}
	a, err := decoded(res.JSON200)
	if err != nil {
		return err
	}
	if !flagQuiet && printer.Format == output.Table {
		fmt.Fprintf(cmd.ErrOrStderr(), "%s now has %d variable(s); a new revision is rolling out.\n", a.Name, len(vars))
		fmt.Fprintf(cmd.ErrOrStderr(), "Watch it with `cloud app get %s`.\n", a.Name)
		return nil
	}
	return printer.Print(envRows{vars: vars, show: envShowValues})
}

// parseEnvFile reads KEY=VALUE lines, ignoring blanks and # comments, and
// accepting the `export KEY=VALUE` that .env files often carry.
func parseEnvFile(r io.Reader) (map[string]string, error) {
	out := map[string]string{}
	sc := bufio.NewScanner(r)
	line := 0
	for sc.Scan() {
		line++
		s := strings.TrimSpace(sc.Text())
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		s = strings.TrimPrefix(s, "export ")
		k, v, ok := strings.Cut(s, "=")
		if !ok || strings.TrimSpace(k) == "" {
			return nil, fmt.Errorf("line %d is not KEY=VALUE: %q", line, sc.Text())
		}
		k = strings.TrimSpace(k)
		// Strip one layer of matching quotes, the way a shell would.
		if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
			v = v[1 : len(v)-1]
		}
		out[k] = v
	}
	return out, sc.Err()
}

var appEnvCmd = &cobra.Command{
	Use:   "env",
	Short: "Environment variables on an app",
	Long: `Read and change an app's environment variables.

Changing them rolls out a new revision, because the platform redeploys when
the app changes — so these are not free metadata edits.

Values are masked in the listing by default. The platform treats them as
configuration rather than secrets and will return them, but people put API
keys here, so revealing them is opt-in.`,
	Example: `  cloud app env list notifie
  cloud app env set notifie LOG_LEVEL=debug
  cloud app env set notifie SENDGRID_KEY --from-stdin SENDGRID_KEY < key.txt
  cloud app env set notifie --env-file .env
  cloud app env unset notifie OLD_FLAG`,
}

var appEnvListCmd = &cobra.Command{
	Use:   "list <app>",
	Short: "List an app's environment variables",
	Long: `List the variables set on an app.

Values are masked unless --show-values is given; -o json always returns them
in full, since that output is meant for another program.`,
	Example: `  cloud app env list notifie
  cloud app env list notifie --show-values
  cloud app env list notifie -o json | jq -r .SENDGRID_KEY
  cloud app env list notifie --quiet          # names only`,
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
		vars, err := appEnvVars(cmd.Context(), c, org, args[0])
		if err != nil {
			return err
		}
		if printer.Format != output.Table {
			return printer.Print(vars)
		}
		if len(vars) == 0 && !flagQuiet {
			fmt.Fprintf(cmd.ErrOrStderr(), "%s has no environment variables.\n", args[0])
			return nil
		}
		return printer.Print(envRows{vars: vars, show: envShowValues})
	},
}

var appEnvSetCmd = &cobra.Command{
	Use:   "set <app> [KEY=VALUE ...]",
	Short: "Set environment variables, keeping the rest",
	Long: `Add or change variables, leaving the others alone.

The platform replaces the whole set on write, so this reads the current
variables, merges yours in, and writes them back. Two people setting different
variables at the same moment can therefore lose one of the two changes.

A value that is a secret should not go in the command line, where it lands in
shell history: --from-stdin NAME reads exactly one value from stdin, and
--env-file reads a whole KEY=VALUE file.

Setting a variable redeploys the app.`,
	Example: `  cloud app env set notifie LOG_LEVEL=debug
  cloud app env set notifie LOG_LEVEL=debug TZ=Africa/Lusaka

  # a secret, kept out of argv and history
  cloud app env set notifie --from-stdin SENDGRID_KEY < key.txt
  pass show sendgrid | cloud app env set notifie --from-stdin SENDGRID_KEY

  # a whole file
  cloud app env set notifie --env-file .env`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		org, err := requireOrg()
		if err != nil {
			return err
		}
		app := args[0]
		incoming := map[string]string{}

		if envFile != "" {
			f, err := os.Open(envFile) // #nosec G304 -- the file the user named
			if err != nil {
				return err
			}
			parsed, parseErr := parseEnvFile(f)
			_ = f.Close()
			if parseErr != nil {
				return &UsageError{fmt.Errorf("%s: %w", envFile, parseErr)}
			}
			for k, v := range parsed {
				incoming[k] = v
			}
		}
		if envFromStdin != "" {
			data, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), 1<<20))
			if err != nil {
				return err
			}
			// One value, with the trailing newline a here-doc or a file adds.
			incoming[envFromStdin] = strings.TrimRight(string(data), "\r\n")
		}
		for _, pair := range args[1:] {
			k, v, ok := strings.Cut(pair, "=")
			if !ok || k == "" {
				return &UsageError{fmt.Errorf("%q is not KEY=VALUE", pair)}
			}
			incoming[k] = v
		}
		if len(incoming) == 0 {
			return &UsageError{errors.New("nothing to set — pass KEY=VALUE, --env-file or --from-stdin")}
		}

		c, _, err := apiClient()
		if err != nil {
			return err
		}
		vars, err := appEnvVars(cmd.Context(), c, org, app)
		if err != nil {
			return err
		}
		for k, v := range incoming {
			vars[k] = v
		}
		return patchEnvVars(cmd, c, org, app, vars)
	},
}

var appEnvUnsetCmd = &cobra.Command{
	Use:   "unset <app> KEY [KEY ...]",
	Short: "Remove environment variables",
	Long: `Remove variables, leaving the others alone. Removing a variable
redeploys the app.

A name that is not set is reported rather than ignored, because a silent
success is indistinguishable from a typo in the name.`,
	Example: `  cloud app env unset notifie OLD_FLAG
  cloud app env unset notifie OLD_FLAG LEGACY_URL`,
	Args: cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		org, err := requireOrg()
		if err != nil {
			return err
		}
		app, names := args[0], args[1:]
		c, _, err := apiClient()
		if err != nil {
			return err
		}
		vars, err := appEnvVars(cmd.Context(), c, org, app)
		if err != nil {
			return err
		}
		var missing []string
		for _, k := range names {
			if _, ok := vars[k]; !ok {
				missing = append(missing, k)
				continue
			}
			delete(vars, k)
		}
		if len(missing) > 0 {
			return &UsageError{fmt.Errorf("%s has no variable named %s", app, strings.Join(missing, ", "))}
		}
		return patchEnvVars(cmd, c, org, app, vars)
	},
}

var appUpdateCmd = &cobra.Command{
	Use:   "update <name>",
	Short: "Change an app's port, description or registry credentials",
	Long: `Change settings that were previously only available at creation.

The image is changed with "cloud app deploy", the replica range with "cloud
app scale", and environment variables with "cloud app env" — this covers what
is left. Any change redeploys the app.

Rotating a registry token is the common case: pass the new credentials with
--registry-password-stdin so the password never reaches argv.

The pricing tier cannot be changed here; the platform does not accept it on an
update, so a different size still means creating a new app.`,
	Example: `  cloud app update notifie --port 3000
  cloud app update notifie --description "notification service"

  # rotate a registry token
  echo "$NEW_TOKEN" | cloud app update notifie \
      --registry-server ghcr.io --registry-username deploy --registry-password-stdin

  # stop using a private registry
  cloud app update notifie --clear-registry`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		org, err := requireOrg()
		if err != nil {
			return err
		}
		body := api.PatchOrgsOrgAppsAppJSONRequestBody{}
		changed := false
		if cmd.Flags().Changed("port") {
			body.ContainerPort = &updatePort
			changed = true
		}
		if cmd.Flags().Changed("description") {
			body.Description = &updateDesc
			changed = true
		}
		if updateRegClr {
			if updateRegSrv != "" || updateRegUser != "" || updateRegPass {
				return &UsageError{errors.New("--clear-registry cannot be combined with the other --registry flags")}
			}
			// The API takes null to mean "forget the credentials"; the
			// generated type spells that as a nil pointer on a set field,
			// which the encoder omits — so this goes through as a raw patch.
			return clearRegistry(cmd, org, args[0])
		}
		if updateRegSrv != "" || updateRegUser != "" || updateRegPass {
			if updateRegSrv == "" || updateRegUser == "" || !updateRegPass {
				return &UsageError{errors.New("registry credentials need --registry-server, --registry-username and --registry-password-stdin together")}
			}
			pass, err := stdinToken(cmd.InOrStdin())
			if err != nil {
				return err
			}
			body.RegistryAuth = &struct {
				Password string `json:"password"`
				Server   string `json:"server"`
				Username string `json:"username"`
			}{Password: pass, Server: updateRegSrv, Username: updateRegUser}
			changed = true
		}
		if !changed {
			return &UsageError{errors.New("nothing to change — pass --port, --description, the --registry flags, or --clear-registry")}
		}

		c, _, err := apiClient()
		if err != nil {
			return err
		}
		res, err := c.PatchOrgsOrgAppsAppWithResponse(cmd.Context(), org, args[0], body)
		if err != nil {
			return reachErr(err)
		}
		if err := apiErr(res.StatusCode(), res.Body); err != nil {
			return err
		}
		a, err := decoded(res.JSON200)
		if err != nil {
			return err
		}
		if !flagQuiet && printer.Format == output.Table {
			fmt.Fprintf(cmd.ErrOrStderr(), "Updated %s — a new revision is rolling out.\n", a.Name)
			return nil
		}
		return printer.Print(appRows{*a})
	},
}

// clearRegistry sends registryAuth: null, which the generated body type cannot
// express — an omitted field and a null one are the same nil pointer there,
// and only the null means "forget these credentials".
func clearRegistry(cmd *cobra.Command, org, app string) error {
	c, _, err := apiClient()
	if err != nil {
		return err
	}
	res, err := c.PatchOrgsOrgAppsAppWithBodyWithResponse(cmd.Context(), org, app,
		"application/json", strings.NewReader(`{"registryAuth":null}`))
	if err != nil {
		return reachErr(err)
	}
	if err := apiErr(res.StatusCode(), res.Body); err != nil {
		return err
	}
	a, err := decoded(res.JSON200)
	if err != nil {
		return err
	}
	if !flagQuiet && printer.Format == output.Table {
		fmt.Fprintf(cmd.ErrOrStderr(), "Removed the registry credentials from %s — a new revision is rolling out.\n", a.Name)
		return nil
	}
	return printer.Print(appRows{*a})
}

func init() {
	appEnvListCmd.Flags().BoolVar(&envShowValues, "show-values", false, "print values instead of masking them")

	appEnvSetCmd.Flags().StringVar(&envFile, "env-file", "", "read KEY=VALUE lines from a file")
	appEnvSetCmd.Flags().StringVar(&envFromStdin, "from-stdin", "", "read one variable's value from stdin, named by this flag")

	appUpdateCmd.Flags().IntVar(&updatePort, "port", 0, "port the container listens on")
	appUpdateCmd.Flags().StringVar(&updateDesc, "description", "", "short description")
	appUpdateCmd.Flags().StringVar(&updateRegSrv, "registry-server", "", "private registry host")
	appUpdateCmd.Flags().StringVar(&updateRegUser, "registry-username", "", "private registry user")
	appUpdateCmd.Flags().BoolVar(&updateRegPass, "registry-password-stdin", false, "read the registry password from stdin")
	appUpdateCmd.Flags().BoolVar(&updateRegClr, "clear-registry", false, "forget the stored registry credentials")

	appEnvCmd.AddCommand(appEnvListCmd, appEnvSetCmd, appEnvUnsetCmd)
	appCmd.AddCommand(appEnvCmd, appUpdateCmd)
}
