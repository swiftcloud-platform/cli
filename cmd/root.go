// Package cmd holds the Cobra command tree. Commands stay thin: they parse
// flags, call into internal packages, and print through internal/output.
package cmd

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"cloud/internal/api"
	"cloud/internal/config"
	"cloud/internal/output"
	"cloud/internal/version"
)

// Global flags, resolved once in PersistentPreRunE.
var (
	flagContext string
	flagAPIURL  string
	flagOrg     string
	flagRegion  string
	flagOutput  string
	flagQuiet   bool
)

// Resolved configuration and printer for the running command.
var (
	cfg     *config.Resolved
	printer *output.Printer
)

var rootCmd = &cobra.Command{
	Use:   "cloud",
	Short: "SwiftCloud from the command line",
	Long: `cloud deploys apps, manages databases and works with object storage on SwiftCloud.

Sign in once with "cloud login"; every other command uses that session.
Set CLOUD_TOKEN, CLOUD_ORG and CLOUD_REGION in CI instead of a config file.

Commands read as: cloud <resource> <verb> [name] [flags]. A name in <angle
brackets> is a positional argument and comes before any flags, so the app is
named first and the thing being done to it second:

  cloud app domain list <app>
  cloud app domain add <app> <hostname>

Every command's own --help lists its arguments, flags and examples.`,
	Example: `  cloud login                                    sign in on this machine
  cloud whoami                                   who am I, and which API
  cloud app list                                 apps in the current organisation
  cloud app create demo --image nginx:1.27       create one and print its URL
  cloud app domain list demo                     domains attached to "demo"`,
	SilenceUsage:  true,
	SilenceErrors: true,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		format, err := output.ParseFormat(flagOutput)
		if err != nil {
			return &UsageError{err}
		}
		printer = &output.Printer{W: cmd.OutOrStdout(), Format: format, Quiet: flagQuiet}
		envTokenWarned = false

		if err := validateResourceArgs(cmd, args); err != nil {
			return err
		}

		dir, err := config.Dir(os.Getenv)
		if err != nil {
			return err
		}
		file, err := config.Load(dir + string(os.PathSeparator) + "config.yaml")
		if err != nil {
			return err
		}
		cfg, err = config.Resolve(file, os.Getenv, config.Overrides{
			Context: flagContext, APIURL: flagAPIURL, Org: flagOrg, Region: flagRegion,
		})
		var badURL *config.APIURLError
		if errors.As(err, &badURL) && cmd.CommandPath() == "cloud logout" && cfg != nil {
			// logout needs only the host to forget a credential, and a
			// retired or otherwise refused URL is exactly where someone
			// would want to clear one. Nothing is sent to it.
			return nil
		}
		if err != nil {
			cfg = nil
			return &UsageError{err}
		}
		return nil
	},
}

/*
Positional arguments that become URL path segments.

The generated client path-escapes each argument, which leaves "." and ".."
alone, and then resolves the path against the API URL, which removes them:
`cloud vm ssh-key remove web ..` became DELETE …/vms/web/, and a redirect of
that turned it into deleting the VM. So every positional argument is treated
as a resource name and checked here, once, before any command runs — unless
the command says otherwise, because some arguments are not names at all:
local file paths and s3:// URIs in `storage cp` and friends ("." is a fine
directory), KEY=VALUE pairs, a public-key file.

A command opts out with the annotationArgs annotation; commands defined in
files this check cannot annotate are listed in argPolicyByPath. The API
client refuses a collapsed path as well (api.CheckRequestPath), so a gap here
fails closed.
*/

// annotationArgs on a command overrides how its positional args are checked.
const annotationArgs = "cloud/args"

// Policies for annotationArgs.
const (
	// argsNone: no positional argument is a resource name.
	argsNone = "none"
	// argsFirst: only the first argument is a resource name.
	argsFirst = "first"
	// argsS3: arguments are local paths or s3:// URIs; only the bucket of
	// an s3:// URI is a resource name.
	argsS3 = "s3"
)

// argPolicyByPath covers commands whose source does not carry an annotation.
var argPolicyByPath = map[string]string{
	"cloud storage ls":       argsS3,
	"cloud storage stat":     argsS3,
	"cloud storage cat":      argsS3,
	"cloud storage cp":       argsS3,
	"cloud storage sync":     argsS3,
	"cloud storage mv":       argsS3,
	"cloud storage rm":       argsS3,
	"cloud storage presign":  argsS3,
	"cloud storage versions": argsS3,
	"cloud storage restore":  argsS3,
	"cloud app env set":      argsFirst,
	"cloud app env unset":    argsFirst,
}

func argPolicy(cmd *cobra.Command) string {
	if p, ok := cmd.Annotations[annotationArgs]; ok {
		return p
	}
	if p, ok := argPolicyByPath[cmd.CommandPath()]; ok {
		return p
	}
	return ""
}

// validateResourceArgs refuses an argument that would not stay one path
// segment. Shell completion and help are exempt: completion passes the word
// being typed, which is routinely empty.
func validateResourceArgs(cmd *cobra.Command, args []string) error {
	switch cmd.Name() {
	case cobra.ShellCompRequestCmd, cobra.ShellCompNoDescRequestCmd, "help", "completion":
		return nil
	}
	if cmd.HasParent() && cmd.Parent().Name() == "completion" {
		return nil
	}
	check := args
	switch argPolicy(cmd) {
	case argsNone:
		return nil
	case argsFirst:
		if len(check) > 1 {
			check = check[:1]
		}
	case argsS3:
		for _, a := range args {
			rest, ok := strings.CutPrefix(a, "s3://")
			if !ok {
				continue
			}
			bucket, _, _ := strings.Cut(rest, "/")
			if bucket == "." || bucket == ".." {
				return &UsageError{fmt.Errorf("%q: %q is not a valid bucket name", a, bucket)}
			}
		}
		return nil
	}
	for _, a := range check {
		if err := config.ValidateSegment("name", a); err != nil {
			return &UsageError{fmt.Errorf("%q is not a valid name: names must not be empty, \".\", \"..\" or contain control characters", a)}
		}
	}
	return nil
}

// Execute runs the CLI.
func Execute() error {
	rootCmd.Version = version.Long()
	rejectUnknownSubcommands(rootCmd)
	return rootCmd.Execute()
}

// rejectUnknownSubcommands makes `cloud db logs` an error rather than a help
// screen with exit 0.
//
// Cobra's default for a command group with no Run of its own is to print help
// and succeed, whatever arguments follow. So a removed or misspelled
// subcommand looks exactly like asking for help, and a script sees success —
// which matters most for a command that used to exist, where the user has the
// muscle memory and no reason to read the output. A bare group still prints
// its help and exits 0, because that is a fair thing to ask for.
func rejectUnknownSubcommands(c *cobra.Command) {
	for _, sub := range c.Commands() {
		rejectUnknownSubcommands(sub)
	}
	if !c.HasSubCommands() || c.Run != nil || c.RunE != nil {
		return
	}
	c.RunE = func(cmd *cobra.Command, args []string) error {
		if len(args) > 0 {
			return &UsageError{fmt.Errorf("unknown command %q for %q — run `%s --help` to see what it takes",
				args[0], cmd.CommandPath(), cmd.CommandPath())}
		}
		return cmd.Help()
	}
}

// UsageError marks a mistake in how the command was invoked, as opposed to a
// failure while doing the work. Scripts can tell them apart by exit code.
type UsageError struct{ Err error }

func (e *UsageError) Error() string { return e.Err.Error() }
func (e *UsageError) Unwrap() error { return e.Err }

// Exit codes, stable for scripts.
const (
	ExitOK      = 0
	ExitError   = 1
	ExitUsage   = 2
	ExitAuth    = 3 // not signed in, or token expired/revoked
	ExitDenied  = 4 // signed in, but the role does not allow this
	ExitMissing = 5 // the resource does not exist (or is another tenant's)
)

// ExitCode maps an error to the process exit status.
func ExitCode(err error) int {
	var u *UsageError
	if errors.As(err, &u) {
		return ExitUsage
	}
	var a *AuthError
	if errors.As(err, &a) {
		return ExitAuth
	}
	var pe *api.PathError
	if errors.As(err, &pe) {
		return ExitUsage
	}
	// The platform's typed problem decides first. Its detail is free text
	// from the server, and a detail that happened to begin "invalid argument"
	// must not turn a 403 or 404 into a usage error.
	var e *api.Error
	if errors.As(err, &e) {
		switch e.Type {
		case "unauthenticated":
			return ExitAuth
		case "forbidden":
			return ExitDenied
		case "not-found":
			return ExitMissing
		case "validation", "conflict":
			return ExitUsage
		case "unsupported-engine", "limit-reached", "billing-blocked":
			// The platform refused something it understood: automatic backups
			// on MariaDB, a plan limit, an unfunded organisation. Each is a
			// mistake in the request or the account, not a CLI failure, and
			// each carries a sentence worth printing verbatim.
			return ExitUsage
		}
		return ExitError
	}
	// Cobra's own complaints are usage mistakes and must exit 2 like ours.
	// A missing required flag is reported by ValidateRequiredFlags as a plain
	// error, so there is nothing to type-assert on; the message is stable and
	// matching it beats letting `--image` missing exit 1 while `--engine`
	// wrong exits 2.
	msg := err.Error()
	for _, prefix := range []string{"required flag", "unknown flag", "unknown command", "invalid argument", "accepts ", "unknown shorthand flag"} {
		if strings.HasPrefix(msg, prefix) {
			return ExitUsage
		}
	}
	return ExitError
}

func init() {
	// Cobra lists subcommands by name only, so "list <app>" shows up as a bare
	// "list" and the reader has to open a second --help to learn where the app
	// name goes. Listing .Use instead puts the arguments in the first screen.
	rootCmd.SetUsageTemplate(strings.ReplaceAll(
		rootCmd.UsageTemplate(),
		"rpad .Name .NamePadding ",
		"rpad .Use 28 ",
	))

	pf := rootCmd.PersistentFlags()
	pf.StringVar(&flagContext, "context", "", "named context from the config file (env CLOUD_CONTEXT)")
	pf.StringVar(&flagAPIURL, "api-url", "", fmt.Sprintf("API base URL (env CLOUD_API_URL; default %s)", config.DefaultAPIURL))
	pf.StringVar(&flagOrg, "org", "", "organisation slug (env CLOUD_ORG)")
	pf.StringVar(&flagRegion, "region", "", "default region for new resources (env CLOUD_REGION)")
	pf.StringVarP(&flagOutput, "output", "o", "table", "output format: table, json or yaml")
	pf.BoolVarP(&flagQuiet, "quiet", "q", false, "print only identifiers, one per line")
}
