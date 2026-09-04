package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/spf13/cobra"

	"cloud/internal/config"
	"cloud/internal/output"
)

/*
Contexts: named targets (API URL, organisation, default region) so staging,
production and a local platform coexist on one machine. internal/config has
always resolved them; these commands are how a person creates and switches
them without hand-editing config.yaml.
*/

type contextRow struct {
	Name    string `json:"name" yaml:"name"`
	Current bool   `json:"current" yaml:"current"`
	APIURL  string `json:"apiUrl" yaml:"apiUrl"`
	Org     string `json:"org" yaml:"org"`
	Region  string `json:"region" yaml:"region"`
}

type contextRows []contextRow

func (r contextRows) Columns() []string { return []string{"", "NAME", "API", "ORG", "REGION"} }
func (r contextRows) Rows() [][]string {
	out := make([][]string, len(r))
	for i, c := range r {
		mark := ""
		if c.Current {
			mark = "*"
		}
		api := c.APIURL
		if api == "" {
			api = config.DefaultAPIURL + " (default)"
		}
		out[i] = []string{mark, c.Name, api, orDash(c.Org), orDash(c.Region)}
	}
	return out
}
func (r contextRows) IDs() []string {
	out := make([]string, len(r))
	for i, c := range r {
		out[i] = c.Name
	}
	return out
}

func configPath() (string, error) {
	dir, err := config.Dir(os.Getenv)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.yaml"), nil
}

func loadConfigFile() (string, *config.File, error) {
	path, err := configPath()
	if err != nil {
		return "", nil, err
	}
	f, err := config.Load(path)
	return path, f, err
}

func rowsFrom(f *config.File) contextRows {
	names := make([]string, 0, len(f.Contexts))
	for n := range f.Contexts {
		names = append(names, n)
	}
	sort.Strings(names)
	rows := make(contextRows, 0, len(names))
	for _, n := range names {
		c := f.Contexts[n]
		rows = append(rows, contextRow{Name: n, Current: n == f.CurrentContext, APIURL: c.APIURL, Org: c.Org, Region: c.Region})
	}
	return rows
}

var contextCmd = &cobra.Command{
	Use:   "context",
	Short: "Named targets: API URL, organisation and region",
	Long: `A context bundles an API URL, an organisation and a default region under a
name, so you can switch between production, staging and a local platform with
one command. Flags and CLOUD_* variables still override whatever the context says.`,
	Example: `  cloud context set local --api-url http://localhost:5173/api/v1 --org platform
  cloud context use local
  cloud context list`,
}

var contextListCmd = &cobra.Command{
	Use:     "list",
	Short:   "List contexts; * marks the current one",
	Example: "  cloud context list\n  cloud context list -o json",
	Args:    cobra.NoArgs,
	// Reads only the config file; never needs a credential or the network.
	PersistentPreRunE: preRunLocal,
	RunE: func(_ *cobra.Command, _ []string) error {
		_, f, err := loadConfigFile()
		if err != nil {
			return err
		}
		return printer.Print(rowsFrom(f))
	},
}

var contextCurrentCmd = &cobra.Command{
	Use:               "current",
	Short:             "Print the current context name",
	Example:           "  cloud context current",
	Args:              cobra.NoArgs,
	PersistentPreRunE: preRunLocal,
	RunE: func(cmd *cobra.Command, _ []string) error {
		_, f, err := loadConfigFile()
		if err != nil {
			return err
		}
		if f.CurrentContext == "" {
			return &UsageError{errors.New("no current context — create one with `cloud context set <name> …` then `cloud context use <name>`")}
		}
		fmt.Fprintln(cmd.OutOrStdout(), f.CurrentContext)
		return nil
	},
}

var contextUseCmd = &cobra.Command{
	Use:               "use <name>",
	Short:             "Make a context current",
	Example:           "  cloud context use staging",
	Args:              cobra.ExactArgs(1),
	PersistentPreRunE: preRunLocal,
	RunE: func(cmd *cobra.Command, args []string) error {
		path, f, err := loadConfigFile()
		if err != nil {
			return err
		}
		if _, ok := f.Contexts[args[0]]; !ok {
			return &UsageError{fmt.Errorf("no context named %q (see `cloud context list`)", args[0])}
		}
		f.CurrentContext = args[0]
		if err := config.Save(path, f); err != nil {
			return err
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "Switched to context %q.\n", args[0])
		return nil
	},
}

var (
	ctxSetAPIURL string
	ctxSetOrg    string
	ctxSetRegion string
	ctxSetUse    bool
)

var contextSetCmd = &cobra.Command{
	Use:   "set <name> [--api-url URL] [--org SLUG] [--region NAME]",
	Short: "Create a context or change its fields",
	Long: `Create a context, or change only the fields you pass on an existing one.
Omitted fields are kept. Pass an empty value ("") to clear a field.`,
	Example: `  cloud context set prod --org acme --region zm-lusaka-central-1
  cloud context set local --api-url http://localhost:5173/api/v1 --org platform --use
  cloud context set prod --region ""`,
	Args:              cobra.ExactArgs(1),
	PersistentPreRunE: preRunLocal,
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		if name == "" {
			return &UsageError{errors.New("context name must not be empty")}
		}
		if !cmd.Flags().Changed("api-url") && !cmd.Flags().Changed("org") && !cmd.Flags().Changed("region") {
			// Creating with no fields is allowed only when it does not exist yet.
			_, f, err := loadConfigFile()
			if err != nil {
				return err
			}
			if _, exists := f.Contexts[name]; exists {
				return &UsageError{errors.New("nothing to change — pass --api-url, --org or --region")}
			}
		}
		if cmd.Flags().Changed("api-url") && ctxSetAPIURL != "" {
			if err := config.ValidateAPIURL(ctxSetAPIURL); err != nil {
				return &UsageError{err}
			}
		}
		path, f, err := loadConfigFile()
		if err != nil {
			return err
		}
		c := f.Contexts[name]
		if cmd.Flags().Changed("api-url") {
			c.APIURL = ctxSetAPIURL
		}
		if cmd.Flags().Changed("org") {
			c.Org = ctxSetOrg
		}
		if cmd.Flags().Changed("region") {
			c.Region = ctxSetRegion
		}
		_, existed := f.Contexts[name]
		f.Contexts[name] = c
		if ctxSetUse || f.CurrentContext == "" {
			f.CurrentContext = name
		}
		if err := config.Save(path, f); err != nil {
			return err
		}
		verb := "Updated"
		if !existed {
			verb = "Created"
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "%s context %q", verb, name)
		if f.CurrentContext == name {
			fmt.Fprint(cmd.ErrOrStderr(), " (current)")
		}
		fmt.Fprintln(cmd.ErrOrStderr(), ".")
		return nil
	},
}

var contextDeleteCmd = &cobra.Command{
	Use:               "delete <name>",
	Short:             "Remove a context (credentials for its API are kept; use `cloud logout`)",
	Example:           "  cloud context delete old-staging",
	Args:              cobra.ExactArgs(1),
	PersistentPreRunE: preRunLocal,
	RunE: func(cmd *cobra.Command, args []string) error {
		path, f, err := loadConfigFile()
		if err != nil {
			return err
		}
		if _, ok := f.Contexts[args[0]]; !ok {
			return &UsageError{fmt.Errorf("no context named %q", args[0])}
		}
		delete(f.Contexts, args[0])
		if f.CurrentContext == args[0] {
			f.CurrentContext = ""
		}
		if err := config.Save(path, f); err != nil {
			return err
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "Deleted context %q.\n", args[0])
		return nil
	},
}

// preRunLocal sets up the printer without resolving a context. Context
// commands must work when the current context is broken — that is often why
// someone is running them.
func preRunLocal(cmd *cobra.Command, _ []string) error {
	format, err := output.ParseFormat(flagOutput)
	if err != nil {
		return &UsageError{err}
	}
	printer = &output.Printer{W: cmd.OutOrStdout(), Format: format, Quiet: flagQuiet}
	return nil
}

func init() {
	contextSetCmd.Flags().StringVar(&ctxSetAPIURL, "api-url", "", "API base URL for this context")
	contextSetCmd.Flags().StringVar(&ctxSetOrg, "org", "", "default organisation slug")
	contextSetCmd.Flags().StringVar(&ctxSetRegion, "region", "", "default region for new resources")
	contextSetCmd.Flags().BoolVar(&ctxSetUse, "use", false, "also make it the current context")
	contextCmd.AddCommand(contextListCmd, contextCurrentCmd, contextUseCmd, contextSetCmd, contextDeleteCmd)
	rootCmd.AddCommand(contextCmd)
}
