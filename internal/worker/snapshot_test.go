package worker

import (
	"encoding/json"
	"maps"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/google/uuid"
	"github.com/orpheus-agents/orpheus-space/internal/config"
	"github.com/orpheus-agents/orpheus-space/internal/store/db"
	coreapi "github.com/orpheus-agents/orpheus/client"
)

func TestSnapshotLocalTimes(t *testing.T) {
	for _, tc := range []struct{ zone, scheduled, dispatched, wantScheduled, wantDispatched string }{
		{"Europe/Moscow", "2026-10-04T21:30:00Z", "2026-10-05T22:00:00Z", "2026-10-05T00:30:00+03:00", "2026-10-06T01:00:00+03:00"},
		{"Europe/Berlin", "2026-10-25T00:30:00Z", "2026-10-25T01:30:00Z", "2026-10-25T02:30:00+02:00", "2026-10-25T02:30:00+01:00"},
	} {
		t.Run(tc.zone, func(t *testing.T) {
			scheduled, _ := time.Parse(time.RFC3339, tc.scheduled)
			dispatched, _ := time.Parse(time.RFC3339, tc.dispatched)
			snapshot, err := Builder(config.Config{})(db.Schedule{Timezone: tc.zone}, db.ScheduleOccurrence{ScheduledAt: scheduled}, nil, dispatched)
			if err != nil {
				t.Fatal(err)
			}
			var request coreapi.CreateSession
			if err := json.Unmarshal(snapshot.Body, &request); err != nil {
				t.Fatal(err)
			}
			message := request.Messages[0]
			for _, want := range []string{"scheduled_at: " + tc.wantScheduled + "\n", "dispatched_at: " + tc.wantDispatched + "\n"} {
				if !strings.Contains(message.Text, want) {
					t.Fatalf("missing %q in %s", want, message.Text)
				}
			}
			var metadata struct {
				ScheduledAt string `json:"scheduled_at"`
			}
			if err := json.Unmarshal(*message.Metadata, &metadata); err != nil || metadata.ScheduledAt != tc.scheduled {
				t.Fatal(metadata, err)
			}
		})
	}
	if _, err := Builder(config.Config{})(db.Schedule{Timezone: "Invalid/Zone"}, db.ScheduleOccurrence{}, nil, time.Now()); err == nil {
		t.Fatal("invalid timezone accepted")
	}
}

func TestSnapshotLastSuccessfulRun(t *testing.T) {
	at := time.Date(2026, 10, 25, 0, 30, 0, 0, time.UTC)
	for _, tc := range []struct {
		name     string
		previous *db.ScheduleOccurrence
		want     string
	}{
		{name: "first run"},
		{name: "DST change", previous: &db.ScheduleOccurrence{ScheduledAt: at, ExecutionStartedAt: new(at.Add(time.Minute)), FinishedAt: new(at.Add(time.Hour))}, want: `{"scheduled_at":"2026-10-25T02:30:00+02:00","execution_started_at":"2026-10-25T02:31:00+02:00","finished_at":"2026-10-25T02:30:00+01:00"}`},
		{name: "unknown start", previous: &db.ScheduleOccurrence{ScheduledAt: at, FinishedAt: new(at.Add(time.Hour))}, want: `{"scheduled_at":"2026-10-25T02:30:00+02:00","finished_at":"2026-10-25T02:30:00+01:00"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snapshot, err := Builder(config.Config{})(db.Schedule{Timezone: "Europe/Berlin", Prompt: "Collect data"}, db.ScheduleOccurrence{ScheduledAt: at.Add(2 * time.Hour)}, tc.previous, at.Add(2*time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			var request coreapi.CreateSession
			if err := json.Unmarshal(snapshot.Body, &request); err != nil {
				t.Fatal(err)
			}
			message := request.Messages[0]
			var metadata map[string]json.RawMessage
			if err := json.Unmarshal(*message.Metadata, &metadata); err != nil {
				t.Fatal(err)
			}
			if tc.previous == nil {
				if strings.Contains(message.Text, "last_successful_run") || metadata["last_successful_run"] != nil {
					t.Fatal(message)
				}
				return
			}
			var expected map[string]string
			if err := json.Unmarshal([]byte(tc.want), &expected); err != nil {
				t.Fatal(err)
			}
			if got := readFrontMatter(t, message.Text).LastSuccessfulRun; !maps.Equal(got, expected) {
				t.Fatal(got)
			}
			if !strings.HasSuffix(message.Text, "---\n\nCollect data") {
				t.Fatal(message.Text)
			}
			var times map[string]string
			if err := json.Unmarshal(metadata["last_successful_run"], &times); err != nil {
				t.Fatal(err)
			}
			if times["scheduled_at"] != at.Format(time.RFC3339) || times["finished_at"] != at.Add(time.Hour).Format(time.RFC3339) {
				t.Fatal(times)
			}
			if tc.previous.ExecutionStartedAt == nil {
				if _, exists := times["execution_started_at"]; exists {
					t.Fatal(times)
				}
			} else if times["execution_started_at"] != at.Add(time.Minute).Format(time.RFC3339) {
				t.Fatal(times)
			}
		})
	}
}

// Read the actual YAML with independent field types to check the message contract.
func readFrontMatter(t *testing.T, text string) struct {
	LastSuccessfulRun map[string]string `yaml:"last_successful_run"`
} {
	t.Helper()
	var result struct {
		LastSuccessfulRun map[string]string `yaml:"last_successful_run"`
	}
	content, ok := strings.CutPrefix(text, "---\n")
	if !ok {
		t.Fatal("missing front matter opening", text)
	}
	content, _, ok = strings.Cut(content, "\n---\n\n")
	if !ok {
		t.Fatal("missing front matter closing", text)
	}
	if err := yaml.Unmarshal([]byte(content), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestStoredSelectionAndInstructionOverrides(t *testing.T) {
	cfg := config.Config{}
	cfg.Execution.Agent.Profile = "creation-default"
	cfg.Execution.Sandbox.Template = "creation-template"
	row := db.Schedule{ID: uuid.New(), Timezone: "UTC", Profile: "selected", Template: "selected:v1", SessionMode: "reuse"}
	occ := db.ScheduleOccurrence{ID: uuid.New(), ScheduledAt: time.Now()}
	first, err := Builder(cfg)(row, occ, nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var request coreapi.CreateSession
	if err = json.Unmarshal(first.Body, &request); err != nil {
		t.Fatal(err)
	}
	if request.Configuration.Agent.Profile != row.Profile || request.Configuration.Sandbox.Template != row.Template || request.Configuration.Agent.Instructions != nil {
		t.Fatal(request.Configuration)
	}
	cfg.Execution.Agent.Profile, cfg.Execution.Sandbox.Template = "new-default", "new-template"
	row.ReusableSessionID, row.ReusableFingerprint = new(uuid.New()), &first.Fingerprint
	reuse, err := Builder(cfg)(row, occ, nil, time.Now())
	if err != nil || reuse.Fingerprint != first.Fingerprint || reuse.Path == "/api/v1/sessions" {
		t.Fatal(reuse, err)
	}
	for _, selection := range []string{"profile", "template"} {
		changed := row
		if selection == "profile" {
			changed.Profile = "other"
		} else {
			changed.Template = "other"
		}
		fresh, err := Builder(cfg)(changed, occ, nil, time.Now())
		if err != nil || fresh.Fingerprint == first.Fingerprint || fresh.Path != "/api/v1/sessions" {
			t.Fatal(fresh, err)
		}
	}
	row.ReusableSessionID = nil
	for _, text := range []string{"", "override"} {
		cfg.Execution.Agent.Instructions = &text
		fresh, err := Builder(cfg)(row, occ, nil, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(fresh.Body, &request); err != nil || request.Configuration.Agent.Instructions == nil || *request.Configuration.Agent.Instructions != text {
			t.Fatal(request, err)
		}
	}
}
