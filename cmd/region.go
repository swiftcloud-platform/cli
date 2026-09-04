package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"cloud/internal/api"
)

type regionRows []api.Region

func (r regionRows) Columns() []string { return []string{"NAME", "LOCATION", "STATUS"} }
func (r regionRows) Rows() [][]string {
	out := make([][]string, len(r))
	for i, x := range r {
		out[i] = []string{x.Name, deref(x.Location), x.Status}
	}
	return out
}
func (r regionRows) IDs() []string {
	out := make([]string, len(r))
	for i, x := range r {
		out[i] = x.Name
	}
	return out
}

var regionCmd = &cobra.Command{
	Use:   "region",
	Short: "Regions available for new resources",
	Long: `Regions are where new resources are created. A region only matters at
creation time, so pass --region to "cloud app create", or set a default with
CLOUD_REGION or a context.`,
	Example: `  cloud region list
  cloud app create demo --image nginx:1.27 --region zm-lusaka-central-1`,
}

var regionListCmd = &cobra.Command{
	Use:   "list",
	Short: "List regions",
	Example: `  cloud region list
  cloud region list -o json`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, _, err := apiClient()
		if err != nil {
			return err
		}
		res, err := c.GetRegionsWithResponse(cmd.Context())
		if err != nil {
			return fmt.Errorf("could not reach %s: %w", cfg.APIURL, err)
		}
		if err := apiErr(res.StatusCode(), res.Body); err != nil {
			return err
		}
		list, err := decoded(res.JSON200)
		if err != nil {
			return err
		}
		return printer.Print(regionRows(list.Items))
	},
}

func init() {
	regionCmd.AddCommand(regionListCmd)
	rootCmd.AddCommand(regionCmd)
}
