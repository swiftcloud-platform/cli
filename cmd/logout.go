package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var logoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Forget the stored credential for the current API",
	Args:  cobra.NoArgs,
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
