package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

var vmConsoleCmd = &cobra.Command{
	Use:   "console <name>",
	Short: "Open the VM console in the browser",
	Long: `Open the VM's serial console in the default browser.

The console uses the platform's dashboard (xterm.js over SSE). You need to
be signed in to the dashboard in your browser — if you are not, the login
page appears first.`,
	Example: `  cloud vm console web`,
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
		res, err := c.GetOrgsOrgVmsVmWithResponse(cmd.Context(), org, args[0])
		if err != nil {
			return reachErr(err)
		}
		if err := apiErr(res.StatusCode(), res.Body); err != nil {
			return err
		}
		vm, err := decoded(res.JSON200)
		if err != nil {
			return err
		}

		// Derive the dashboard base URL from the API URL.
		// API: https://cloud.co.zm/api/v1 → Dashboard: https://cloud.co.zm
		baseURL := strings.TrimSuffix(cfg.APIURL, "/api/v1")
		baseURL = strings.TrimSuffix(baseURL, "/api/v1") // idempotent
		dashboardURL := baseURL + "/dash/vms/" + vm.Id + "/console"

		if !flagQuiet {
			fmt.Fprintf(cmd.ErrOrStderr(), "Opening console for %s…\n", args[0])
		}
		openBrowser(dashboardURL)
		return nil
	},
}

func init() {
	_ = vmConsoleCmd // registered in vm.go init()
}
