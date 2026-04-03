package cmd

import (
	"github.com/spf13/cobra"
)

// manageCmd represents the manage command
var manageCmd = &cobra.Command{
	Use:   "manage",
	Short: "Manage infrastructure resources",
	Long:  `Manage infrastructure resources such as VM templates, regions, and Proxmox nodes.`,
}

func init() {
	rootCmd.AddCommand(manageCmd)
}
