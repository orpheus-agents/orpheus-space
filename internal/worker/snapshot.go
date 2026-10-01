package worker

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/orpheus-agents/orpheus-space/internal/config"
	"github.com/orpheus-agents/orpheus-space/internal/schedule"
	"github.com/orpheus-agents/orpheus-space/internal/store"
	"github.com/orpheus-agents/orpheus-space/internal/store/db"
	coreapi "github.com/orpheus-agents/orpheus/client"
)

func Builder(cfg config.Config) store.BuildSnapshot {
	return func(row db.Schedule, occ db.ScheduleOccurrence, now time.Time) (store.Snapshot, error) {
		location, err := time.LoadLocation(row.Timezone)
		if err != nil {
			return store.Snapshot{}, schedule.Invalid("timezone")
		}
		env := append(slices.Clone(cfg.Execution.Sandbox.EnvFrom), row.EnvFrom...)
		slices.Sort(env)
		env = slices.Compact(env)
		if _, err := schedule.EnvNames(env, cfg.AllowedEnv); err != nil {
			return store.Snapshot{}, err
		}
		configuration := coreapi.ConfigurationInput{Agent: coreapi.AgentInput{Profile: cfg.Execution.Agent.Profile, Model: row.Model, Instructions: cfg.Execution.Agent.Instructions}, Sandbox: coreapi.SandboxInput{Template: cfg.Execution.Sandbox.Template, EnvFrom: &env}, Limits: &coreapi.LimitsInput{RunTimeoutSeconds: new(cfg.Execution.Limits.RunTimeoutSeconds), MaxSessionTokens: cfg.Execution.Limits.MaxSessionTokens}}
		fingerprintInput, _ := json.Marshal(struct {
			Configuration coreapi.ConfigurationInput
			Mode          string
		}{configuration, row.SessionMode})
		hash := sha256.Sum256(fingerprintInput)
		fingerprint := hex.EncodeToString(hash[:])
		metadata, _ := json.Marshal(map[string]any{"schedule_id": row.ID, "occurrence_id": occ.ID, "scheduled_at": occ.ScheduledAt.UTC(), "timezone": row.Timezone})
		text := fmt.Sprintf("---\nschedule_id: %s\noccurrence_id: %s\nscheduled_at: %s\ndispatched_at: %s\ntimezone: %s\n---\n\n%s", row.ID, occ.ID, occ.ScheduledAt.In(location).Format(time.RFC3339), now.In(location).Format(time.RFC3339), row.Timezone, row.Prompt)
		messages := []coreapi.TextMessage{{Text: text, Metadata: new(json.RawMessage(metadata))}}
		snapshot := store.Snapshot{Path: "/api/v1/sessions", Fingerprint: fingerprint, Reusable: row.SessionMode == "reuse"}
		var body any = coreapi.CreateSession{AllowMultipleRuns: &snapshot.Reusable, Configuration: configuration, Namespace: new("schedule"), ExternalKey: new(row.ID.String()), Messages: messages}
		if snapshot.Reusable && row.ReusableSessionID != nil && row.ReusableFingerprint != nil && *row.ReusableFingerprint == fingerprint {
			snapshot.Path = "/api/v1/sessions/" + row.ReusableSessionID.String() + "/runs"
			body = coreapi.CreateRun{Messages: messages}
		}
		raw, err := json.Marshal(body)
		snapshot.Body = raw
		return snapshot, err
	}
}
