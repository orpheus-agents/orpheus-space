package worker

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

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
			snapshot, err := Builder(config.Config{})(db.Schedule{Timezone: tc.zone}, db.ScheduleOccurrence{ScheduledAt: scheduled}, dispatched)
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
	if _, err := Builder(config.Config{})(db.Schedule{Timezone: "Invalid/Zone"}, db.ScheduleOccurrence{}, time.Now()); err == nil {
		t.Fatal("invalid timezone accepted")
	}
}
