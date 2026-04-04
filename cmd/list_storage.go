package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// listStorageCmd represents the list-storage command
var listStorageCmd = &cobra.Command{
	Use:   "list-storage",
	Short: "List storage on a Proxmox node",
	Long: `List storage on a Proxmox node and identify which storage is shared across the cluster.

Shared storage (Ceph, NFS, iSCSI, GlusterFS) makes VM templates accessible to all nodes.

Example usage (remote):
  cloud manage list-storage \
    --ssh-host 192.168.1.100 \
    --ssh-username root \
    --ssh-password 'yourpassword'

Example usage (local):
  cloud manage list-storage --local`,
	RunE: runListStorage,
}

func init() {
	manageCmd.AddCommand(listStorageCmd)

	listStorageCmd.Flags().BoolVar(&localMode, "local", false, "Run commands locally instead of via SSH")
	listStorageCmd.Flags().StringVar(&sshHost, "ssh-host", "", "Proxmox host hostname or IP")
	listStorageCmd.Flags().IntVar(&sshPort, "ssh-port", 22, "SSH port (default: 22)")
	listStorageCmd.Flags().StringVar(&sshUsername, "ssh-username", "root", "SSH username (default: root)")
	listStorageCmd.Flags().StringVar(&sshPassword, "ssh-password", "", "SSH password")
	listStorageCmd.Flags().StringVar(&sshPrivateKey, "ssh-private-key", "", "Path to SSH private key file")
	listStorageCmd.Flags().BoolVar(&sshInsecure, "ssh-insecure", false, "Skip host key verification")

	listStorageCmd.PreRunE = func(cmd *cobra.Command, args []string) error {
		if !localMode && sshHost == "" {
			return fmt.Errorf("--ssh-host is required when not using --local")
		}
		if !localMode && sshPassword == "" && sshPrivateKey == "" {
			return fmt.Errorf("either --ssh-password or --ssh-private-key is required when not using --local")
		}
		return nil
	}
}

func runListStorage(cmd *cobra.Command, args []string) error {
	mode := "remote (SSH)"
	if localMode {
		mode = "local"
	}

	fmt.Printf("📋 Listing storage (mode: %s)...\n\n", mode)

	exec, closer, err := createExecutor()
	if err != nil {
		return err
	}
	defer closer()

	stdout, stderr, err := exec.Run("pvesm status")
	if err != nil {
		return fmt.Errorf("failed to list storage: %s\n%s", stderr, stdout)
	}

	lines := strings.Split(stdout, "\n")
	if len(lines) <= 1 {
		fmt.Println("No storage found.")
		return nil
	}

	// Get shared status from storage configs
	sharedStatus := getSharedStorageStatus(exec)

	fmt.Printf("%-20s %-12s %-10s %-12s %-10s %-8s\n", "STORAGE", "TYPE", "STATUS", "TOTAL", "USED", "SHARED")
	fmt.Println("-------------------- ------------ ---------- ------------ ---------- --------")

	for _, line := range lines[1:] {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}

		name := fields[0]
		stype := fields[1]
		status := fields[2]
		total := fields[3]
		used := fields[4]

		shared := "No"
		if sharedStatus[name] {
			shared = "Yes"
		}

		fmt.Printf("%-20s %-12s %-10s %-12s %-10s %-8s\n", name, stype, status, total, used, shared)
	}

	fmt.Println()
	fmt.Println("Storage with SHARED=Yes is accessible to all nodes in the cluster.")
	fmt.Println("Store VM templates on shared storage to make them available cluster-wide.")
	fmt.Println()
	fmt.Println("To make templates accessible to all nodes:")
	fmt.Println("  1. Move to shared storage: cloud manage move-template --vm-id <id> --target <shared-storage>")
	fmt.Println("  2. Or replicate to all nodes: cloud manage replicate-template --vm-id <id> --template-id <id> --template-name <name>")

	return nil
}

func getSharedStorageStatus(exec CommandExecutor) map[string]bool {
	shared := make(map[string]bool)

	stdout, _, err := exec.Run("cat /etc/pve/storage.cfg")
	if err != nil {
		return shared
	}

	lines := strings.Split(stdout, "\n")
	var currentName string
	var currentType string

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "\t") {
			// Check for nodes restriction (if present, it's NOT shared)
			if strings.HasPrefix(strings.TrimSpace(line), "nodes ") && currentName != "" {
				delete(shared, currentName)
			}
			continue
		}

		// Parse lines like: "nfs: nfs-shared" or "lvmthin: local-lvm"
		if strings.Contains(line, ":") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				currentType = strings.TrimSpace(parts[0])
				currentName = strings.TrimSpace(parts[1])

				// Mark as shared by default for shared storage types
				if isSharedStorageType(currentType) {
					shared[currentName] = true
				}
			}
		}
	}

	return shared
}

func isSharedStorageType(stype string) bool {
	sharedTypes := map[string]bool{
		"cephfs":    true,
		"rbd":       true,
		"nfs":       true,
		"glusterfs": true,
		"iscsi":     true,
		"lvm":       true,
	}
	return sharedTypes[strings.ToLower(stype)]
}
