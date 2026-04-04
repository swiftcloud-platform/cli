package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var vmStopCmd = &cobra.Command{
	Use:   "stop <vmid>",
	Short: "Stop a VM (hard power off)",
	Long:  `Stop a virtual machine immediately (hard power off). Use 'shutdown' for graceful shutdown.`,
	Args:  cobra.ExactArgs(1),
	RunE:  runVMStop,
}

func init() {
	vmCmd.AddCommand(vmStopCmd)
	addCommonVMFlags(vmStopCmd)
}

func runVMStop(cmd *cobra.Command, args []string) error {
	vmid := parseVMID(args[0])

	exec, closer, err := createExecutor()
	if err != nil {
		return err
	}
	defer closer()

	fmt.Printf("⏹️  Stopping VM %d...\n", vmid)
	stdout, stderr, err := exec.Run(fmt.Sprintf("qm stop %d", vmid))
	if err != nil {
		return fmt.Errorf("failed to stop VM: %s\n%s", stderr, stdout)
	}
	fmt.Println("✅ VM stopped")
	return nil
}
