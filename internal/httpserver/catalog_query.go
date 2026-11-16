package httpserver

import (
	"strings"
	"unicode/utf8"

	"github.com/orpheus-agents/orpheus-space/internal/schedule"
)

func validateCatalogNames(name, contains *string) error {
	if name != nil && contains != nil {
		return schedule.InvalidAt("query", "name_contains")
	}
	for field, value := range map[string]*string{"name": name, "name_contains": contains} {
		if value != nil && (strings.TrimSpace(*value) == "" || strings.ContainsRune(*value, 0) || !utf8.ValidString(*value)) {
			return schedule.InvalidAt("query", field)
		}
	}
	return nil
}
