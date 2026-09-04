package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var logoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Forget the stored credential for the current API",
	Long: `Delete the stored credential for the API URL in use. Credentials are
kept per API host, so this signs you out of that one host and leaves any others
alone; "cloud whoami" shows which host you are about to affect.`,
	Example: `  cloud logout
  CLOUD_API_URL=http://localhost:5173/api/v1 cloud logout    # just the dev host`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		store, err := credentialStore()
		if err != nil {
			return err
		}
		if err := store.Clear(cfg.APIURL); err != nil {
			return err
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "Signed out of %s.\n", cfg.APIURL)
		return nil
	},
}

func init() { rootCmd.AddCommand(logoutCmd) }
