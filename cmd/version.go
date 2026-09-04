package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"cloud/internal/version"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the CLI version",
	// No config or network needed; must work on a machine that has never signed in.
	PersistentPreRunE: func(*cobra.Command, []string) error { return nil },
	RunE: func(cmd *cobra.Command, _ []string) error {
		if flagOutput == "json" {
			fmt.Fprintf(cmd.OutOrStdout(), "{\"version\":%q}\n", version.Version())
			return nil
		}
		fmt.Fprintln(cmd.OutOrStdout(), version.Long())
		return nil
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
