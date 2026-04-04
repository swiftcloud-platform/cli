package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

var vmStatusCmd = &cobra.Command{
	Use:   "status <vmid>",
	Short: "Get VM status",
	Long:  `Get the current status of a virtual machine.`,
	Args:  cobra.ExactArgs(1),
	RunE:  runVMStatus,
}

var vmConfigCmd = &cobra.Command{
	Use:   "config <vmid>",
	Short: "Get VM configuration",
	Long:  `Get the full configuration of a virtual machine.`,
	Args:  cobra.ExactArgs(1),
	RunE:  runVMConfig,
}

func init() {
	vmCmd.AddCommand(vmStatusCmd)
	vmCmd.AddCommand(vmConfigCmd)
	addCommonVMFlags(vmStatusCmd)
	addCommonVMFlags(vmConfigCmd)
}

func runVMStatus(cmd *cobra.Command, args []string) error {
	vmid := parseVMID(args[0])
	exec, closer, err := createExecutor()
	if err != nil {
		return err
	}
	defer closer()

	stdout, stderr, err := exec.Run(fmt.Sprintf("qm status %d", vmid))
	if err != nil {
		return fmt.Errorf("failed to get VM status: %s\n%s", stderr, stdout)
	}

	fmt.Printf("📊 VM %d Status:\n", vmid)
	fmt.Println(strings.TrimSpace(stdout))
	return nil
}

func runVMConfig(cmd *cobra.Command, args []string) error {
	vmid := parseVMID(args[0])
	exec, closer, err := createExecutor()
	if err != nil {
		return err
	}
	defer closer()

	stdout, stderr, err := exec.Run(fmt.Sprintf("qm config %d", vmid))
	if err != nil {
		return fmt.Errorf("failed to get VM config: %s\n%s", stderr, stdout)
	}

	fmt.Printf("⚙️  VM %d Configuration:\n", vmid)
	fmt.Println(strings.TrimSpace(stdout))
	return nil
}
