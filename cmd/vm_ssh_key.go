package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"cloud/internal/api"
	"cloud/internal/output"
)

// ── commands ────────────────────────────────────────────────────────────────

var vmSSHKeyCmd = &cobra.Command{
	Use:   "ssh-key",
	Short: "SSH keys on a VM",
	Long: `Manage the SSH public keys recorded on a VM.

Keys are installed only on a machine's first boot: those given to "cloud vm
create --ssh-key", or recorded before the machine first started. A key added
to a machine that is already set up is recorded but NOT installed — add it to
~/.ssh/authorized_keys on the machine yourself. Removing a key likewise only
removes it from the record, not from the machine.`,
	Example: `  cloud vm ssh-key list web
  cloud vm ssh-key add web ~/.ssh/id_ed25519.pub --label laptop
  cloud vm ssh-key remove web key_abc123`,
}

var vmSSHKeyListCmd = &cobra.Command{
	Use:   "list <vm>",
	Short: "List SSH keys on a VM",
	Example: `  cloud vm ssh-key list web
  cloud vm ssh-key list web -o json`,
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
		res, err := c.GetOrgsOrgVmsVmSshKeysWithResponse(cmd.Context(), org, args[0], nil)
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
		return printer.Print(vmSSHKeyRows(list.Items))
	},
}

var vmSSHKeyAddCmd = &cobra.Command{
	Use:   "add <vm> <path-or-key>",
	Short: "Record an SSH key on a VM",
	Long: `Record an SSH public key on a VM. The argument is either a path to a
public key file (e.g. ~/.ssh/id_ed25519.pub) or a literal public key string
starting with ssh-, quoted so it is one argument.

Keys are installed only on a machine's first boot. On a machine that is
already set up the key is recorded but NOT installed: add it to
~/.ssh/authorized_keys on the machine yourself. The platform's note about
what it did with the key is printed after it is added.`,
	Example: `  cloud vm ssh-key add web ~/.ssh/id_ed25519.pub
  cloud vm ssh-key add web ~/.ssh/id_ed25519.pub --label laptop
  cloud vm ssh-key add web "ssh-ed25519 AAAA... me@laptop"`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		org, err := requireOrg()
		if err != nil {
			return err
		}
		key, err := readSSHKey(args[1])
		if err != nil {
			return err
		}
		body := api.VmSshKeyCreate{PublicKey: key}
		if vmSSHKeyLabel != "" {
			body.Label = &vmSSHKeyLabel
		}
		c, _, err := apiClient()
		if err != nil {
			return err
		}
		res, err := c.PostOrgsOrgVmsVmSshKeysWithResponse(cmd.Context(), org, args[0], body)
		if err != nil {
			return reachErr(err)
		}
		if err := apiErr(res.StatusCode(), res.Body); err != nil {
			return err
		}
		k, err := decoded(res.JSON201)
		if err != nil {
			return err
		}
		if !flagQuiet && printer.Format == output.Table {
			fmt.Fprintf(cmd.ErrOrStderr(), "Recorded key %s (%s)\n", k.Id, k.Fingerprint)
			if k.Notice != nil && *k.Notice != "" {
				fmt.Fprintln(cmd.ErrOrStderr(), *k.Notice)
			}
			return nil
		}
		return printer.Print(vmSSHKeyRows{*k})
	},
}

var vmSSHKeyRemoveCmd = &cobra.Command{
	Use:   "remove <vm> <key-id>",
	Short: "Remove an SSH key from a VM",
	Long: `Remove an SSH public key from the VM's record. A key already
installed on the machine stays in ~/.ssh/authorized_keys there until you
remove it on the machine.`,
	Example: `  cloud vm ssh-key remove web key_abc123
  cloud vm ssh-key remove web key_abc123 --yes`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		org, err := requireOrg()
		if err != nil {
			return err
		}
		if err := confirm(cmd, vmSSHKeyRemoveYes, "SSH key", args[1]); err != nil {
			return err
		}
		c, _, err := apiClient()
		if err != nil {
			return err
		}
		res, err := c.DeleteOrgsOrgVmsVmSshKeysKeyIdWithResponse(cmd.Context(), org, args[0], args[1])
		if err != nil {
			return reachErr(err)
		}
		if err := apiErr(res.StatusCode(), res.Body); err != nil {
			return err
		}
		if !flagQuiet {
			fmt.Fprintf(cmd.ErrOrStderr(), "Removed key %s from %s\n", args[1], args[0])
		}
		return nil
	},
}

func init() {
	vmSSHKeyAddCmd.Flags().StringVar(&vmSSHKeyLabel, "label", "", "human-readable label for this key")
	_ = vmSSHKeyCmd // registered in vm.go init()
}
