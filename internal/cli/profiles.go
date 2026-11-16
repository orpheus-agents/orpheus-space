package cli

import (
	"net/http"

	api "github.com/orpheus-agents/orpheus-space/client"
	"github.com/spf13/cobra"
)

func profileCommand(o *options) *cobra.Command {
	var active, available bool
	var name, contains, cursor string
	var limit int
	cmd := &cobra.Command{Use: "profiles", Short: "Read one profile page; use next_cursor or prev_cursor to continue", Args: cobra.NoArgs}
	cmd.Flags().BoolVar(&active, "active", false, "Filter active or inactive profiles (default: both)")
	cmd.Flags().BoolVar(&available, "available", false, "Filter profiles whose profile and account are both active")
	cmd.Flags().StringVar(&name, "name", "", "Exact profile name")
	cmd.Flags().StringVar(&contains, "name-contains", "", "Case-insensitive literal name substring")
	cmd.Flags().IntVar(&limit, "limit", 50, "Page size (1–200)")
	cmd.Flags().StringVar(&cursor, "cursor", "", "Page cursor from a previous response with the same filters")
	cmd.MarkFlagsMutuallyExclusive("name", "name-contains")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		p := api.GetProfilesParams{Limit: &limit}
		if cmd.Flags().Changed("available") {
			p.Available = &available
		}
		if cmd.Flags().Changed("active") {
			p.Active = &active
		}
		if cmd.Flags().Changed("name") {
			p.Name = &name
		}
		if cmd.Flags().Changed("name-contains") {
			p.NameContains = &contains
		}
		if cmd.Flags().Changed("cursor") {
			p.Cursor = &cursor
		}
		return o.call(func(c *api.Client) (*http.Response, error) { return c.GetProfiles(cmd.Context(), &p) })
	}
	return cmd
}
