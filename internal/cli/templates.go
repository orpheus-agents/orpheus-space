package cli

import (
	"net/http"

	api "github.com/orpheus-agents/orpheus-space/client"
	"github.com/spf13/cobra"
)

func templateCommand(o *options) *cobra.Command {
	var active bool
	var name, contains, cursor string
	var limit int
	cmd := &cobra.Command{Use: "templates", Short: "Read one template page; use next_cursor or prev_cursor to continue", Args: cobra.NoArgs}
	cmd.Flags().BoolVar(&active, "active", false, "Filter active or inactive templates (default: both)")
	cmd.Flags().StringVar(&name, "name", "", "Exact stored name, including team slug and tag")
	cmd.Flags().StringVar(&contains, "name-contains", "", "Case-insensitive literal name substring")
	cmd.Flags().IntVar(&limit, "limit", 50, "Page size (1–200)")
	cmd.Flags().StringVar(&cursor, "cursor", "", "Page cursor from a previous response with the same filters")
	cmd.MarkFlagsMutuallyExclusive("name", "name-contains")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		p := api.GetTemplatesParams{Limit: &limit}
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
		return o.call(func(c *api.Client) (*http.Response, error) { return c.GetTemplates(cmd.Context(), &p) })
	}
	return cmd
}
