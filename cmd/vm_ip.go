package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"cloud/internal/output"
)

// ── commands ────────────────────────────────────────────────────────────────

var vmIPCmd = &cobra.Command{
	Use:   "ip",
	Short: "Public IP addresses on a VM",
	Long: `Manage public IP addresses for a VM. A public IP is allocated from
the platform's pool when attached and released when detached.`,
	Example: `  cloud vm ip get web
  cloud vm ip attach web
  cloud vm ip detach web`,
}

var vmIPGetCmd = &cobra.Command{
	Use:   "get <vm>",
	Short: "Show IP addresses of a VM",
	Example: `  cloud vm ip get web
  cloud vm ip get web -o json`,
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
		res, err := c.GetOrgsOrgVmsVmIpWithResponse(cmd.Context(), org, args[0])
		if err != nil {
			return reachErr(err)
		}
		if err := apiErr(res.StatusCode(), res.Body); err != nil {
			return err
		}
		ip, err := decoded(res.JSON200)
		if err != nil {
			return err
		}
		if printer.Format != output.Table {
			return printer.Print(ip)
		}
		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "Private IP   %s\n", ip.PrivateIp)
		fmt.Fprintf(out, "Public IP    %s\n", orDash(ip.PublicIp))
		return nil
	},
}

var vmIPAttachCmd = &cobra.Command{
	Use:   "attach <vm>",
	Short: "Allocate and attach a public IP to a VM",
	Long: `Allocate a public IP address from the platform's pool and attach it
to a VM. The IP is assigned by the platform; you cannot choose a specific
address.`,
	Example: `  cloud vm ip attach web`,
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
		res, err := c.PostOrgsOrgVmsVmIpAttachWithResponse(cmd.Context(), org, args[0])
		if err != nil {
			return reachErr(err)
		}
		if err := apiErr(res.StatusCode(), res.Body); err != nil {
			return err
		}
		if !flagQuiet && printer.Format == output.Table {
			fmt.Fprintf(cmd.ErrOrStderr(), "Attaching public IP to %s…\n", args[0])
			return nil
		}
		return printer.Print(struct {
			Status string `json:"status"`
		}{Status: "attaching"})
	},
}

var vmIPDetachCmd = &cobra.Command{
	Use:   "detach <vm>",
	Short: "Release the public IP from a VM",
	Long: `Release the public IP address from a VM. The IP is returned to the
platform's pool and may be assigned to another resource.`,
	Example: `  cloud vm ip detach web
  cloud vm ip detach web --yes`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		org, err := requireOrg()
		if err != nil {
			return err
		}
		if err := confirm(cmd, vmIPDetachYes, "public IP on VM", args[0]); err != nil {
			return err
		}
		c, _, err := apiClient()
		if err != nil {
			return err
		}
		res, err := c.PostOrgsOrgVmsVmIpDetachWithResponse(cmd.Context(), org, args[0])
		if err != nil {
			return reachErr(err)
		}
		if err := apiErr(res.StatusCode(), res.Body); err != nil {
			return err
		}
		if !flagQuiet {
			fmt.Fprintf(cmd.ErrOrStderr(), "Detached public IP from %s\n", args[0])
		}
		return nil
	},
}

func init() {
	_ = vmIPCmd // registered in vm.go init()
}
