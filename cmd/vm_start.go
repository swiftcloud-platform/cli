package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var vmStartCmd = &cobra.Command{
	Use:   "start <vmid>",
	Short: "Start a VM",
	Long:  `Start a virtual machine on a Proxmox node.`,
	Args:  cobra.ExactArgs(1),
	RunE:  runVMStart,
}

func init() {
	vmCmd.AddCommand(vmStartCmd)
	addCommonVMFlags(vmStartCmd)
}

func runVMStart(cmd *cobra.Command, args []string) error {
	vmid := parseVMID(args[0])

	exec, closer, err := createExecutor()
	if err != nil {
		return err
	}
	defer closer()

	fmt.Printf("▶️  Starting VM %d...\n", vmid)
	stdout, stderr, err := exec.Run(fmt.Sprintf("qm start %d", vmid))
	if err != nil {
		return fmt.Errorf("failed to start VM: %s\n%s", stderr, stdout)
	}
	fmt.Println("✅ VM started")
	return nil
}
