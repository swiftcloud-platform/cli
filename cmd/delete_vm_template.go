package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"cloud/internal/executor"
	proxmoxssh "cloud/internal/ssh"
)

var (
	deleteVMID int
)

// deleteVMTemplateCmd represents the delete-vm command
var deleteVMTemplateCmd = &cobra.Command{
	Use:   "delete-vm-template",
	Short: "Delete a VM template from a Proxmox node",
	Long: `Delete a VM template from a Proxmox node by running 'qm destroy'.

This command can run in two modes:
  - Remote (default): SSHs into the Proxmox host and runs qm destroy
  - Local (--local): Runs qm destroy directly on the local machine

Example usage (remote):
  cloud manage delete-vm \
    --ssh-host 192.168.1.100 \
    --ssh-username root \
    --ssh-password 'yourpassword' \
    --vm-id 9000

Example usage (local):
  cloud manage delete-vm \
    --local \
    --vm-id 9000`,
	RunE: runDeleteVMTemplate,
}

func init() {
	manageCmd.AddCommand(deleteVMTemplateCmd)

	// Execution mode flag
	deleteVMTemplateCmd.Flags().BoolVar(&localMode, "local", false, "Run qm commands locally instead of via SSH")

	// SSH connection flags
	deleteVMTemplateCmd.Flags().StringVar(&sshHost, "ssh-host", "", "Proxmox host hostname or IP")
	deleteVMTemplateCmd.Flags().IntVar(&sshPort, "ssh-port", 22, "SSH port (default: 22)")
	deleteVMTemplateCmd.Flags().StringVar(&sshUsername, "ssh-username", "root", "SSH username (default: root)")
	deleteVMTemplateCmd.Flags().StringVar(&sshPassword, "ssh-password", "", "SSH password")
	deleteVMTemplateCmd.Flags().StringVar(&sshPrivateKey, "ssh-private-key", "", "Path to SSH private key file")
	deleteVMTemplateCmd.Flags().BoolVar(&sshInsecure, "ssh-insecure", false, "Skip host key verification")

	// Template flags
	deleteVMTemplateCmd.Flags().IntVar(&deleteVMID, "vm-id", 0, "Proxmox VM ID to delete")

	// Mark required flags
	deleteVMTemplateCmd.MarkFlagRequired("vm-id")

	// Validate flags
	deleteVMTemplateCmd.PreRunE = func(cmd *cobra.Command, args []string) error {
		if !localMode && sshHost == "" {
			return fmt.Errorf("--ssh-host is required when not using --local")
		}
		if !localMode && sshPassword == "" && sshPrivateKey == "" {
			return fmt.Errorf("either --ssh-password or --ssh-private-key is required when not using --local")
		}
		return nil
	}
}

func runDeleteVMTemplate(cmd *cobra.Command, args []string) error {
	mode := "remote (SSH)"
	if localMode {
		mode = "local"
	}

	fmt.Printf("🗑️  Deleting VM (ID: %d, mode: %s)...\n", deleteVMID, mode)

	// Create the appropriate executor
	var exec CommandExecutor
	var closer func() error

	if localMode {
		exec = executor.NewLocalExecutor()
		closer = func() error { return nil }
	} else {
		fmt.Printf("🔌 Connecting to %s via SSH...\n", sshHost)
		sshClient, err := proxmoxssh.NewClient(proxmoxssh.Config{
			Host:           sshHost,
			Port:           sshPort,
			Username:       sshUsername,
			Password:       sshPassword,
			PrivateKeyPath: sshPrivateKey,
			Insecure:       sshInsecure,
		})
		if err != nil {
			return fmt.Errorf("failed to connect via SSH: %w", err)
		}
		exec = &sshExecutor{client: sshClient}
		closer = sshClient.Close
		fmt.Println("✅ Connected via SSH")
		fmt.Println()
	}
	defer closer()

	// Run qm destroy
	fmt.Printf("💥 Destroying VM %d...\n", deleteVMID)
	destroyCmd := fmt.Sprintf("qm destroy %d", deleteVMID)
	stdout, stderr, err := exec.Run(destroyCmd)
	if err != nil {
		return fmt.Errorf("failed to delete vm: %s\n%s", stderr, stdout)
	}

	fmt.Println("✅ VM deleted successfully")
	return nil
}
