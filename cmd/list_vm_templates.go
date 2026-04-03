package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

// listVMTemplatesCmd represents the list-vm-templates command
var listVMTemplatesCmd = &cobra.Command{
	Use:   "list-vm-templates",
	Short: "List VM templates on a Proxmox node",
	Long: `List VM templates on a Proxmox node by running 'qm list'.

This command can run in two modes:
  - Remote (default): SSHs into the Proxmox host and runs qm list
  - Local (--local): Runs qm list directly on the local machine

Example usage (remote):
  cloud manage list-vm-templates \
    --ssh-host 192.168.1.100 \
    --ssh-username root \
    --ssh-password 'yourpassword'

Example usage (local):
  cloud manage list-vm-templates --local`,
	RunE: runListVMTemplates,
}

func init() {
	manageCmd.AddCommand(listVMTemplatesCmd)

	listVMTemplatesCmd.Flags().BoolVar(&localMode, "local", false, "Run qm commands locally instead of via SSH")
	listVMTemplatesCmd.Flags().StringVar(&sshHost, "ssh-host", "", "Proxmox host hostname or IP")
	listVMTemplatesCmd.Flags().IntVar(&sshPort, "ssh-port", 22, "SSH port (default: 22)")
	listVMTemplatesCmd.Flags().StringVar(&sshUsername, "ssh-username", "root", "SSH username (default: root)")
	listVMTemplatesCmd.Flags().StringVar(&sshPassword, "ssh-password", "", "SSH password")
	listVMTemplatesCmd.Flags().StringVar(&sshPrivateKey, "ssh-private-key", "", "Path to SSH private key file")
	listVMTemplatesCmd.Flags().BoolVar(&sshInsecure, "ssh-insecure", false, "Skip host key verification")

	listVMTemplatesCmd.PreRunE = func(cmd *cobra.Command, args []string) error {
		if !localMode && sshHost == "" {
			return fmt.Errorf("--ssh-host is required when not using --local")
		}
		if !localMode && sshPassword == "" && sshPrivateKey == "" {
			return fmt.Errorf("either --ssh-password or --ssh-private-key is required when not using --local")
		}
		return nil
	}
}

func runListVMTemplates(cmd *cobra.Command, args []string) error {
	mode := "remote (SSH)"
	if localMode {
		mode = "local"
	}

	fmt.Printf("📋 Listing VM templates (mode: %s)...\n\n", mode)

	exec, closer, err := createExecutor()
	if err != nil {
		return err
	}
	defer closer()

	stdout, stderr, err := exec.Run("qm list")
	if err != nil {
		return fmt.Errorf("failed to list VMs: %s\n%s", stderr, stdout)
	}

	vms := parseQMList(stdout)

	// Check each VM's config for the template flag
	var templates []vmEntry
	for _, vm := range vms {
		isTpl, err := checkIsTemplate(exec, vm.VMID)
		if err != nil {
			fmt.Printf("  ⚠️  Warning: could not check template status for VM %d: %v\n", vm.VMID, err)
			continue
		}
		if isTpl {
			templates = append(templates, vm)
		}
	}

	if len(templates) == 0 {
		fmt.Println("No VM templates found.")
		return nil
	}

	fmt.Printf("%-8s %-30s %-12s %-10s\n", "VMID", "NAME", "STATUS", "MEMORY")
	fmt.Println("-------- ------------------------------ ------------ ----------")

	for _, t := range templates {
		fmt.Printf("%-8d %-30s %-12s %-10d\n", t.VMID, t.Name, t.Status, t.MemoryMB)
	}

	fmt.Printf("\nTotal: %d template(s)\n", len(templates))
	return nil
}
