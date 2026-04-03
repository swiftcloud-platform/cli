package cmd

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

// listVMsCmd represents the list-vms command
var listVMsCmd = &cobra.Command{
	Use:   "list-vms",
	Short: "List VMs on a Proxmox node",
	Long: `List VMs on a Proxmox node by running 'qm list'.

This command can run in two modes:
  - Remote (default): SSHs into the Proxmox host and runs qm list
  - Local (--local): Runs qm list directly on the local machine

Example usage (remote):
  cloud manage list-vms \
    --ssh-host 192.168.1.100 \
    --ssh-username root \
    --ssh-password 'yourpassword'

Example usage (local):
  cloud manage list-vms --local`,
	RunE: runListVMs,
}

func init() {
	manageCmd.AddCommand(listVMsCmd)

	listVMsCmd.Flags().BoolVar(&localMode, "local", false, "Run qm commands locally instead of via SSH")
	listVMsCmd.Flags().StringVar(&sshHost, "ssh-host", "", "Proxmox host hostname or IP")
	listVMsCmd.Flags().IntVar(&sshPort, "ssh-port", 22, "SSH port (default: 22)")
	listVMsCmd.Flags().StringVar(&sshUsername, "ssh-username", "root", "SSH username (default: root)")
	listVMsCmd.Flags().StringVar(&sshPassword, "ssh-password", "", "SSH password")
	listVMsCmd.Flags().StringVar(&sshPrivateKey, "ssh-private-key", "", "Path to SSH private key file")
	listVMsCmd.Flags().BoolVar(&sshInsecure, "ssh-insecure", false, "Skip host key verification")

	listVMsCmd.PreRunE = func(cmd *cobra.Command, args []string) error {
		if !localMode && sshHost == "" {
			return fmt.Errorf("--ssh-host is required when not using --local")
		}
		if !localMode && sshPassword == "" && sshPrivateKey == "" {
			return fmt.Errorf("either --ssh-password or --ssh-private-key is required when not using --local")
		}
		return nil
	}
}

type vmEntry struct {
	VMID       int
	Name       string
	Status     string
	MemoryMB   int
	IsTemplate bool
}

func runListVMs(cmd *cobra.Command, args []string) error {
	mode := "remote (SSH)"
	if localMode {
		mode = "local"
	}

	fmt.Printf("📋 Listing VMs (mode: %s)...\n\n", mode)

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
	for i, vm := range vms {
		isTpl, err := checkIsTemplate(exec, vm.VMID)
		if err != nil {
			fmt.Printf("  ⚠️  Warning: could not check template status for VM %d: %v\n", vm.VMID, err)
			continue
		}
		vms[i].IsTemplate = isTpl
	}

	var nonTemplates []vmEntry
	for _, vm := range vms {
		if !vm.IsTemplate {
			nonTemplates = append(nonTemplates, vm)
		}
	}

	if len(nonTemplates) == 0 {
		fmt.Println("No VMs found.")
		return nil
	}

	fmt.Printf("%-8s %-30s %-12s %-10s\n", "VMID", "NAME", "STATUS", "MEMORY")
	fmt.Println("-------- ------------------------------ ------------ ----------")

	for _, vm := range nonTemplates {
		fmt.Printf("%-8d %-30s %-12s %-10d\n", vm.VMID, vm.Name, vm.Status, vm.MemoryMB)
	}

	fmt.Printf("\nTotal: %d VM(s)\n", len(nonTemplates))
	return nil
}

func parseQMList(output string) []vmEntry {
	var vms []vmEntry
	lines := strings.Split(output, "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "VMID") {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}

		vmid, _ := strconv.Atoi(fields[0])
		memMB, _ := strconv.Atoi(fields[3])

		vms = append(vms, vmEntry{
			VMID:     vmid,
			Name:     fields[1],
			Status:   fields[2],
			MemoryMB: memMB,
		})
	}

	return vms
}

// checkIsTemplate checks if a VM is a template by looking at its config
func checkIsTemplate(exec CommandExecutor, vmid int) (bool, error) {
	stdout, stderr, err := exec.Run(fmt.Sprintf("qm config %d", vmid))
	if err != nil {
		return false, fmt.Errorf("%s", stderr)
	}

	// Look for "template: 1" in the config output
	for _, line := range strings.Split(stdout, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "template:") {
			return true, nil
		}
	}
	return false, nil
}
