package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"cloud/internal/config"
	"cloud/internal/termsafe"
)

var orgCmd = &cobra.Command{
	Use:   "org",
	Short: "Organisations you belong to",
	Long: `Apps, databases and buckets all belong to an organisation, so most
commands need to know which one. Set it once with "cloud org use <slug>" and
every later command uses it; override per command with --org or CLOUD_ORG.`,
	Example: `  cloud org list
  cloud org use platform
  cloud app list --org another-org     # just this once, without changing the default`,
}

var orgListCmd = &cobra.Command{
	Use:   "list",
	Short: "List your organisations and your role in each",
	Example: `  cloud org list
  cloud org list -o json`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, _, err := apiClient()
		if err != nil {
			return err
		}
		res, err := c.GetOrgsWithResponse(cmd.Context(), nil)
		if err != nil {
			return fmt.Errorf("could not reach %s: %w", cfg.APIURL, err)
		}
		if err := apiErr(res.StatusCode(), res.Body); err != nil {
			return err
		}
		list, err := decoded(res.JSON200)
		if err != nil {
			return err
		}
		return printer.Print(orgRowsFrom(list.Items))
	},
}

var orgUseCmd = &cobra.Command{
	Use:   "use <slug>",
	Short: "Set the default organisation for the current context",
	Long: `Set the default organisation, written to the config file so it survives
new shells. The argument is the slug from "cloud org list", not the display name.`,
	Example: `  cloud org use platform`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		slug := args[0]
		// Verify before writing, so a typo does not become the default.
		c, _, err := apiClient()
		if err != nil {
			return err
		}
		res, err := c.GetOrgsWithResponse(cmd.Context(), nil)
		if err != nil {
			return fmt.Errorf("could not reach %s: %w", cfg.APIURL, err)
		}
		if err := apiErr(res.StatusCode(), res.Body); err != nil {
			return err
		}
		list, err := decoded(res.JSON200)
		if err != nil {
			return err
		}
		found := false
		for _, o := range list.Items {
			if o.Slug == slug || o.Id == slug {
				slug = o.Slug
				found = true
			}
		}
		if !found {
			return &UsageError{fmt.Errorf("you are not a member of an organisation %q (see `cloud org list`)", slug)}
		}

		w := cmd.ErrOrStderr()
		name, err := saveDefaultOrg(w, slug)
		if err != nil {
			return err
		}
		fmt.Fprintf(w, "Default organisation for context %q is now %s.\n", name, termsafe.String(slug))
		// --org and CLOUD_ORG outrank the saved default, so while one is set
		// the change above does not apply — say so rather than let the next
		// command act on a different organisation than the one just chosen.
		switch src := cfg.Source["org"]; src {
		case "flag", "env":
			how := "--org"
			if src == "env" {
				how = "CLOUD_ORG"
			}
			if cfg.Org != slug {
				fmt.Fprintf(w, "warning: %s is set to %s, which overrides this default — commands run with it will act on %s, not %s.\n",
					how, termsafe.String(cfg.Org), termsafe.String(cfg.Org), termsafe.String(slug))
			} else {
				fmt.Fprintf(w, "note: %s is also set; it overrides the saved default whenever it is present.\n", how)
			}
		}
		return nil
	},
}

func init() {
	orgCmd.AddCommand(orgListCmd, orgUseCmd)
	rootCmd.AddCommand(orgCmd)
}

// saveDefaultOrg writes the organisation into the current context and returns
// the context it wrote to, creating "default" when none is named.
//
// Shared by `org use` and by `login`, which sets the first organisation
// automatically so that signing in leaves the CLI ready to use rather than
// one instruction short of it.
//
// It writes the organisation and nothing else into an existing context. An
// API URL that came from --api-url or CLOUD_API_URL is an override for this
// run; writing it into the context would make it permanent, so every later
// command — and every token — would go to that host without anyone having
// asked. Only when the context is being created here, and the URL was given
// as a flag on this very command, is it saved with the organisation (the
// organisation belongs to that API), and stderr says so.
func saveDefaultOrg(w io.Writer, slug string) (string, error) {
	dir, err := config.Dir(os.Getenv)
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "config.yaml")
	file, err := config.Load(path)
	if err != nil {
		return "", err
	}
	name := cfg.ContextName
	if name == "" {
		name = "default"
		file.CurrentContext = name
	}
	if file.Contexts == nil {
		file.Contexts = map[string]config.Context{}
	}
	ctx, existed := file.Contexts[name]
	ctx.Org = slug
	if !existed && ctx.APIURL == "" && cfg.Source["api_url"] == "flag" && cfg.APIURL != config.DefaultAPIURL {
		ctx.APIURL = cfg.APIURL
		fmt.Fprintf(w, "Saved API URL %s in new context %q along with the organisation.\n", cfg.APIURL, name)
	}
	file.Contexts[name] = ctx
	return name, config.Save(path, file)
}
