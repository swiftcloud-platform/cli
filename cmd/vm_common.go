package cmd

import (
	"strconv"

	"github.com/spf13/cobra"
)

// addCommonVMFlags adds the common execution mode and SSH flags to a VM command
func addCommonVMFlags(cmd *cobra.Command) {
	cmd.Flags().BoolVar(&localMode, "local", false, "Run commands locally instead of via SSH")
	cmd.Flags().StringVar(&sshHost, "ssh-host", "", "Proxmox host hostname or IP")
	cmd.Flags().IntVar(&sshPort, "ssh-port", 22, "SSH port (default: 22)")
	cmd.Flags().StringVar(&sshUsername, "ssh-username", "root", "SSH username (default: root)")
	cmd.Flags().StringVar(&sshPassword, "ssh-password", "", "SSH password")
	cmd.Flags().StringVar(&sshPrivateKey, "ssh-private-key", "", "Path to SSH private key file")
	cmd.Flags().BoolVar(&sshInsecure, "ssh-insecure", false, "Skip host key verification")

	cmd.PreRunE = func(cmd *cobra.Command, args []string) error {
		if !localMode && sshHost == "" {
			return cmdError("--ssh-host is required when not using --local")
		}
		if !localMode && sshPassword == "" && sshPrivateKey == "" {
			return cmdError("either --ssh-password or --ssh-private-key is required when not using --local")
		}
		return nil
	}
}

// parseVMID parses a VM ID from a string argument
func parseVMID(s string) int {
	id, _ := strconv.Atoi(s)
	return id
}

// cmdError creates a user-friendly error
func cmdError(msg string) error {
	return &userError{message: msg}
}

type userError struct {
	message string
}

func (e *userError) Error() string {
	return e.message
}
