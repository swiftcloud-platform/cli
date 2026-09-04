package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"cloud/internal/api"
	"cloud/internal/output"
)

// ── table shapes ────────────────────────────────────────────────────────────

type appRows []api.App

func (r appRows) Columns() []string { return []string{"NAME", "STATUS", "IMAGE", "REPLICAS", "URL"} }
func (r appRows) Rows() [][]string {
	out := make([][]string, len(r))
	for i, a := range r {
		out[i] = []string{a.Name, a.Status, a.Image, fmt.Sprintf("%d–%d", a.ReplicasMin, a.ReplicasMax), a.Url}
	}
	return out
}
func (r appRows) IDs() []string {
	out := make([]string, len(r))
	for i, a := range r {
		out[i] = a.Name
	}
	return out
}

type deploymentRows []api.Deployment

func (r deploymentRows) Columns() []string {
	return []string{"CREATED", "STATUS", "IMAGE", "REVISION", "REASON"}
}
func (r deploymentRows) Rows() [][]string {
	out := make([][]string, len(r))
	for i, d := range r {
		out[i] = []string{d.CreatedAt.Local().Format("2006-01-02 15:04"), d.Status, d.Image, d.Revision, d.FailureReason}
	}
	return out
}
func (r deploymentRows) IDs() []string {
	out := make([]string, len(r))
	for i, d := range r {
		out[i] = d.Id
	}
	return out
}

type domainRows []api.CustomDomain

func (r domainRows) Columns() []string { return []string{"DOMAIN", "ROUTING", "TLS", "NOTE"} }
func (r domainRows) Rows() [][]string {
	out := make([][]string, len(r))
	for i, d := range r {
		// TLS is shown exactly as the platform reports it. "pending" here is a
		// true statement; nothing on this page may claim a certificate exists.
		out[i] = []string{d.Domain, d.Status, d.Tls, d.Message}
	}
	return out
}
func (r domainRows) IDs() []string {
	out := make([]string, len(r))
	for i, d := range r {
		out[i] = d.Domain
	}
	return out
}

// ── commands ────────────────────────────────────────────────────────────────

var appCmd = &cobra.Command{Use: "app", Short: "Container apps"}

var appListCmd = &cobra.Command{
	Use:   "list",
	Short: "List apps in the organisation",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		org, err := requireOrg()
		if err != nil {
			return err
		}
		c, _, err := apiClient()
		if err != nil {
			return err
		}
		res, err := c.GetOrgsOrgAppsWithResponse(cmd.Context(), org)
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
		return printer.Print(appRows(list.Items))
	},
}

var (
	createImage, createRegion, createDesc, createSize string
	createPort, createMin, createMax                  int
	createEnv                                         []string
	createRegServer, createRegUser                    string
	createRegPassStdin                                bool
	flagWait                                          bool
	flagTimeout                                       time.Duration
)

var appCreateCmd = &cobra.Command{
	Use:   "create <name> --image <ref>",
	Short: "Create an app from a container image",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		org, err := requireOrg()
		if err != nil {
			return err
		}
		region := createRegion
		if region == "" {
			region = cfg.Region
		}
		if region == "" {
			return &UsageError{errors.New("no region given — pass --region, set CLOUD_REGION, or add one to your context (see `cloud region list`)")}
		}
		env, err := parseEnv(createEnv)
		if err != nil {
			return err
		}
		body := api.PostOrgsOrgAppsJSONRequestBody{
			Name: args[0], Image: createImage, Region: region,
			ContainerPort: &createPort, ReplicasMin: &createMin, ReplicasMax: &createMax,
		}
		if createDesc != "" {
			body.Description = &createDesc
		}
		if createSize != "" {
			body.Size = &createSize
		}
		if len(env) > 0 {
			body.EnvVars = &env
		}
		if createRegServer != "" || createRegUser != "" || createRegPassStdin {
			if createRegServer == "" || createRegUser == "" || !createRegPassStdin {
				return &UsageError{errors.New("private registry needs --registry-server, --registry-username and --registry-password-stdin together")}
			}
			pw, err := stdinToken(cmd.InOrStdin())
			if err != nil {
				return err
			}
			body.RegistryAuth = &struct {
				Password string `json:"password"`
				Server   string `json:"server"`
				Username string `json:"username"`
			}{Password: pw, Server: createRegServer, Username: createRegUser}
		}

		c, _, err := apiClient()
		if err != nil {
			return err
		}
		res, err := c.PostOrgsOrgAppsWithResponse(cmd.Context(), org, body)
		if err != nil {
			return reachErr(err)
		}
		if err := apiErr(res.StatusCode(), res.Body); err != nil {
			return err
		}
		app, err := decoded(res.JSON201)
		if err != nil {
			return err
		}
		if !flagQuiet {
			fmt.Fprintf(cmd.ErrOrStderr(), "Created %s (%s). Status: %s\n", app.Name, app.Id, app.Status)
		}
		if flagWait {
			app, err = waitForApp(cmd, c, org, app.Name, flagTimeout)
			if err != nil {
				return err
			}
		}
		if printer.Format == output.Table && !flagQuiet {
			if app.Url != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "%s\n", app.Url)
			}
			return nil
		}
		return printer.Print(appRows{*app})
	},
}

var appGetCmd = &cobra.Command{
	Use:   "get <name>",
	Short: "Show an app",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		org, err := requireOrg()
		if err != nil {
			return err
		}
		c, _, err := apiClient()
		if err != nil {
			return err
		}
		res, err := c.GetOrgsOrgAppsAppWithResponse(cmd.Context(), org, args[0])
		if err != nil {
			return reachErr(err)
		}
		if err := apiErr(res.StatusCode(), res.Body); err != nil {
			return err
		}
		a, err := decoded(res.JSON200)
		if err != nil {
			return err
		}
		if printer.Format != output.Table {
			return printer.Print(a)
		}
		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "Name         %s\nStatus       %s\nURL          %s\nImage        %s\nRegion       %s\nPort         %d\nReplicas     %d–%d\nSize         %s\n",
			a.Name, a.Status, orDash(a.Url), a.Image, orDash(a.Region), a.ContainerPort, a.ReplicasMin, a.ReplicasMax, orDash(a.Size))
		if a.Description != "" {
			fmt.Fprintf(out, "Description  %s\n", a.Description)
		}
		if a.RegistryAuth.Server != "" {
			fmt.Fprintf(out, "Registry     %s as %s\n", a.RegistryAuth.Server, a.RegistryAuth.Username)
		}
		if len(a.EnvVars) > 0 {
			fmt.Fprintf(out, "Env          %d variable(s); `--output json` to see them\n", len(a.EnvVars))
		}
		fmt.Fprintf(out, "Created      %s\n", a.CreatedAt.Local().Format("2006-01-02 15:04"))
		return nil
	},
}

var deployImage string

var appDeployCmd = &cobra.Command{
	Use:   "deploy <name> --image <ref>",
	Short: "Roll out a new image",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		org, err := requireOrg()
		if err != nil {
			return err
		}
		c, _, err := apiClient()
		if err != nil {
			return err
		}
		res, err := c.PostOrgsOrgAppsAppDeployWithResponse(cmd.Context(), org, args[0], api.PostOrgsOrgAppsAppDeployJSONRequestBody{Image: deployImage})
		if err != nil {
			return reachErr(err)
		}
		if err := apiErr(res.StatusCode(), res.Body); err != nil {
			return err
		}
		app, err := decoded(res.JSON202)
		if err != nil {
			return err
		}
		if !flagQuiet {
			fmt.Fprintf(cmd.ErrOrStderr(), "Deploying %s → %s\n", app.Name, deployImage)
		}
		if flagWait {
			if app, err = waitForApp(cmd, c, org, app.Name, flagTimeout); err != nil {
				return err
			}
		}
		if printer.Format == output.Table {
			if !flagQuiet && app.Url != "" {
				fmt.Fprintln(cmd.OutOrStdout(), app.Url)
			}
			return nil
		}
		return printer.Print(appRows{*app})
	},
}

var scaleMin, scaleMax int

var appScaleCmd = &cobra.Command{
	Use:   "scale <name> --min N --max N",
	Short: "Change the replica range",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if !cmd.Flags().Changed("min") && !cmd.Flags().Changed("max") {
			return &UsageError{errors.New("pass --min and/or --max")}
		}
		org, err := requireOrg()
		if err != nil {
			return err
		}
		body := api.PatchOrgsOrgAppsAppJSONRequestBody{}
		if cmd.Flags().Changed("min") {
			body.ReplicasMin = &scaleMin
		}
		if cmd.Flags().Changed("max") {
			body.ReplicasMax = &scaleMax
		}
		c, _, err := apiClient()
		if err != nil {
			return err
		}
		res, err := c.PatchOrgsOrgAppsAppWithResponse(cmd.Context(), org, args[0], body)
		if err != nil {
			return reachErr(err)
		}
		if err := apiErr(res.StatusCode(), res.Body); err != nil {
			return err
		}
		a, err := decoded(res.JSON200)
		if err != nil {
			return err
		}
		if !flagQuiet {
			fmt.Fprintf(cmd.ErrOrStderr(), "%s now scales %d–%d\n", a.Name, a.ReplicasMin, a.ReplicasMax)
		}
		if printer.Format != output.Table {
			return printer.Print(appRows{*a})
		}
		return nil
	},
}

var logsFollow bool
var logsTail int

var appLogsCmd = &cobra.Command{
	Use:   "logs <name>",
	Short: "Print recent logs; -f to follow",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		org, err := requireOrg()
		if err != nil {
			return err
		}
		c, _, err := apiClient()
		if err != nil {
			return err
		}
		params := &api.GetOrgsOrgAppsAppLogsParams{Tail: &logsTail}
		if logsFollow {
			params.Follow = &logsFollow
		}
		// Raw response: the body is a stream we copy line by line, not JSON.
		res, err := c.GetOrgsOrgAppsAppLogs(cmd.Context(), org, args[0], params)
		if err != nil {
			return reachErr(err)
		}
		defer res.Body.Close()
		if res.StatusCode != 200 {
			body, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
			return apiErr(res.StatusCode, body)
		}
		sc := bufio.NewScanner(res.Body)
		sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
		out := cmd.OutOrStdout()
		for sc.Scan() {
			fmt.Fprintln(out, sc.Text())
		}
		if err := sc.Err(); err != nil && !errors.Is(err, io.EOF) && cmd.Context().Err() == nil {
			return err
		}
		return nil
	},
}

var deleteYes bool

var appDeleteCmd = &cobra.Command{
	Use:   "delete <name>",
	Short: "Delete an app and everything about it",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		org, err := requireOrg()
		if err != nil {
			return err
		}
		if err := confirm(cmd, deleteYes, "app", args[0]); err != nil {
			return err
		}
		c, _, err := apiClient()
		if err != nil {
			return err
		}
		res, err := c.DeleteOrgsOrgAppsAppWithResponse(cmd.Context(), org, args[0])
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

// ── domains ─────────────────────────────────────────────────────────────────

var appDomainCmd = &cobra.Command{Use: "domain", Short: "Custom domains on an app"}

var appDomainListCmd = &cobra.Command{
	Use:   "list <app>",
	Short: "List custom domains and their routing/TLS state",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		org, err := requireOrg()
		if err != nil {
			return err
		}
		c, _, err := apiClient()
		if err != nil {
			return err
		}
		res, err := c.GetOrgsOrgAppsAppDomainsWithResponse(cmd.Context(), org, args[0])
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
		return printer.Print(domainRows(list.Items))
	},
}

var appDomainAddCmd = &cobra.Command{
	Use:   "add <app> <hostname>",
	Short: "Attach a custom domain (prints the CNAME target if DNS is not yet pointing at us)",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		org, err := requireOrg()
		if err != nil {
			return err
		}
		c, _, err := apiClient()
		if err != nil {
			return err
		}
		res, err := c.PostOrgsOrgAppsAppDomainsWithResponse(cmd.Context(), org, args[0], api.PostOrgsOrgAppsAppDomainsJSONRequestBody{Domain: strings.ToLower(args[1])})
		if err != nil {
			return reachErr(err)
		}
		if err := apiErr(res.StatusCode(), res.Body); err != nil {
			return err
		}
		d, err := decoded(res.JSON201)
		if err != nil {
			return err
		}
		if !flagQuiet && printer.Format == output.Table {
			fmt.Fprintf(cmd.ErrOrStderr(), "Attached %s — routing: %s, TLS: %s\n", d.Domain, d.Status, d.Tls)
			if d.Message != "" {
				fmt.Fprintf(cmd.ErrOrStderr(), "%s\n", d.Message)
			}
			return nil
		}
		return printer.Print(domainRows{*d})
	},
}

var appDomainRemoveCmd = &cobra.Command{
	Use:   "remove <app> <hostname>",
	Short: "Detach a custom domain",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		org, err := requireOrg()
		if err != nil {
			return err
		}
		c, _, err := apiClient()
		if err != nil {
			return err
		}
		res, err := c.DeleteOrgsOrgAppsAppDomainsDomainWithResponse(cmd.Context(), org, args[0], strings.ToLower(args[1]))
		if err != nil {
			return reachErr(err)
		}
		if err := apiErr(res.StatusCode(), res.Body); err != nil {
			return err
		}
		if !flagQuiet {
			fmt.Fprintf(cmd.ErrOrStderr(), "Removed %s from %s\n", args[1], args[0])
		}
		return nil
	},
}

// ── helpers ─────────────────────────────────────────────────────────────────

func reachErr(err error) error { return fmt.Errorf("could not reach %s: %w", cfg.APIURL, err) }

// parseEnv turns --env KEY=VALUE flags into a map; a bare KEY is an error, not an empty value.
func parseEnv(pairs []string) (map[string]string, error) {
	out := map[string]string{}
	for _, p := range pairs {
		k, v, ok := strings.Cut(p, "=")
		if !ok || k == "" {
			return nil, &UsageError{fmt.Errorf("--env %q is not KEY=VALUE", p)}
		}
		out[k] = v
	}
	return out, nil
}

func init() {
	f := appCreateCmd.Flags()
	f.StringVar(&createImage, "image", "", "container image reference (required)")
	f.StringVar(&createRegion, "region", "", "region name (default from context / CLOUD_REGION)")
	f.StringVar(&createDesc, "description", "", "short description")
	f.StringVar(&createSize, "size", "", "pricing tier (see /pricing)")
	f.IntVar(&createPort, "port", 8080, "port the container listens on")
	f.IntVar(&createMin, "min", 0, "minimum replicas (0 = scale to zero)")
	f.IntVar(&createMax, "max", 3, "maximum replicas")
	f.StringArrayVar(&createEnv, "env", nil, "environment variable KEY=VALUE (repeatable)")
	f.StringVar(&createRegServer, "registry-server", "", "private registry host")
	f.StringVar(&createRegUser, "registry-username", "", "private registry user")
	f.BoolVar(&createRegPassStdin, "registry-password-stdin", false, "read the registry password from stdin")
	_ = appCreateCmd.MarkFlagRequired("image")

	appDeployCmd.Flags().StringVar(&deployImage, "image", "", "new image reference (required)")
	_ = appDeployCmd.MarkFlagRequired("image")

	appScaleCmd.Flags().IntVar(&scaleMin, "min", 0, "minimum replicas")
	appScaleCmd.Flags().IntVar(&scaleMax, "max", 0, "maximum replicas")

	appLogsCmd.Flags().BoolVarP(&logsFollow, "follow", "f", false, "keep streaming")
	appLogsCmd.Flags().IntVar(&logsTail, "tail", 200, "number of recent lines")

	appDeleteCmd.Flags().BoolVarP(&deleteYes, "yes", "y", false, "skip the confirmation (scripts)")

	for _, c := range []*cobra.Command{appCreateCmd, appDeployCmd} {
		c.Flags().BoolVar(&flagWait, "wait", false, "wait until the app reaches a terminal status")
		c.Flags().DurationVar(&flagTimeout, "timeout", 10*time.Minute, "how long --wait waits")
	}

	appDomainCmd.AddCommand(appDomainListCmd, appDomainAddCmd, appDomainRemoveCmd)
	appCmd.AddCommand(appListCmd, appCreateCmd, appGetCmd, appDeployCmd, appScaleCmd, appLogsCmd, appDeleteCmd, appDomainCmd)
	rootCmd.AddCommand(appCmd)
}
