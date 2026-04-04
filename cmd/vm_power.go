package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var vmShutdownCmd = &cobra.Command{
	Use:   "shutdown <vmid>",
	Short: "Shutdown a VM (graceful)",
	Long:  `Gracefully shutdown a virtual machine via ACPI (requires QEMU guest agent).`,
	Args:  cobra.ExactArgs(1),
	RunE:  runVMShutdown,
}

var vmRestartCmd = &cobra.Command{
	Use:   "restart <vmid>",
	Short: "Restart a VM",
	Long:  `Restart a virtual machine.`,
	Args:  cobra.ExactArgs(1),
	RunE:  runVMRestart,
}

var vmResetCmd = &cobra.Command{
	Use:   "reset <vmid>",
	Short: "Reset a VM",
	Long:  `Reset a virtual machine (equivalent to pressing the reset button).`,
	Args:  cobra.ExactArgs(1),
	RunE:  runVMReset,
}

func init() {
	vmCmd.AddCommand(vmShutdownCmd)
	vmCmd.AddCommand(vmRestartCmd)
	vmCmd.AddCommand(vmResetCmd)
	addCommonVMFlags(vmShutdownCmd)
	addCommonVMFlags(vmRestartCmd)
	addCommonVMFlags(vmResetCmd)
}

func runVMShutdown(cmd *cobra.Command, args []string) error {
	vmid := parseVMID(args[0])
	exec, closer, err := createExecutor()
	if err != nil {
		return err
	}
	defer closer()

	fmt.Printf("🔌 Shutting down VM %d...\n", vmid)
	stdout, stderr, err := exec.Run(fmt.Sprintf("qm shutdown %d", vmid))
	if err != nil {
		return fmt.Errorf("failed to shutdown VM: %s\n%s", stderr, stdout)
	}
	fmt.Println("✅ VM shutdown initiated")
	return nil
}

func runVMRestart(cmd *cobra.Command, args []string) error {
	vmid := parseVMID(args[0])
	exec, closer, err := createExecutor()
	if err != nil {
		return err
	}
	defer closer()

	fmt.Printf("🔄 Restarting VM %d...\n", vmid)
	stdout, stderr, err := exec.Run(fmt.Sprintf("qm reboot %d", vmid))
	if err != nil {
		return fmt.Errorf("failed to restart VM: %s\n%s", stderr, stdout)
	}
	fmt.Println("✅ VM restarted")
	return nil
}

func runVMReset(cmd *cobra.Command, args []string) error {
	vmid := parseVMID(args[0])
	exec, closer, err := createExecutor()
	if err != nil {
		return err
	}
	defer closer()

	fmt.Printf("🔁 Resetting VM %d...\n", vmid)
	stdout, stderr, err := exec.Run(fmt.Sprintf("qm reset %d", vmid))
	if err != nil {
		return fmt.Errorf("failed to reset VM: %s\n%s", stderr, stdout)
	}
	fmt.Println("✅ VM reset")
	return nil
}
