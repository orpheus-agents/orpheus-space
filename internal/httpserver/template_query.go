package httpserver

import (
	"strings"
	"unicode/utf8"

	"github.com/orpheus-agents/orpheus-space/internal/api"
	"github.com/orpheus-agents/orpheus-space/internal/schedule"
)

func validateTemplateQuery(p api.GetTemplatesParams) error {
	if p.Name != nil && p.NameContains != nil {
		return schedule.InvalidAt("query", "name_contains")
	}
	for field, value := range map[string]*string{"name": p.Name, "name_contains": p.NameContains} {
		if value != nil && (strings.TrimSpace(*value) == "" || strings.ContainsRune(*value, 0) || !utf8.ValidString(*value)) {
			return schedule.InvalidAt("query", field)
		}
	}
	return nil
}
