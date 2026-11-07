package cmd

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"cloud/internal/config"
	"cloud/internal/termsafe"
)

var vmConsoleCmd = &cobra.Command{
	Use:   "console <name>",
	Short: "Open the VM console in the browser",
	Long: `Open the VM's serial console in the default browser.

The console is a page of the dashboard. You need to be signed in to the
dashboard in your browser — if you are not, the login page appears first.
The URL is always printed, so it can be opened by hand when no browser can
be started (over SSH, say).`,
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
		// "." or ".." survive path escaping and would move the browser to
		// another dashboard page.
		if err := config.ValidateSegment("VM id", vm.Id); err != nil {
			return fmt.Errorf("unexpected response from %s: %w", cfg.APIURL, err)
		}

		dashboardURL := consoleURL(cfg.APIURL, vm.Id)
		// stdout carries the URL, so `cloud vm console web -q` is scriptable.
		fmt.Fprintln(cmd.OutOrStdout(), dashboardURL)
		if !flagQuiet {
			fmt.Fprintf(cmd.ErrOrStderr(), "Opening console for %s…\n", termsafe.String(args[0]))
		}
		if err := openBrowser(dashboardURL); err != nil {
			return fmt.Errorf("could not open a browser (%w); open the URL above yourself", err)
		}
		return nil
	},
}

// consoleURL derives the dashboard page from the API URL:
// https://cloud.co.zm/api/v1 → https://cloud.co.zm/dash/vms/<id>/console.
// The id comes from the platform, so it is escaped: a "/" or "?" in it must
// not steer the browser to another page.
func consoleURL(apiURL, id string) string {
	base := strings.TrimSuffix(strings.TrimRight(apiURL, "/"), "/api/v1")
	return base + "/dash/vms/" + url.PathEscape(id) + "/console"
}

func init() {
	_ = vmConsoleCmd // registered in vm.go init()
}
