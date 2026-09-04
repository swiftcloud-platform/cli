package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"cloud/internal/config"
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
		res, err := c.GetOrgsWithResponse(cmd.Context())
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
		res, err := c.GetOrgsWithResponse(cmd.Context())
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

		dir, err := config.Dir(os.Getenv)
		if err != nil {
			return err
		}
		path := filepath.Join(dir, "config.yaml")
		file, err := config.Load(path)
		if err != nil {
			return err
		}
		name := cfg.ContextName
		if name == "" {
			name = "default"
			file.CurrentContext = name
		}
		ctx := file.Contexts[name]
		ctx.Org = slug
		if ctx.APIURL == "" && cfg.APIURL != config.DefaultAPIURL {
			ctx.APIURL = cfg.APIURL
		}
		file.Contexts[name] = ctx
		if err := config.Save(path, file); err != nil {
			return err
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "Default organisation for context %q is now %s.\n", name, slug)
		return nil
	},
}

func init() {
	orgCmd.AddCommand(orgListCmd, orgUseCmd)
	rootCmd.AddCommand(orgCmd)
}
