package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"cloud/internal/spice"
)

var vmConsoleCmd = &cobra.Command{
	Use:   "console <name>",
	Short: "Open a SPICE console in the browser",
	Long: `Open a SPICE console for a VM in the default browser.

The command fetches a short-lived connection ticket from the platform,
starts a local server with an embedded SPICE client, and opens the browser.
Close the browser tab or press Ctrl-C to end the session.`,
	Example: `  cloud vm console web
  cloud vm console web                # opens browser automatically`,
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
		res, err := c.GetOrgsOrgVmsVmConsoleWithResponse(cmd.Context(), org, args[0])
		if err != nil {
			return reachErr(err)
		}
		if err := apiErr(res.StatusCode(), res.Body); err != nil {
			return err
		}
		ticket, err := decoded(res.JSON200)
		if err != nil {
			return err
		}
		if !flagQuiet {
			fmt.Fprintf(cmd.ErrOrStderr(), "Opening console for %s…\n", args[0])
		}
		if err := spice.Open(ticket.WsUrl, ticket.Ticket); err != nil {
			return fmt.Errorf("console: %w", err)
		}
		return nil
	},
}

func init() {
	_ = vmConsoleCmd // registered in vm.go init()
}
