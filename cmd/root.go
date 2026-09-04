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
	PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
		format, err := output.ParseFormat(flagOutput)
		if err != nil {
			return &UsageError{err}
		}
		printer = &output.Printer{W: cmd.OutOrStdout(), Format: format, Quiet: flagQuiet}

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
		if err != nil {
			return &UsageError{err}
		}
		return nil
	},
}

// Execute runs the CLI.
func Execute() error {
	rootCmd.Version = version.Long()
	return rootCmd.Execute()
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
