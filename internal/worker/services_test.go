package worker

import (
	"bytes"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/orpheus-agents/orpheus-space/internal/config"
	"github.com/orpheus-agents/orpheus-space/internal/store/db"
	coreapi "github.com/orpheus-agents/orpheus/client"
)

func TestServicesSnapshotScopeAndFingerprint(t *testing.T) {
	row := db.Schedule{ID: uuid.New(), Profile: "default", Template: "sandbox", Timezone: "UTC", SessionMode: "reuse", Services: []string{"b", "a"}}
	occ := db.ScheduleOccurrence{ID: uuid.New(), ScheduledAt: time.Now()}
	build := Builder(config.Config{})
	first, err := build(row, occ, nil, occ.ScheduledAt)
	if err != nil {
		t.Fatal(err)
	}
	var request coreapi.CreateSession
	if err := json.Unmarshal(first.Body, &request); err != nil {
		t.Fatal(err)
	}
	sandbox := request.Configuration.Sandbox
	if sandbox.Services == nil || !slices.Equal(*sandbox.Services, []string{"a", "b"}) || sandbox.EnvFrom != nil || bytes.Contains(first.Body, []byte(`"env_from"`)) {
		t.Fatal(string(first.Body))
	}
	row.Services = []string{"a", "b"}
	reordered, err := build(row, occ, nil, occ.ScheduledAt)
	if err != nil || reordered.Fingerprint != first.Fingerprint {
		t.Fatal(reordered, err)
	}
	row.ReusableSessionID = new(uuid.New())
	row.ReusableFingerprint = &first.Fingerprint
	reused, err := build(row, occ, nil, occ.ScheduledAt)
	if err != nil || reused.Path == "/api/v1/sessions" || bytes.Contains(reused.Body, []byte(`"services"`)) || bytes.Contains(reused.Body, []byte(`"env_from"`)) {
		t.Fatal(reused, err)
	}
	row.Services = []string{"a"}
	changed, err := build(row, occ, nil, occ.ScheduledAt)
	if err != nil || changed.Fingerprint == first.Fingerprint || changed.Path != "/api/v1/sessions" {
		t.Fatal(changed, err)
	}
	row.Services = nil
	empty, err := build(row, occ, nil, occ.ScheduledAt)
	if err != nil || !bytes.Contains(empty.Body, []byte(`"services":[]`)) || empty.Fingerprint == changed.Fingerprint {
		t.Fatal(empty, err)
	}
}
