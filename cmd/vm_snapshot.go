package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

var (
	snapshotDescription string
)

var vmSnapshotCmd = &cobra.Command{
	Use:   "snapshot",
	Short: "Manage VM snapshots",
	Long:  `Create, list, delete, and revert VM snapshots.`,
}

var vmSnapshotCreateCmd = &cobra.Command{
	Use:   "create <vmid> <name>",
	Short: "Create a VM snapshot",
	Long:  `Create a snapshot of a virtual machine.`,
	Args:  cobra.ExactArgs(2),
	RunE:  runVMSnapshotCreate,
}

var vmSnapshotListCmd = &cobra.Command{
	Use:   "list <vmid>",
	Short: "List VM snapshots",
	Long:  `List all snapshots of a virtual machine.`,
	Args:  cobra.ExactArgs(1),
	RunE:  runVMSnapshotList,
}

var vmSnapshotDeleteCmd = &cobra.Command{
	Use:   "delete <vmid> <name>",
	Short: "Delete a VM snapshot",
	Long:  `Delete a snapshot of a virtual machine.`,
	Args:  cobra.ExactArgs(2),
	RunE:  runVMSnapshotDelete,
}

var vmSnapshotRevertCmd = &cobra.Command{
	Use:   "revert <vmid> <name>",
	Short: "Revert to a VM snapshot",
	Long:  `Revert a virtual machine to a snapshot.`,
	Args:  cobra.ExactArgs(2),
	RunE:  runVMSnapshotRevert,
}

func init() {
	vmCmd.AddCommand(vmSnapshotCmd)
	vmSnapshotCmd.AddCommand(vmSnapshotCreateCmd)
	vmSnapshotCmd.AddCommand(vmSnapshotListCmd)
	vmSnapshotCmd.AddCommand(vmSnapshotDeleteCmd)
	vmSnapshotCmd.AddCommand(vmSnapshotRevertCmd)

	vmSnapshotCreateCmd.Flags().StringVar(&snapshotDescription, "description", "", "Snapshot description")
	addCommonVMFlags(vmSnapshotCreateCmd)
	addCommonVMFlags(vmSnapshotListCmd)
	addCommonVMFlags(vmSnapshotDeleteCmd)
	addCommonVMFlags(vmSnapshotRevertCmd)
}

func runVMSnapshotCreate(cmd *cobra.Command, args []string) error {
	vmid := parseVMID(args[0])
	name := args[1]

	exec, closer, err := createExecutor()
	if err != nil {
		return err
	}
	defer closer()

	cmdStr := fmt.Sprintf("qm snapshot %d %s", vmid, name)
	if snapshotDescription != "" {
		cmdStr += fmt.Sprintf(" --description '%s'", snapshotDescription)
	}

	fmt.Printf("📸 Creating snapshot '%s' for VM %d...\n", name, vmid)
	stdout, stderr, err := exec.Run(cmdStr)
	if err != nil {
		return fmt.Errorf("failed to create snapshot: %s\n%s", stderr, stdout)
	}
	fmt.Println("✅ Snapshot created")
	return nil
}

func runVMSnapshotList(cmd *cobra.Command, args []string) error {
	vmid := parseVMID(args[0])

	exec, closer, err := createExecutor()
	if err != nil {
		return err
	}
	defer closer()

	stdout, stderr, err := exec.Run(fmt.Sprintf("qm listsnapshot %d", vmid))
	if err != nil {
		return fmt.Errorf("failed to list snapshots: %s\n%s", stderr, stdout)
	}

	fmt.Printf("📋 VM %d Snapshots:\n", vmid)
	fmt.Println(strings.TrimSpace(stdout))
	return nil
}

func runVMSnapshotDelete(cmd *cobra.Command, args []string) error {
	vmid := parseVMID(args[0])
	name := args[1]

	exec, closer, err := createExecutor()
	if err != nil {
		return err
	}
	defer closer()

	fmt.Printf("🗑️  Deleting snapshot '%s' from VM %d...\n", name, vmid)
	stdout, stderr, err := exec.Run(fmt.Sprintf("qm delsnapshot %d %s", vmid, name))
	if err != nil {
		return fmt.Errorf("failed to delete snapshot: %s\n%s", stderr, stdout)
	}
	fmt.Println("✅ Snapshot deleted")
	return nil
}

func runVMSnapshotRevert(cmd *cobra.Command, args []string) error {
	vmid := parseVMID(args[0])
	name := args[1]

	exec, closer, err := createExecutor()
	if err != nil {
		return err
	}
	defer closer()

	fmt.Printf("🔄 Reverting VM %d to snapshot '%s'...\n", vmid, name)
	stdout, stderr, err := exec.Run(fmt.Sprintf("qm rollback %d %s", vmid, name))
	if err != nil {
		return fmt.Errorf("failed to revert snapshot: %s\n%s", stderr, stdout)
	}
	fmt.Println("✅ VM reverted to snapshot")
	return nil
}
