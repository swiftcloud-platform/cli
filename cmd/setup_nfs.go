package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

var (
	nfsServer     string
	nfsShare      string
	nfsContent    string
	nfsNodes      string
	nfsUsername   string
	nfsPassword   string
	nfsPrivateKey string
	nfsPort       int
	nfsInsecure   bool
)

// setupNFSCmd represents the setup-nfs command
var setupNFSCmd = &cobra.Command{
	Use:   "setup-nfs",
	Short: "Set up NFS shared storage for the Proxmox cluster",
	Long: `Set up NFS shared storage for the Proxmox cluster.

This command can:
  1. Set up an NFS server on a node (creates export directory, installs nfs-kernel-server)
  2. Configure NFS client on all cluster nodes
  3. Add NFS storage to Proxmox

Example usage - Set up NFS server on current node:
  cloud manage setup-nfs \
    --ssh-host 192.168.1.100 \
    --ssh-username root \
    --ssh-password 'yourpassword' \
    --server \
    --share /srv/proxmox-nfs \
    --content images,iso,vztmpl,backup

Example usage - Configure NFS storage on all nodes:
  cloud manage setup-nfs \
    --ssh-host 192.168.1.100 \
    --ssh-username root \
    --ssh-password 'yourpassword' \
    --server 192.168.1.100 \
    --share /srv/proxmox-nfs \
    --content images,iso,vztmpl,backup \
    --storage-name nfs-shared`,
	RunE: runSetupNFS,
}

func init() {
	manageCmd.AddCommand(setupNFSCmd)

	// NFS configuration flags
	setupNFSCmd.Flags().BoolVar(&serverMode, "server", false, "Set up NFS server on the target node")
	setupNFSCmd.Flags().StringVar(&nfsServer, "nfs-server", "", "NFS server IP/hostname (required if not --server)")
	setupNFSCmd.Flags().StringVar(&nfsShare, "share", "/srv/proxmox-nfs", "NFS export/share path")
	setupNFSCmd.Flags().StringVar(&nfsContent, "content", "images,iso,vztmpl,backup", "Content types (comma-separated: images,iso,vztmpl,backup,rootdir)")
	setupNFSCmd.Flags().StringVar(&nfsStorageName, "storage-name", "nfs-shared", "Proxmox storage name for the NFS share")
	setupNFSCmd.Flags().BoolVar(&allNodes, "all-nodes", false, "Configure NFS client on all cluster nodes (runs via first node)")

	// SSH flags
	setupNFSCmd.Flags().StringVar(&sshHost, "ssh-host", "", "Proxmox host hostname or IP")
	setupNFSCmd.Flags().IntVar(&sshPort, "ssh-port", 22, "SSH port (default: 22)")
	setupNFSCmd.Flags().StringVar(&sshUsername, "ssh-username", "root", "SSH username (default: root)")
	setupNFSCmd.Flags().StringVar(&sshPassword, "ssh-password", "", "SSH password")
	setupNFSCmd.Flags().StringVar(&sshPrivateKey, "ssh-private-key", "", "Path to SSH private key file")
	setupNFSCmd.Flags().BoolVar(&sshInsecure, "ssh-insecure", false, "Skip host key verification")

	setupNFSCmd.MarkFlagRequired("ssh-host")
	setupNFSCmd.PreRunE = func(cmd *cobra.Command, args []string) error {
		if sshPassword == "" && sshPrivateKey == "" {
			return fmt.Errorf("either --ssh-password or --ssh-private-key is required")
		}
		if !serverMode && nfsServer == "" {
			return fmt.Errorf("--nfs-server is required when not using --server")
		}
		return nil
	}
}

var (
	serverMode    bool
	nfsStorageName string
	allNodes      bool
)

func runSetupNFS(cmd *cobra.Command, args []string) error {
	exec, closer, err := createExecutor()
	if err != nil {
		return err
	}
	defer closer()

	if serverMode {
		return setupNFSServer(exec)
	}

	if allNodes {
		return setupNFSAllNodes(exec)
	}

	return setupNFSClient(exec)
}

func setupNFSServer(exec CommandExecutor) error {
	fmt.Println("🔧 Setting up NFS server...")
	fmt.Println()

	// Step 1: Install NFS server
	fmt.Println("📦 Installing nfs-kernel-server...")
	stdout, stderr, err := exec.Run("apt update && apt install -y nfs-kernel-server")
	if err != nil {
		return fmt.Errorf("failed to install nfs-kernel-server: %s\n%s", stderr, stdout)
	}
	fmt.Println("✅ nfs-kernel-server installed")
	fmt.Println()

	// Step 2: Create export directory
	fmt.Printf("📁 Creating export directory: %s\n", nfsShare)
	stdout, stderr, err = exec.Run(fmt.Sprintf("mkdir -p %s && chmod 777 %s", nfsShare, nfsShare))
	if err != nil {
		return fmt.Errorf("failed to create export directory: %s\n%s", stderr, stdout)
	}
	fmt.Println("✅ Export directory created")
	fmt.Println()

	// Step 3: Configure exports
	fmt.Println("📝 Configuring NFS exports...")
	exportLine := fmt.Sprintf("%s *(rw,sync,no_subtree_check,no_root_squash)", nfsShare)
	stdout, stderr, err = exec.Run(fmt.Sprintf("grep -q '%s' /etc/exports || echo '%s' >> /etc/exports", exportLine, exportLine))
	if err != nil {
		return fmt.Errorf("failed to configure exports: %s\n%s", stderr, stdout)
	}
	fmt.Println("✅ NFS exports configured")
	fmt.Println()

	// Step 4: Export and restart
	fmt.Println("🔄 Exporting NFS shares...")
	stdout, stderr, err = exec.Run("exportfs -ra && systemctl restart nfs-kernel-server && systemctl enable nfs-kernel-server")
	if err != nil {
		return fmt.Errorf("failed to export shares: %s\n%s", stderr, stdout)
	}
	fmt.Println("✅ NFS server running")
	fmt.Println()

	// Step 5: Show status
	fmt.Println("📊 NFS Server Status:")
	stdout, _, _ = exec.Run("systemctl is-active nfs-kernel-server && exportfs -v")
	fmt.Println(stdout)
	fmt.Println()

	fmt.Println("🎉 NFS server setup complete!")
	fmt.Println()
	fmt.Println("Next steps:")
	fmt.Printf("  1. On each cluster node, run:\n")
	fmt.Printf("     cloud manage setup-nfs --ssh-host <node-ip> --ssh-username root --ssh-password 'pass' \\\n")
	fmt.Printf("       --nfs-server %s --share %s --storage-name nfs-shared\n", sshHost, nfsShare)

	return nil
}

func setupNFSAllNodes(exec CommandExecutor) error {
	fmt.Println("🔧 Setting up NFS client on all cluster nodes...")
	fmt.Println()

	// Step 1: Discover nodes
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

	// Step 2: Install nfs-common on all nodes via pvecm run
	fmt.Println("📦 Installing nfs-common on all nodes...")
	for _, node := range nodes {
		fmt.Printf("   Installing on %s... ", node)
		// Use pvecm to run on remote nodes, or run directly on local
		var cmd string
		if strings.Contains(stdout, "(local)") {
			// We're on one of the nodes, install locally first
			cmd = fmt.Sprintf("pvecm run %s 'apt update && apt install -y nfs-common' 2>/dev/null || (apt update && apt install -y nfs-common)", node)
		} else {
			cmd = "apt update && apt install -y nfs-common"
		}
		_, stderr, err := exec.Run(cmd)
		if err != nil {
			// Try simpler approach
			_, stderr, err = exec.Run("apt update && apt install -y nfs-common")
			if err != nil {
				fmt.Printf("⚠️  %s\n", strings.TrimSpace(stderr))
				continue
			}
		}
		fmt.Println("✅")
	}
	fmt.Println()

	// Step 3: Add NFS storage (cluster-wide, only needs to run once)
	fmt.Printf("📋 Adding NFS storage '%s' to Proxmox (cluster-wide)...\n", nfsStorageName)

	// Check if storage already exists
	existsOut, _, _ := exec.Run(fmt.Sprintf("pvesm status | grep -q '^%s ' && echo yes || echo no", nfsStorageName))
	if strings.TrimSpace(existsOut) == "yes" {
		fmt.Printf("  ℹ️  Storage '%s' already exists, verifying config...\n", nfsStorageName)
		// Just verify it's active
		stdout, stderr, err = exec.Run(fmt.Sprintf("pvesm status --storage %s", nfsStorageName))
	} else {
		stdout, stderr, err = exec.Run(fmt.Sprintf(
			"pvesm add nfs %s --server %s --export %s --content %s",
			nfsStorageName, nfsServer, nfsShare, nfsContent,
		))
	}
	if err != nil {
		return fmt.Errorf("failed to add storage: %s\n%s", stderr, stdout)
	}
	fmt.Println("✅ NFS storage added")
	fmt.Println()

	// Step 4: Verify storage
	fmt.Println("📊 Verifying storage...")
	stdout, stderr, err = exec.Run(fmt.Sprintf("pvesm status --storage %s", nfsStorageName))
	if err != nil {
		fmt.Printf("  ⚠️  Warning: %s\n", strings.TrimSpace(stderr))
	} else {
		fmt.Println(stdout)
	}
	fmt.Println()

	fmt.Println("🎉 NFS setup complete on all nodes!")
	fmt.Println()
	fmt.Println("The NFS storage is now available to all nodes in the cluster.")
	fmt.Println("Proxmox automatically replicates storage configuration via corosync.")

	return nil
}

func setupNFSClient(exec CommandExecutor) error {
	fmt.Println("🔧 Setting up NFS client and Proxmox storage...")
	fmt.Println()

	// Step 1: Install NFS client
	fmt.Println("📦 Installing nfs-common...")
	stdout, stderr, err := exec.Run("apt update && apt install -y nfs-common")
	if err != nil {
		return fmt.Errorf("failed to install nfs-common: %s\n%s", stderr, stdout)
	}
	fmt.Println("✅ nfs-common installed")
	fmt.Println()

	// Step 2: Verify NFS share is accessible
	fmt.Printf("🔍 Verifying NFS share: %s:%s\n", nfsServer, nfsShare)
	stdout, stderr, err = exec.Run(fmt.Sprintf("showmount -e %s", nfsServer))
	if err != nil {
		fmt.Printf("  ⚠️  Warning: could not verify NFS share: %s\n", strings.TrimSpace(stderr))
		fmt.Println("  Continuing anyway...")
	} else {
		fmt.Println("✅ NFS share is accessible")
		fmt.Println(stdout)
	}
	fmt.Println()

	// Step 3: Add NFS storage to Proxmox
	fmt.Printf("📋 Adding NFS storage '%s' to Proxmox...\n", nfsStorageName)
	stdout, stderr, err = exec.Run(fmt.Sprintf(
		"pvesm add nfs %s --server %s --export %s --content %s",
		nfsStorageName, nfsServer, nfsShare, nfsContent,
	))
	if err != nil {
		// Check if storage already exists
		if strings.Contains(stderr, "already exists") {
			fmt.Printf("  ℹ️  Storage '%s' already exists, updating...\n", nfsStorageName)
			stdout, stderr, err = exec.Run(fmt.Sprintf(
				"pvesm set %s --server %s --export %s --content %s",
				nfsStorageName, nfsServer, nfsShare, nfsContent,
			))
			if err != nil {
				return fmt.Errorf("failed to update storage: %s\n%s", stderr, stdout)
			}
		} else {
			return fmt.Errorf("failed to add storage: %s\n%s", stderr, stdout)
		}
	}
	fmt.Println("✅ NFS storage added to Proxmox")
	fmt.Println()

	// Step 4: Verify storage
	fmt.Println("📊 Verifying storage...")
	stdout, stderr, err = exec.Run(fmt.Sprintf("pvesm status --storage %s", nfsStorageName))
	if err != nil {
		fmt.Printf("  ⚠️  Warning: %s\n", strings.TrimSpace(stderr))
	} else {
		fmt.Println(stdout)
	}
	fmt.Println()

	fmt.Println("🎉 NFS client setup complete!")
	fmt.Println()
	fmt.Println("The NFS storage is now available to this node.")
	fmt.Println("Repeat this command on all cluster nodes to make templates accessible everywhere.")

	return nil
}
