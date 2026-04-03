package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

// initNodeCmd represents the init-node command
var initNodeCmd = &cobra.Command{
	Use:   "init-node",
	Short: "Initialize a Proxmox node for SDN (setup dnsmasq)",
	Long: `Initialize a Proxmox node for SwiftCloud SDN by installing and configuring dnsmasq.

This command runs the following steps:
  1. Updates package lists (apt update)
  2. Installs dnsmasq (apt install dnsmasq -y)
  3. Disables the default dnsmasq instance (systemctl disable --now dnsmasq)

This is required for SDN to work correctly on each new Proxmox node.

Example usage (remote):
  cloud manage init-node \
    --ssh-host 192.168.1.100 \
    --ssh-username root \
    --ssh-password 'yourpassword'

Example usage (local - run on Proxmox host):
  cloud manage init-node --local`,
	RunE: runInitNode,
}

func init() {
	manageCmd.AddCommand(initNodeCmd)

	// Execution mode flag
	initNodeCmd.Flags().BoolVar(&localMode, "local", false, "Run commands locally instead of via SSH")

	// SSH connection flags
	initNodeCmd.Flags().StringVar(&sshHost, "ssh-host", "", "Proxmox host hostname or IP")
	initNodeCmd.Flags().IntVar(&sshPort, "ssh-port", 22, "SSH port (default: 22)")
	initNodeCmd.Flags().StringVar(&sshUsername, "ssh-username", "root", "SSH username (default: root)")
	initNodeCmd.Flags().StringVar(&sshPassword, "ssh-password", "", "SSH password")
	initNodeCmd.Flags().StringVar(&sshPrivateKey, "ssh-private-key", "", "Path to SSH private key file")
	initNodeCmd.Flags().BoolVar(&sshInsecure, "ssh-insecure", false, "Skip host key verification")

	// Validate flags
	initNodeCmd.PreRunE = func(cmd *cobra.Command, args []string) error {
		if !localMode && sshHost == "" {
			return fmt.Errorf("--ssh-host is required when not using --local")
		}
		if !localMode && sshPassword == "" && sshPrivateKey == "" {
			return fmt.Errorf("either --ssh-password or --ssh-private-key is required when not using --local")
		}
		return nil
	}
}

func runInitNode(cmd *cobra.Command, args []string) error {
	mode := "remote (SSH)"
	if localMode {
		mode = "local"
	}

	fmt.Printf("🔧 Initializing node for SDN (mode: %s)...\n\n", mode)

	exec, closer, err := createExecutor()
	if err != nil {
		return err
	}
	defer closer()

	// Step 1: Update package lists
	fmt.Println("📦 Updating package lists...")
	stdout, stderr, err := exec.Run("apt update")
	if err != nil {
		return fmt.Errorf("failed to update packages: %s\n%s", stderr, stdout)
	}
	fmt.Println("✅ Package lists updated")
	fmt.Println()

	// Step 2: Install dnsmasq
	fmt.Println("📥 Installing dnsmasq...")
	stdout, stderr, err = exec.Run("apt install dnsmasq -y")
	if err != nil {
		return fmt.Errorf("failed to install dnsmasq: %s\n%s", stderr, stdout)
	}
	fmt.Println("✅ dnsmasq installed")
	fmt.Println()

	// Step 3: Disable default dnsmasq instance
	fmt.Println("🛑 Disabling default dnsmasq instance...")
	stdout, stderr, err = exec.Run("systemctl disable --now dnsmasq")
	if err != nil {
		return fmt.Errorf("failed to disable dnsmasq: %s\n%s", stderr, stdout)
	}
	fmt.Println("✅ dnsmasq disabled")
	fmt.Println()

	// Verify installation
	fmt.Println("🔍 Verifying setup...")
	stdout, stderr, err = exec.Run("dpkg -s dnsmasq 2>/dev/null | grep -q 'Status: install ok installed' && echo 'installed' || echo 'not installed'")
	if err != nil || stdout != "installed" {
		fmt.Printf("  ⚠️  Warning: dnsmasq may not be installed correctly\n")
	} else {
		fmt.Println("✅ dnsmasq verified as installed")
	}

	stdout, stderr, err = exec.Run("systemctl is-enabled dnsmasq 2>/dev/null || echo 'disabled'")
	if err != nil {
		stdout = "disabled"
	}
	fmt.Printf("   dnsmasq service status: %s\n", stdout)
	fmt.Println()

	fmt.Println("🎉 Node initialization complete!")
	fmt.Println()
	fmt.Println("The node is now ready for SwiftCloud SDN.")

	return nil
}
