package core

import (
	"bytes"
	"encoding/json"

	"github.com/google/uuid"
	coreapi "github.com/orpheus-agents/orpheus/client"
)

// Required availability fields must be present: missing flags are not inactive
// profiles. Reject an incompatible upstream response instead of hiding choices.
func decodeProfilePage(raw []byte) (coreapi.Profiles, error) {
	invalid := &Failure{Code: "core_invalid_response"}
	var fields map[string]json.RawMessage
	var out coreapi.Profiles
	if json.Unmarshal(raw, &fields) != nil || json.Unmarshal(raw, &out) != nil || out.Items == nil {
		return coreapi.Profiles{}, invalid
	}
	for _, key := range []string{"items", "next_cursor", "prev_cursor"} {
		if _, present := fields[key]; !present {
			return coreapi.Profiles{}, invalid
		}
	}
	var items []map[string]json.RawMessage
	if json.Unmarshal(fields["items"], &items) != nil {
		return coreapi.Profiles{}, invalid
	}
	for i, item := range items {
		for _, key := range []string{"id", "name", "description", "harness", "account_id", "account_name", "account_active", "active", "available", "model", "codex", "instructions"} {
			value, present := item[key]
			if !present || key != "description" && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				return coreapi.Profiles{}, invalid
			}
		}
		p := out.Items[i]
		if p.ID == uuid.Nil || p.AccountID == uuid.Nil || p.Name == "" || p.AccountName == "" || p.Model == "" || p.Harness != coreapi.ProfileHarnessCodex || p.Available != (p.Active && p.AccountActive) {
			return coreapi.Profiles{}, invalid
		}
	}
	return out, nil
}
