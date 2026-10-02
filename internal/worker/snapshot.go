package worker

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"time"

	"github.com/google/uuid"
	"go.yaml.in/yaml/v3"

	"github.com/orpheus-agents/orpheus-space/internal/config"
	"github.com/orpheus-agents/orpheus-space/internal/schedule"
	"github.com/orpheus-agents/orpheus-space/internal/store"
	"github.com/orpheus-agents/orpheus-space/internal/store/db"
	coreapi "github.com/orpheus-agents/orpheus/client"
)

type messageFrontMatter struct {
	ScheduleID        uuid.UUID           `json:"schedule_id" yaml:"schedule_id"`
	OccurrenceID      uuid.UUID           `json:"occurrence_id" yaml:"occurrence_id"`
	ScheduledAt       time.Time           `json:"scheduled_at" yaml:"scheduled_at"`
	DispatchedAt      time.Time           `json:"-" yaml:"dispatched_at"`
	Timezone          string              `json:"timezone" yaml:"timezone"`
	LastSuccessfulRun *successfulRunTimes `json:"last_successful_run,omitzero" yaml:"last_successful_run,omitempty"`
}

// successfulRunTimes describes a previous completed run, not a data coverage guarantee.
type successfulRunTimes struct {
	ScheduledAt        time.Time  `json:"scheduled_at" yaml:"scheduled_at"`
	ExecutionStartedAt *time.Time `json:"execution_started_at,omitzero" yaml:"execution_started_at,omitempty"`
	FinishedAt         time.Time  `json:"finished_at" yaml:"finished_at"`
}

func successTimes(occ *db.ScheduleOccurrence, location *time.Location) successfulRunTimes {
	result := successfulRunTimes{ScheduledAt: occ.ScheduledAt.In(location), FinishedAt: occ.FinishedAt.In(location)}
	if occ.ExecutionStartedAt != nil {
		result.ExecutionStartedAt = new(occ.ExecutionStartedAt.In(location))
	}
	return result
}

func Builder(cfg config.Config) store.BuildSnapshot {
	return func(row db.Schedule, occ db.ScheduleOccurrence, lastSuccess *db.ScheduleOccurrence, now time.Time) (store.Snapshot, error) {
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
		configuration := coreapi.ConfigurationInput{Agent: coreapi.AgentInput{Profile: row.Profile, Model: row.Model, Instructions: cfg.Execution.Agent.Instructions}, Sandbox: coreapi.SandboxInput{Template: row.Template, EnvFrom: &env}, Limits: &coreapi.LimitsInput{RunTimeoutSeconds: new(cfg.Execution.Limits.RunTimeoutSeconds), MaxSessionTokens: cfg.Execution.Limits.MaxSessionTokens}}
		fingerprintInput, _ := json.Marshal(struct {
			Configuration coreapi.ConfigurationInput
			Mode          string
		}{configuration, row.SessionMode})
		hash := sha256.Sum256(fingerprintInput)
		fingerprint := hex.EncodeToString(hash[:])
		fields := messageFrontMatter{
			ScheduleID: row.ID, OccurrenceID: occ.ID,
			ScheduledAt: occ.ScheduledAt.In(location), DispatchedAt: now.In(location),
			Timezone: row.Timezone,
		}
		if lastSuccess != nil {
			fields.LastSuccessfulRun = new(successTimes(lastSuccess, location))
		}
		frontMatter, err := yaml.Marshal(fields)
		if err != nil {
			return store.Snapshot{}, err
		}
		text := "---\n" + string(frontMatter) + "---\n\n" + row.Prompt
		fields.ScheduledAt = occ.ScheduledAt.UTC()
		if lastSuccess != nil {
			fields.LastSuccessfulRun = new(successTimes(lastSuccess, time.UTC))
		}
		metadata, err := json.Marshal(fields)
		if err != nil {
			return store.Snapshot{}, err
		}
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
