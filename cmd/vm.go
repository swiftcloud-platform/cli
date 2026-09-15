package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"cloud/internal/api"
	"cloud/internal/output"
	"cloud/internal/wait"
)

/*
Virtual machines — v1.1.

KubeVirt-backed VMs with power management, console access via an embedded
SPICE client, SSH key management and public IP attach/detach.

Statuses are the worker's to write. Nothing below decides a VM is ready;
--wait polls and, when nothing moves, reports the worker heartbeat age
through the same healthProbe the app commands use.
*/

var (
	vmCreateImage   string
	vmCreateSize    string
	vmCreateRegion  string
	vmCreateDesc    string
	vmCreateSSHKey  string
	vmDeleteYes     bool
	vmSSHKeyLabel   string
	vmSSHKeyRemoveYes bool
	vmIPDetachYes   bool
)

// ── table shapes ────────────────────────────────────────────────────────────

type vmRows []api.Vm

func (r vmRows) Columns() []string {
	return []string{"NAME", "STATUS", "IMAGE", "SIZE", "REGION", "PUBLIC IP"}
}
func (r vmRows) Rows() [][]string {
	out := make([][]string, len(r))
	for i, v := range r {
		out[i] = []string{v.Name, v.Status, v.Image, v.Size, v.Region, orDash(v.PublicIp)}
	}
	return out
}
func (r vmRows) IDs() []string {
	out := make([]string, len(r))
	for i, v := range r {
		out[i] = v.Name
	}
	return out
}

type vmSshKeyRows []api.VmSshKey

func (r vmSshKeyRows) Columns() []string { return []string{"ID", "LABEL", "FINGERPRINT", "ADDED"} }
func (r vmSshKeyRows) Rows() [][]string {
	out := make([][]string, len(r))
	for i, k := range r {
		out[i] = []string{k.Id, orDash(k.Label), k.Fingerprint, k.AddedAt.Local().Format("2006-01-02 15:04")}
	}
	return out
}
func (r vmSshKeyRows) IDs() []string {
	out := make([]string, len(r))
	for i, k := range r {
		out[i] = k.Id
	}
	return out
}

// ── waiting ─────────────────────────────────────────────────────────────────

// waitForVM polls GET vm until its status is terminal, printing each change
// to stderr. It shares terminalStatus and healthProbe with the app commands.
func waitForVM(cmd *cobra.Command, c *api.ClientWithResponses, org, name string, timeout time.Duration) (*api.Vm, error) {
	var last *api.Vm
	poll := func(ctx context.Context) (string, bool, error) {
		res, err := c.GetOrgsOrgVmsVmWithResponse(ctx, org, name)
		if err != nil {
			return "", false, err
		}
		if err := apiErr(res.StatusCode(), res.Body); err != nil {
			return "", false, err
		}
		if res.JSON200 == nil {
			return "", false, fmt.Errorf("unexpected response from %s", cfg.APIURL)
		}
		last = res.JSON200
		done, failed := terminalStatus(last.Status)
		if failed {
			return last.Status, false, &wait.ErrFailed{Status: last.Status, Reason: last.ErrorMessage}
		}
		return last.Status, done, nil
	}
	_, err := wait.Until(cmd.Context(), poll, healthProbe(c), wait.Options{
		Interval: 3 * time.Second,
		Timeout:  timeout,
		OnStatus: func(s string) {
			if !flagQuiet {
				fmt.Fprintf(cmd.ErrOrStderr(), "  %s\n", s)
			}
		},
	})
	return last, err
}

// printVM renders one VM, waiting first when --wait was given.
func printVM(cmd *cobra.Command, c *api.ClientWithResponses, org string, vm *api.Vm, verb string) error {
	if flagWait {
		waited, err := waitForVM(cmd, c, org, vm.Name, flagTimeout)
		if err != nil {
			return err
		}
		if waited != nil {
			vm = waited
		}
	}
	if !flagQuiet && printer.Format == output.Table {
		fmt.Fprintf(cmd.ErrOrStderr(), "%s %s — status: %s\n", verb, vm.Name, vm.Status)
		if vm.PublicIp != "" {
			fmt.Fprintf(cmd.ErrOrStderr(), "Public IP:  %s\n", vm.PublicIp)
		}
		if vm.PrivateIp != "" {
			fmt.Fprintf(cmd.ErrOrStderr(), "Private IP: %s\n", vm.PrivateIp)
		}
		if vm.ErrorMessage != "" {
			fmt.Fprintf(cmd.ErrOrStderr(), "%s\n", vm.ErrorMessage)
		}
		return nil
	}
	return printer.Print(vmRows{*vm})
}

// ── commands ────────────────────────────────────────────────────────────────

var vmCmd = &cobra.Command{
	Use:   "vm",
	Short: "Virtual machines",
	Long: `Virtual machines: a KubeVirt-backed VM with power management, console
access, SSH keys and public IP assignment.

Every command below except "list" takes the VM's name as its first argument,
before any flags. VMs belong to an organisation, taken from --org, CLOUD_ORG
or the current context, so the name alone is enough to identify one.`,
	Example: `  cloud vm list
  cloud vm create web --image ubuntu-24.04 --wait
  cloud vm get web
  cloud vm console web
  cloud vm ip attach web`,
}

var vmListCmd = &cobra.Command{
	Use:   "list",
	Short: "List VMs in the organisation",
	Example: `  cloud vm list
  cloud vm list -o json
  cloud vm list --quiet            # names only, one per line, for scripts`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		org, err := requireOrg()
		if err != nil {
			return err
		}
		c, _, err := apiClient()
		if err != nil {
			return err
		}
		res, err := c.GetOrgsOrgVmsWithResponse(cmd.Context(), org, nil)
		if err != nil {
			return reachErr(err)
		}
		if err := apiErr(res.StatusCode(), res.Body); err != nil {
			return err
		}
		list, err := decoded(res.JSON200)
		if err != nil {
			return err
		}
		return printer.Print(vmRows(list.Items))
	},
}

var vmCreateCmd = &cobra.Command{
	Use:   "create <name> --image <ref>",
	Short: "Create a virtual machine",
	Long: `Create a VM. The name is yours to choose and is how every other command
refers to it; --image is the only required flag.

The VM is created immediately but starts as "provisioning"; pass --wait to
block until it is running (or fails).`,
	Example: `  cloud vm create web --image ubuntu-24.04
  cloud vm create web --image ubuntu-24.04 --size vm-2 --region zm-lusaka-central-1 --wait
  cloud vm create web --image ubuntu-24.04 --ssh-key ~/.ssh/id_ed25519.pub
  cloud vm create web --image ubuntu-24.04 --ssh-key ssh-ed25519 AAAA...`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		org, err := requireOrg()
		if err != nil {
			return err
		}
		region := vmCreateRegion
		if region == "" {
			region = cfg.Region
		}
		if region == "" {
			return &UsageError{errors.New("no region given — pass --region, set CLOUD_REGION, or add one to your context (see `cloud region list`)")}
		}
		body := api.PostOrgsOrgVmsJSONRequestBody{
			Name:  args[0],
			Image: vmCreateImage,
		}
		if region != "" {
			body.Region = region
		}
		if vmCreateSize != "" {
			body.Size = &vmCreateSize
		}
		if vmCreateDesc != "" {
			body.Description = &vmCreateDesc
		}
		if vmCreateSSHKey != "" {
			key, err := readSSHKey(vmCreateSSHKey)
			if err != nil {
				return err
			}
			body.SshPublicKey = &key
		}

		c, _, err := apiClient()
		if err != nil {
			return err
		}
		res, err := c.PostOrgsOrgVmsWithResponse(cmd.Context(), org, body)
		if err != nil {
			return reachErr(err)
		}
		if err := apiErr(res.StatusCode(), res.Body); err != nil {
			return err
		}
		vm, err := decoded(res.JSON201)
		if err != nil {
			return err
		}
		return printVM(cmd, c, org, vm, "Creating")
	},
}

var vmGetCmd = &cobra.Command{
	Use:   "get <name>",
	Short: "Show a VM",
	Example: `  cloud vm get web
  cloud vm get web -o yaml
  cloud vm get web -o json | jq -r .publicIp`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		org, err := requireOrg()
		if err != nil {
			return err
		}
		c, _, err := apiClient()
		if err != nil {
			return err
		}
		res, err := c.GetOrgsOrgVmsVmWithResponse(cmd.Context(), org, args[0])
		if err != nil {
			return reachErr(err)
		}
		if err := apiErr(res.StatusCode(), res.Body); err != nil {
			return err
		}
		v, err := decoded(res.JSON200)
		if err != nil {
			return err
		}
		if printer.Format != output.Table {
			return printer.Print(vmRows{*v})
		}
		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "Name         %s\nStatus       %s\nImage        %s\nSize         %s\nRegion       %s\nPrivate IP   %s\nPublic IP    %s\n",
			v.Name, v.Status, v.Image, orDash(v.Size), orDash(v.Region), orDash(v.PrivateIp), orDash(v.PublicIp))
		if v.ErrorMessage != "" {
			fmt.Fprintf(out, "Reason       %s\n", v.ErrorMessage)
		}
		if v.Description != "" {
			fmt.Fprintf(out, "Description  %s\n", v.Description)
		}
		fmt.Fprintf(out, "Created      %s\n", v.CreatedAt.Local().Format("2006-01-02 15:04"))
		return nil
	},
}

var vmDeleteCmd = &cobra.Command{
	Use:   "delete <name>",
	Short: "Delete a VM",
	Long: `Delete a VM and its data. This cannot be undone.

You are asked to type the VM's name to confirm, because the name is the only
safeguard: deleted names become available again. Use --yes in scripts.`,
	Example: `  cloud vm delete web
  cloud vm delete web --yes          # no prompt, for scripts`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		org, err := requireOrg()
		if err != nil {
			return err
		}
		if err := confirm(cmd, vmDeleteYes, "VM", args[0]); err != nil {
			return err
		}
		c, _, err := apiClient()
		if err != nil {
			return err
		}
		res, err := c.DeleteOrgsOrgVmsVmWithResponse(cmd.Context(), org, args[0])
		if err != nil {
			return reachErr(err)
		}
		if err := apiErr(res.StatusCode(), res.Body); err != nil {
			return err
		}
		if !flagQuiet {
			fmt.Fprintf(cmd.ErrOrStderr(), "Deleted %s\n", args[0])
		}
		return nil
	},
}

// ── power operations ────────────────────────────────────────────────────────

// vmPowerCmd builds start, stop and restart, which differ only in the call
// they make and the word they print.
func vmPowerCmd(verb, short, long, example string, call func(context.Context, *api.ClientWithResponses, string, string) (*api.Vm, int, []byte, error)) *cobra.Command {
	return &cobra.Command{
		Use:     verb + " <name>",
		Short:   short,
		Long:    long,
		Example: example,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			org, err := requireOrg()
			if err != nil {
				return err
			}
			c, _, err := apiClient()
			if err != nil {
				return err
			}
			vm, status, body, err := call(cmd.Context(), c, org, args[0])
			if err != nil {
				return reachErr(err)
			}
			if err := apiErr(status, body); err != nil {
				return err
			}
			vm, err = decoded(vm)
			if err != nil {
				return err
			}
			return printVM(cmd, c, org, vm, strings.ToUpper(verb[:1])+verb[1:]+"ing")
		},
	}
}

var vmStartCmd = vmPowerCmd("start", "Start a stopped VM",
	`Start a VM that was stopped. The VM retains its configuration and data.`,
	`  cloud vm start web
  cloud vm start web --wait`,
	func(ctx context.Context, c *api.ClientWithResponses, org, name string) (*api.Vm, int, []byte, error) {
		res, err := c.PostOrgsOrgVmsVmStartWithResponse(ctx, org, name)
		if err != nil {
			return nil, 0, nil, err
		}
		return res.JSON202, res.StatusCode(), res.Body, nil
	})

var vmStopCmd = vmPowerCmd("stop", "Stop a running VM",
	`Stop a VM. The VM retains its configuration and data; start it again to
bring it back.`,
	`  cloud vm stop web
  cloud vm stop web --wait`,
	func(ctx context.Context, c *api.ClientWithResponses, org, name string) (*api.Vm, int, []byte, error) {
		res, err := c.PostOrgsOrgVmsVmStopWithResponse(ctx, org, name)
		if err != nil {
			return nil, 0, nil, err
		}
		return res.JSON202, res.StatusCode(), res.Body, nil
	})

var vmRestartCmd = vmPowerCmd("restart", "Restart a VM",
	`Restart a VM. The VM is stopped and started again; expect a short outage.`,
	`  cloud vm restart web
  cloud vm restart web --wait`,
	func(ctx context.Context, c *api.ClientWithResponses, org, name string) (*api.Vm, int, []byte, error) {
		res, err := c.PostOrgsOrgVmsVmRestartWithResponse(ctx, org, name)
		if err != nil {
			return nil, 0, nil, err
		}
		return res.JSON202, res.StatusCode(), res.Body, nil
	})

// ── helpers ─────────────────────────────────────────────────────────────────

// readSSHKey accepts either a file path (reads the public key from it) or a
// literal public key string (detected by the ssh- prefix).
func readSSHKey(pathOrKey string) (string, error) {
	if strings.HasPrefix(pathOrKey, "ssh-") {
		return pathOrKey, nil
	}
	data, err := os.ReadFile(pathOrKey)
	if err != nil {
		return "", fmt.Errorf("reading SSH key: %w", err)
	}
	key := strings.TrimSpace(string(data))
	if key == "" {
		return "", &UsageError{fmt.Errorf("SSH key file %s is empty", pathOrKey)}
	}
	if !strings.HasPrefix(key, "ssh-") {
		return "", &UsageError{fmt.Errorf("%s does not contain an SSH public key", pathOrKey)}
	}
	return key, nil
}

func init() {
	f := vmCreateCmd.Flags()
	f.StringVar(&vmCreateImage, "image", "", "VM image reference (required)")
	f.StringVar(&vmCreateSize, "size", "", "pricing tier (vCPU/RAM; default: smallest)")
	f.StringVar(&vmCreateRegion, "region", "", "region name (default from context / CLOUD_REGION)")
	f.StringVar(&vmCreateDesc, "description", "", "short description")
	f.StringVar(&vmCreateSSHKey, "ssh-key", "", "SSH public key or path to a public key file")
	_ = vmCreateCmd.MarkFlagRequired("image")

	vmDeleteCmd.Flags().BoolVarP(&vmDeleteYes, "yes", "y", false, "skip the confirmation (scripts)")

	vmSSHKeyRemoveCmd.Flags().BoolVarP(&vmSSHKeyRemoveYes, "yes", "y", false, "skip the confirmation (scripts)")

	vmIPDetachCmd.Flags().BoolVarP(&vmIPDetachYes, "yes", "y", false, "skip the confirmation (scripts)")

	for _, c := range []*cobra.Command{vmCreateCmd, vmStartCmd, vmStopCmd, vmRestartCmd} {
		c.Flags().BoolVar(&flagWait, "wait", false, "wait until the VM reaches a terminal status")
		c.Flags().DurationVar(&flagTimeout, "timeout", 10*time.Minute, "how long --wait waits")
	}

	vmSSHKeyCmd.AddCommand(vmSSHKeyListCmd, vmSSHKeyAddCmd, vmSSHKeyRemoveCmd)
	vmIPCmd.AddCommand(vmIPGetCmd, vmIPAttachCmd, vmIPDetachCmd)
	vmCmd.AddCommand(vmListCmd, vmCreateCmd, vmGetCmd, vmDeleteCmd,
		vmStartCmd, vmStopCmd, vmRestartCmd,
		vmConsoleCmd, vmSSHKeyCmd, vmIPCmd)
	rootCmd.AddCommand(vmCmd)
}
