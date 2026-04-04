package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

var (
	moveTemplateVMID   int
	moveTemplateTarget string
)

// moveTemplateCmd represents the move-template command
var moveTemplateCmd = &cobra.Command{
	Use:   "move-template",
	Short: "Move a VM template's disk to shared storage",
	Long: `Move a VM template's disk to shared storage so it's accessible to all nodes in the cluster.

This command:
  1. Converts the template back to a VM (qm template --delete 1)
  2. Moves the disk to the target storage (qm move-disk)
  3. Converts it back to a template (qm template)

Example usage (remote):
  cloud manage move-template \
    --ssh-host 192.168.1.100 \
    --ssh-username root \
    --ssh-password 'yourpassword' \
    --vm-id 9000 \
    --target ceph-storage

Example usage (local):
  cloud manage move-template --local --vm-id 9000 --target ceph-storage`,
	RunE: runMoveTemplate,
}

func init() {
	manageCmd.AddCommand(moveTemplateCmd)

	moveTemplateCmd.Flags().BoolVar(&localMode, "local", false, "Run commands locally instead of via SSH")
	moveTemplateCmd.Flags().StringVar(&sshHost, "ssh-host", "", "Proxmox host hostname or IP")
	moveTemplateCmd.Flags().IntVar(&sshPort, "ssh-port", 22, "SSH port (default: 22)")
	moveTemplateCmd.Flags().StringVar(&sshUsername, "ssh-username", "root", "SSH username (default: root)")
	moveTemplateCmd.Flags().StringVar(&sshPassword, "ssh-password", "", "SSH password")
	moveTemplateCmd.Flags().StringVar(&sshPrivateKey, "ssh-private-key", "", "Path to SSH private key file")
	moveTemplateCmd.Flags().BoolVar(&sshInsecure, "ssh-insecure", false, "Skip host key verification")

	moveTemplateCmd.Flags().IntVar(&moveTemplateVMID, "vm-id", 0, "VM ID of the template to move")
	moveTemplateCmd.Flags().StringVar(&moveTemplateTarget, "target", "", "Target shared storage name")

	moveTemplateCmd.MarkFlagRequired("vm-id")
	moveTemplateCmd.MarkFlagRequired("target")

	moveTemplateCmd.PreRunE = func(cmd *cobra.Command, args []string) error {
		if !localMode && sshHost == "" {
			return fmt.Errorf("--ssh-host is required when not using --local")
		}
		if !localMode && sshPassword == "" && sshPrivateKey == "" {
			return fmt.Errorf("either --ssh-password or --ssh-private-key is required when not using --local")
		}
		return nil
	}
}

func runMoveTemplate(cmd *cobra.Command, args []string) error {
	mode := "remote (SSH)"
	if localMode {
		mode = "local"
	}

	fmt.Printf("🔄 Moving VM template %d to storage '%s' (mode: %s)...\n\n", moveTemplateVMID, moveTemplateTarget, mode)

	exec, closer, err := createExecutor()
	if err != nil {
		return err
	}
	defer closer()

	// Step 1: Check current disk location
	fmt.Println("📍 Checking current disk location...")
	stdout, stderr, err := exec.Run(fmt.Sprintf("qm config %d | grep '^scsi0:'", moveTemplateVMID))
	if err != nil {
		return fmt.Errorf("failed to get disk info: %s\n%s", stderr, stdout)
	}

	currentDisk := strings.TrimSpace(stdout)
	fmt.Printf("   Current: %s\n", currentDisk)
	fmt.Println()

	// Step 2: Convert template back to VM
	fmt.Println("🔄 Converting template back to VM...")
	stdout, stderr, err = exec.Run(fmt.Sprintf("qm template %d --delete 1 2>/dev/null || sed -i '/^template:/d' /etc/pve/qemu-server/%d.conf", moveTemplateVMID, moveTemplateVMID))
	if err != nil {
		return fmt.Errorf("failed to convert to VM: %s\n%s", stderr, stdout)
	}
	fmt.Println("✅ Converted to VM")
	fmt.Println()

	// Step 3: Move disk to target storage
	fmt.Printf("💾 Moving disk to '%s'...\n", moveTemplateTarget)
	stdout, stderr, err = exec.Run(fmt.Sprintf("qm move-disk %d scsi0 %s", moveTemplateVMID, moveTemplateTarget))
	if err != nil {
		// Try to convert back to template first on failure
		exec.Run(fmt.Sprintf("qm template %d", moveTemplateVMID))
		return fmt.Errorf("failed to move disk: %s\n%s", stderr, stdout)
	}
	fmt.Println("✅ Disk moved")
	fmt.Println()

	// Step 4: Convert back to template
	fmt.Println("🔄 Converting VM back to template...")
	stdout, stderr, err = exec.Run(fmt.Sprintf("qm template %d", moveTemplateVMID))
	if err != nil {
		return fmt.Errorf("failed to convert to template: %s\n%s", stderr, stdout)
	}
	fmt.Println("✅ Converted to template")
	fmt.Println()

	fmt.Printf("🎉 Template %d is now on shared storage '%s' and accessible to all nodes.\n", moveTemplateVMID, moveTemplateTarget)

	return nil
}
