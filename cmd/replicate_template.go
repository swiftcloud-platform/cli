package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

var (
	replicateTemplateVMID   int
	replicateTemplateID     string
	replicateTemplateName   string
	replicateTemplateFamily string
	replicateStorage        string
	replicateMemory         int
	replicateCores          int
	replicateCloudUser      string
)

// replicateTemplateCmd represents the replicate-template command
var replicateTemplateCmd = &cobra.Command{
	Use:   "replicate-template",
	Short: "Replicate a VM template to all nodes in the cluster",
	Long: `Replicate a VM template to all nodes in the cluster by cloning it to each node's local storage.

This is useful when you don't have shared storage. The command:
  1. Lists all nodes in the cluster
  2. Clones the source template to each node's local storage
  3. Converts each clone to a template

Example usage (remote):
  cloud manage replicate-template \
    --ssh-host 192.168.1.100 \
    --ssh-username root \
    --ssh-password 'yourpassword' \
    --vm-id 9000 \
    --template-id ubuntu-24-04 \
    --template-name 'Ubuntu 24.04 LTS' \
    --storage local-lvm

Example usage (local):
  cloud manage replicate-template --local \
    --vm-id 9000 \
    --template-id ubuntu-24-04 \
    --template-name 'Ubuntu 24.04 LTS' \
    --storage local-lvm`,
	RunE: runReplicateTemplate,
}

func init() {
	manageCmd.AddCommand(replicateTemplateCmd)

	replicateTemplateCmd.Flags().BoolVar(&localMode, "local", false, "Run commands locally instead of via SSH")
	replicateTemplateCmd.Flags().StringVar(&sshHost, "ssh-host", "", "Proxmox host hostname or IP")
	replicateTemplateCmd.Flags().IntVar(&sshPort, "ssh-port", 22, "SSH port (default: 22)")
	replicateTemplateCmd.Flags().StringVar(&sshUsername, "ssh-username", "root", "SSH username (default: root)")
	replicateTemplateCmd.Flags().StringVar(&sshPassword, "ssh-password", "", "SSH password")
	replicateTemplateCmd.Flags().StringVar(&sshPrivateKey, "ssh-private-key", "", "Path to SSH private key file")
	replicateTemplateCmd.Flags().BoolVar(&sshInsecure, "ssh-insecure", false, "Skip host key verification")

	replicateTemplateCmd.Flags().IntVar(&replicateTemplateVMID, "vm-id", 0, "Source template VM ID")
	replicateTemplateCmd.Flags().StringVar(&replicateTemplateID, "template-id", "", "Template ID slug (e.g., ubuntu-24-04)")
	replicateTemplateCmd.Flags().StringVar(&replicateTemplateName, "template-name", "", "Display name for the template")
	replicateTemplateCmd.Flags().StringVar(&replicateTemplateFamily, "family-id", "", "OS family ID")
	replicateTemplateCmd.Flags().StringVar(&replicateStorage, "storage", "local-lvm", "Target storage on each node (default: local-lvm)")
	replicateTemplateCmd.Flags().IntVar(&replicateMemory, "vm-memory", 2048, "Memory in MB (default: 2048)")
	replicateTemplateCmd.Flags().IntVar(&replicateCores, "vm-cores", 2, "CPU cores (default: 2)")
	replicateTemplateCmd.Flags().StringVar(&replicateCloudUser, "cloud-user", "swift", "Cloud-Init username (default: swift)")

	replicateTemplateCmd.MarkFlagRequired("vm-id")
	replicateTemplateCmd.MarkFlagRequired("template-id")
	replicateTemplateCmd.MarkFlagRequired("template-name")

	replicateTemplateCmd.PreRunE = func(cmd *cobra.Command, args []string) error {
		if !localMode && sshHost == "" {
			return fmt.Errorf("--ssh-host is required when not using --local")
		}
		if !localMode && sshPassword == "" && sshPrivateKey == "" {
			return fmt.Errorf("either --ssh-password or --ssh-private-key is required when not using --local")
		}
		return nil
	}
}

func runReplicateTemplate(cmd *cobra.Command, args []string) error {
	mode := "remote (SSH)"
	if localMode {
		mode = "local"
	}

	fmt.Printf("🔄 Replicating template %d to all nodes (mode: %s)...\n\n", replicateTemplateVMID, mode)

	exec, closer, err := createExecutor()
	if err != nil {
		return err
	}
	defer closer()

	// Step 1: Get cluster nodes
	fmt.Println("📡 Discovering cluster nodes...")
	stdout, stderr, err := exec.Run("pvecm nodes")
	if err != nil {
		return fmt.Errorf("failed to list nodes: %s\n%s", stderr, stdout)
	}

	nodes := parseClusterNodes(stdout)
	if len(nodes) == 0 {
		return fmt.Errorf("no cluster nodes found")
	}
	fmt.Printf("   Found %d node(s): %s\n", len(nodes), strings.Join(nodes, ", "))
	fmt.Println()

	// Step 2: Get source template config
	fmt.Println("📋 Reading source template config...")
	stdout, stderr, err = exec.Run(fmt.Sprintf("qm config %d", replicateTemplateVMID))
	if err != nil {
		return fmt.Errorf("failed to read template config: %s\n%s", stderr, stdout)
	}
	fmt.Println("✅ Source template config read")
	fmt.Println()

	// Step 3: Clone to each node (skip source node)
	startVMID := replicateTemplateVMID + 1000 // Use offset to avoid conflicts
	successCount := 0
	skippedCount := 0

	for i, node := range nodes {
		targetVMID := startVMID + i
		vmName := fmt.Sprintf("tpl-%s-%s", replicateTemplateID, node)

		fmt.Printf("📦 Cloning to node '%s' (VM ID: %d)...\n", node, targetVMID)

		// Try direct clone with --target first (works with shared storage)
		cloneCmd := fmt.Sprintf("qm clone %d %d --name %s --target %s --full 1",
			replicateTemplateVMID, targetVMID, vmName, node)
		stdout, stderr, err = exec.Run(cloneCmd)
		if err != nil {
			// Check if it's a local storage issue
			if strings.Contains(stderr, "local storage") || strings.Contains(stderr, "can't clone") {
				fmt.Printf("   ⚠️  Cannot clone to '%s': template uses local storage\n", node)
				fmt.Printf("   💡 For local storage, run setup-vm-template on each node instead\n")
				skippedCount++
				continue
			}
			fmt.Printf("   ⚠️  Failed to clone to %s: %s\n", node, strings.TrimSpace(stderr))
			continue
		}

		// Convert to template
		templateCmd := fmt.Sprintf("qm template %d", targetVMID)
		stdout, stderr, err = exec.Run(templateCmd)
		if err != nil {
			fmt.Printf("   ⚠️  Failed to convert to template on %s: %s\n", node, strings.TrimSpace(stderr))
			continue
		}

		fmt.Printf("   ✅ Template created on %s (VM ID: %d)\n", node, targetVMID)
		successCount++
	}

	fmt.Println()
	fmt.Printf("🎉 Replication complete: %d/%d node(s) have the template", successCount, len(nodes))
	if skippedCount > 0 {
		fmt.Printf(" (%d skipped - local storage)", skippedCount)
	}
	fmt.Println()
	fmt.Println()
	fmt.Println("New template VM IDs:")
	for i, node := range nodes {
		targetVMID := startVMID + i
		status := "✅"
		if i >= successCount+skippedCount {
			status = "❌"
		} else if i >= successCount {
			status = "⏭️ "
		}
		fmt.Printf("   %s Node %-15s VM ID: %d\n", status, node, targetVMID)
	}

	if skippedCount > 0 {
		fmt.Println()
		fmt.Println("💡 To replicate to nodes with local storage:")
		fmt.Println("   1. Set up shared storage (Ceph, NFS, etc.)")
		fmt.Println("   2. Or run 'cloud manage setup-vm-template' on each node individually")
	}

	return nil
}

func parseClusterNodes(output string) []string {
	var nodes []string
	lines := strings.Split(output, "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)
		// Skip header lines
		if line == "" || strings.HasPrefix(line, "Membership") || strings.HasPrefix(line, "---") || strings.HasPrefix(line, "Nodeid") {
			continue
		}

		// Parse lines like: "1 1 dev (local)" or "2 1 dev-02"
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}

		// Node name is the last field (or second-to-last if last is "(local)")
		name := fields[len(fields)-1]
		if name == "(local)" && len(fields) >= 2 {
			name = fields[len(fields)-2]
		}
		name = strings.TrimSpace(name)
		if name != "" {
			nodes = append(nodes, name)
		}
	}

	return nodes
}
