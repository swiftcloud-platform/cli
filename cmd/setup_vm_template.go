package cmd

import (
	"fmt"
	"strings"
	"time"

	"cloud/internal/executor"
	proxmoxssh "cloud/internal/ssh"
	"github.com/spf13/cobra"
)

// CommandExecutor abstracts command execution (SSH or local)
type CommandExecutor interface {
	Run(command string) (string, string, error)
}

var (
	// Execution mode
	localMode bool

	// SSH connection flags (used when not in local mode)
	sshHost          string
	sshPort          int
	sshUsername      string
	sshPassword      string
	sshPrivateKey    string
	sshInsecure      bool

	// Proxmox node flags
	proxmoxNode    string
	proxmoxStorage string

	// Template image flags
	imageURL     string
	imageName    string
	templateID   string
	templateName string
	familyID     string

	// VM configuration flags
	vmID      int
	vmMemory  int
	vmCores   int
	cloudUser string
)

// setupVMTemplateCmd represents the setup-vm-template command
var setupVMTemplateCmd = &cobra.Command{
	Use:   "setup-vm-template",
	Short: "Set up a VM template on a Proxmox node",
	Long: `Set up a VM template on a Proxmox node by downloading a cloud image,
creating a VM with Cloud-Init support, and converting it to a template.

This command can run in two modes:
  - Remote (default): SSHs into the Proxmox host and runs qm commands
  - Local (--local): Runs qm commands directly on the local machine (run on the Proxmox host itself)

Remote mode executes:
  1. Downloads the cloud image via wget/curl on the host
  2. Creates a VM (qm create)
  3. Imports the disk (qm importdisk)
  4. Configures the VM (qm set)
  5. Converts to template (qm template)

Example usage (remote):
  # Set up Ubuntu 24.04 template via SSH
  cloud manage setup-vm-template \
    --ssh-host 192.168.1.100 \
    --ssh-username root \
    --ssh-password 'yourpassword' \
    --proxmox-node pve1 \
    --proxmox-storage local-lvm \
    --image-url 'https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img' \
    --vm-id 9000 \
    --template-id ubuntu-24-04 \
    --template-name 'Ubuntu 24.04 LTS (Noble)' \
    --family-id ubuntu

Example usage (local - run on Proxmox host):
  # Set up Ubuntu 24.04 template locally
  cloud manage setup-vm-template \
    --local \
    --proxmox-node pve1 \
    --proxmox-storage local-lvm \
    --image-url 'https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img' \
    --vm-id 9000 \
    --template-id ubuntu-24-04 \
    --template-name 'Ubuntu 24.04 LTS (Noble)' \
    --family-id ubuntu`,
	RunE: runSetupVMTemplate,
}

func init() {
	manageCmd.AddCommand(setupVMTemplateCmd)

	// Execution mode flag
	setupVMTemplateCmd.Flags().BoolVar(&localMode, "local", false, "Run qm commands locally instead of via SSH (use when running on the Proxmox host)")

	// SSH connection flags (only used when --local is NOT set)
	setupVMTemplateCmd.Flags().StringVar(&sshHost, "ssh-host", "", "Proxmox host hostname or IP")
	setupVMTemplateCmd.Flags().IntVar(&sshPort, "ssh-port", 22, "SSH port (default: 22)")
	setupVMTemplateCmd.Flags().StringVar(&sshUsername, "ssh-username", "root", "SSH username (default: root)")
	setupVMTemplateCmd.Flags().StringVar(&sshPassword, "ssh-password", "", "SSH password")
	setupVMTemplateCmd.Flags().StringVar(&sshPrivateKey, "ssh-private-key", "", "Path to SSH private key file")
	setupVMTemplateCmd.Flags().BoolVar(&sshInsecure, "ssh-insecure", false, "Skip host key verification")

	// Proxmox node flags
	setupVMTemplateCmd.Flags().StringVar(&proxmoxNode, "proxmox-node", "", "Proxmox node name (e.g., pve1)")
	setupVMTemplateCmd.Flags().StringVar(&proxmoxStorage, "proxmox-storage", "local-lvm", "Proxmox storage name (default: local-lvm)")

	// Template image flags
	setupVMTemplateCmd.Flags().StringVar(&imageURL, "image-url", "", "URL to download the cloud image from")
	setupVMTemplateCmd.Flags().StringVar(&imageName, "image-name", "", "Filename for the downloaded image (optional, extracted from URL)")
	setupVMTemplateCmd.Flags().StringVar(&templateID, "template-id", "", "Unique slug for the template (e.g., ubuntu-24-04)")
	setupVMTemplateCmd.Flags().StringVar(&templateName, "template-name", "", "Display name for the template (e.g., Ubuntu 24.04 LTS)")
	setupVMTemplateCmd.Flags().StringVar(&familyID, "family-id", "", "OS family ID (e.g., ubuntu, debian, fedora, windows)")

	// VM configuration flags
	setupVMTemplateCmd.Flags().IntVar(&vmID, "vm-id", 0, "Proxmox VM ID for the template (e.g., 9000). Auto-generated if not provided")
	setupVMTemplateCmd.Flags().IntVar(&vmMemory, "vm-memory", 2048, "Memory in MB for the template VM (default: 2048)")
	setupVMTemplateCmd.Flags().IntVar(&vmCores, "vm-cores", 2, "CPU cores for the template VM (default: 2)")
	setupVMTemplateCmd.Flags().StringVar(&cloudUser, "cloud-user", "swift", "Default Cloud-Init username (default: swift)")

	// Mark required flags
	setupVMTemplateCmd.MarkFlagRequired("proxmox-node")
	setupVMTemplateCmd.MarkFlagRequired("image-url")
	setupVMTemplateCmd.MarkFlagRequired("template-id")
	setupVMTemplateCmd.MarkFlagRequired("template-name")

	// Validate flags
	setupVMTemplateCmd.PreRunE = func(cmd *cobra.Command, args []string) error {
		if !localMode {
			if sshHost == "" {
				return fmt.Errorf("--ssh-host is required when not using --local")
			}
			if sshPassword == "" && sshPrivateKey == "" {
				return fmt.Errorf("either --ssh-password or --ssh-private-key is required when not using --local")
			}
		}
		return nil
	}
}

func runSetupVMTemplate(cmd *cobra.Command, args []string) error {
	// Extract filename from URL if not provided
	if imageName == "" {
		parts := strings.Split(imageURL, "/")
		imageName = parts[len(parts)-1]
		if imageName == "" {
			return fmt.Errorf("could not extract filename from URL: %s", imageURL)
		}
	}

	// Auto-generate VM ID if not provided
	if vmID == 0 {
		vmID = generateVMID()
	}

	mode := "remote (SSH)"
	if localMode {
		mode = "local"
	}

	fmt.Printf("🚀 Starting VM template setup (mode: %s)...\n", mode)
	fmt.Printf("   Image URL: %s\n", imageURL)
	fmt.Printf("   Image Name: %s\n", imageName)
	fmt.Printf("   Proxmox Node: %s\n", proxmoxNode)
	fmt.Printf("   Proxmox Storage: %s\n", proxmoxStorage)
	fmt.Printf("   VM ID: %d\n", vmID)
	fmt.Printf("   Template ID: %s\n", templateID)
	fmt.Printf("   Template Name: %s\n", templateName)
	fmt.Println()

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

	// Step 1: Download the cloud image
	fmt.Println("📥 Downloading cloud image...")
	downloadCmd := fmt.Sprintf("wget -q --show-progress -O %s '%s' || curl -fSL -o %s '%s'", imageName, imageURL, imageName, imageURL)
	stdout, stderr, err := exec.Run(downloadCmd)
	if err != nil {
		return fmt.Errorf("failed to download image: %s\n%s", stderr, stdout)
	}
	fmt.Println("✅ Cloud image downloaded")
	fmt.Println()

	// Step 2: Create the VM (qm create)
	fmt.Printf("🔧 Creating VM (ID: %d)...\n", vmID)
	vmName := fmt.Sprintf("tpl-%s", templateID)
	createCmd := fmt.Sprintf("qm create %d --name %s --memory %d --cores %d --net0 virtio,bridge=vmbr0",
		vmID, vmName, vmMemory, vmCores)
	stdout, stderr, err = exec.Run(createCmd)
	if err != nil {
		return fmt.Errorf("failed to create VM: %s\n%s", stderr, stdout)
	}
	fmt.Println("✅ VM created")
	fmt.Println()

	// Step 3: Import the disk (qm importdisk)
	fmt.Println("💾 Importing disk to VM...")
	importCmd := fmt.Sprintf("qm importdisk %d %s %s", vmID, imageName, proxmoxStorage)
	stdout, stderr, err = exec.Run(importCmd)
	if err != nil {
		return fmt.Errorf("failed to import disk: %s\n%s", stderr, stdout)
	}
	fmt.Println("✅ Disk imported")
	fmt.Println()

	// Step 4: Attach the disk (qm set --scsihw --scsi0)
	fmt.Println("🔗 Attaching disk to VM...")
	attachCmd := fmt.Sprintf("qm set %d --scsihw virtio-scsi-pci --scsi0 %s:vm-%d-disk-0",
		vmID, proxmoxStorage, vmID)
	stdout, stderr, err = exec.Run(attachCmd)
	if err != nil {
		return fmt.Errorf("failed to attach disk: %s\n%s", stderr, stdout)
	}
	fmt.Println("✅ Disk attached")
	fmt.Println()

	// Step 5: Add Cloud-Init drive (qm set --ide2)
	fmt.Println("☁️  Adding Cloud-Init drive...")
	cloudinitCmd := fmt.Sprintf("qm set %d --ide2 %s:cloudinit", vmID, proxmoxStorage)
	stdout, stderr, err = exec.Run(cloudinitCmd)
	if err != nil {
		return fmt.Errorf("failed to add Cloud-Init drive: %s\n%s", stderr, stdout)
	}
	fmt.Println("✅ Cloud-Init drive added")
	fmt.Println()

	// Step 6: Set boot disk (qm set --boot --bootdisk)
	fmt.Println("👢 Setting boot disk...")
	bootCmd := fmt.Sprintf("qm set %d --boot c --bootdisk scsi0", vmID)
	stdout, stderr, err = exec.Run(bootCmd)
	if err != nil {
		return fmt.Errorf("failed to set boot disk: %s\n%s", stderr, stdout)
	}
	fmt.Println("✅ Boot disk set")
	fmt.Println()

	// Step 7: Configure serial console (qm set --serial0 --vga)
	fmt.Println("🖥️  Configuring serial console...")
	serialCmd := fmt.Sprintf("qm set %d --serial0 socket --vga serial0", vmID)
	stdout, stderr, err = exec.Run(serialCmd)
	if err != nil {
		return fmt.Errorf("failed to configure serial console: %s\n%s", stderr, stdout)
	}
	fmt.Println("✅ Serial console configured")
	fmt.Println()

	// Step 8: Set Cloud-Init user
	fmt.Printf("👤 Setting Cloud-Init user: %s\n", cloudUser)
	ciuserCmd := fmt.Sprintf("qm set %d --ciuser %s", vmID, cloudUser)
	stdout, stderr, err = exec.Run(ciuserCmd)
	if err != nil {
		return fmt.Errorf("failed to set Cloud-Init user: %s\n%s", stderr, stdout)
	}
	fmt.Println("✅ Cloud-Init user set")
	fmt.Println()

	// Step 9: Convert to template (qm template)
	fmt.Println("🔄 Converting VM to template...")
	templateCmd := fmt.Sprintf("qm template %d", vmID)
	stdout, stderr, err = exec.Run(templateCmd)
	if err != nil {
		return fmt.Errorf("failed to convert to template: %s\n%s", stderr, stdout)
	}
	fmt.Println("✅ VM converted to template")
	fmt.Println()

	// Step 10: Clean up the downloaded image
	fmt.Println("🧹 Cleaning up downloaded image...")
	cleanupCmd := fmt.Sprintf("rm -f %s", imageName)
	_, _, _ = exec.Run(cleanupCmd)
	fmt.Println("✅ Cleanup complete")
	fmt.Println()

	// Summary
	fmt.Println("🎉 VM template setup completed successfully!")
	fmt.Println()
	fmt.Println("Summary:")
	fmt.Printf("   Template ID:    %s\n", templateID)
	fmt.Printf("   Template Name:  %s\n", templateName)
	fmt.Printf("   Proxmox VM ID:  %d\n", vmID)
	fmt.Printf("   OS Family:      %s\n", familyID)
	fmt.Printf("   Node:           %s\n", proxmoxNode)
	fmt.Printf("   Storage:        %s\n", proxmoxStorage)
	fmt.Println()
	fmt.Println("The template is now ready for use in the SwiftCloud platform.")

	return nil
}

// sshExecutor wraps the SSH client to implement CommandExecutor
type sshExecutor struct {
	client *proxmoxssh.Client
}

func (e *sshExecutor) Run(command string) (string, string, error) {
	return e.client.Run(command)
}

// createExecutor creates a CommandExecutor based on the current flags
func createExecutor() (CommandExecutor, func() error, error) {
	if localMode {
		return executor.NewLocalExecutor(), func() error { return nil }, nil
	}

	sshClient, err := proxmoxssh.NewClient(proxmoxssh.Config{
		Host:           sshHost,
		Port:           sshPort,
		Username:       sshUsername,
		Password:       sshPassword,
		PrivateKeyPath: sshPrivateKey,
		Insecure:       sshInsecure,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to connect via SSH: %w", err)
	}
	return &sshExecutor{client: sshClient}, sshClient.Close, nil
}

// generateVMID generates a VM ID that doesn't conflict with existing templates
// Uses a strategy starting from 9000 range
func generateVMID() int {
	// Start from 9000 and use timestamp-based offset to avoid conflicts
	return 9000 + (int(time.Now().Unix()) % 1000)
}
