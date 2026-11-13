package core

import (
	"encoding/json"

	"github.com/google/uuid"
	coreapi "github.com/orpheus-agents/orpheus/client"
)

// Generated structs do not distinguish absent fields from zero values. Check
// presence before interpreting a missing active flag as an inactive template.
func decodeTemplatePage(raw []byte) (coreapi.Templates, error) {
	invalid := &Failure{Code: "core_invalid_response"}
	var fields map[string]json.RawMessage
	var out coreapi.Templates
	if json.Unmarshal(raw, &fields) != nil || json.Unmarshal(raw, &out) != nil || out.Items == nil {
		return coreapi.Templates{}, invalid
	}
	for _, key := range []string{"items", "next_cursor", "prev_cursor"} {
		if _, present := fields[key]; !present {
			return coreapi.Templates{}, invalid
		}
	}
	var items []map[string]json.RawMessage
	if json.Unmarshal(fields["items"], &items) != nil {
		return coreapi.Templates{}, invalid
	}
	for i, item := range items {
		for _, key := range []string{"id", "name", "template_name", "tag", "description", "active"} {
			if _, present := item[key]; !present {
				return coreapi.Templates{}, invalid
			}
		}
		var active *bool
		if json.Unmarshal(item["active"], &active) != nil || active == nil || out.Items[i].ID == uuid.Nil || out.Items[i].Name == "" || out.Items[i].TemplateName == "" {
			return coreapi.Templates{}, invalid
		}
	}
	return out, nil
}
