package cmd

import (
	"github.com/spf13/cobra"
)

// vmCmd represents the vm command
var vmCmd = &cobra.Command{
	Use:   "vm",
	Short: "Manage virtual machines",
	Long:  `Manage virtual machines on Proxmox nodes including lifecycle, configuration, and snapshots.`,
}

func init() {
	rootCmd.AddCommand(vmCmd)
}
