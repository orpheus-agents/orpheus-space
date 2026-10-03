//go:build integration

package migrate_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/orpheus-agents/orpheus-space/internal/access"
	"github.com/orpheus-agents/orpheus-space/internal/migrate"
	"github.com/orpheus-agents/orpheus-space/internal/schedule"
	"github.com/orpheus-agents/orpheus-space/internal/store"
	"github.com/orpheus-agents/orpheus-space/internal/testutil"
)

func TestSelectionMigrationAndLegacyReplay(t *testing.T) {
	pool := testutil.Database(t)
	sqlDB := stdlib.OpenDB(*pool.Config().ConnConfig)
	defer func() { _ = sqlDB.Close() }()
	p, err := migrate.Provider(sqlDB, "../../migrations")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.DownTo(t.Context(), 4); err != nil {
		t.Fatal(err)
	}
	// Include active, paused and deleted rows. No production defaults in SQL.
	for _, status := range []string{"active", "paused", "deleted"} {
		_, err = pool.Exec(t.Context(), `INSERT INTO schedules(id,name,prompt,cron,timezone,status,session_mode,created_at,updated_at,cron_started_at,next_run_at,deleted_at)
   VALUES ($1,'Report','Prompt','* * * * *','UTC',CASE WHEN $2='deleted' THEN 'paused' ELSE $2 END,'new',now(),now(),now(),CASE WHEN $2='active' THEN now() ELSE NULL END,CASE WHEN $2='deleted' THEN now() ELSE NULL END)`, uuid.New(), status)
		if err != nil {
			t.Fatal(err)
		}
	}
	// Literal pre-feature input fixes both field ordering and omission semantics.
	oldInput := `{"name":"Report","prompt":"Prompt","cron":"* * * * *","timezone":"UTC","status":"paused","model":null,"session_mode":"new","owner_email":null,"env_from":[]}`
	hash := sha256.Sum256([]byte(oldInput))
	key, id := uuid.New(), uuid.New()
	saved := `{"id":"` + id.String() + `","name":"Report","prompt":"Prompt","cron":"* * * * *","timezone":"UTC","status":"paused","model":null,"session_mode":"new","owner_email":null,"env_from":[],"created_at":"2026-10-01T00:00:00Z","updated_at":"2026-10-01T00:00:00Z","next_run_at":null,"deleted_at":null,"last_occurrence":null}`
	if _, err = pool.Exec(t.Context(), `INSERT INTO schedule_create_keys(key,fingerprint,response) VALUES ($1,$2,$3)`, key, hex.EncodeToString(hash[:]), saved); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "space.toml")
	t.Setenv("ORPHEUS_CONFIG_FILE", path)
	if _, err = p.Up(t.Context()); err == nil {
		t.Fatal("backfill succeeded without defaults")
	}
	var version int64
	if err = pool.QueryRow(t.Context(), `SELECT max(version_id) FROM goose_db_version`).Scan(&version); err != nil || version != 5 {
		t.Fatal(version, err)
	}
	// Unrelated settings/files are not loaded, nor does this migration contact Orpheus.
	if err = os.WriteFile(path, []byte("[execution.agent]\nprofile='old-profile'\ninstructions_file='/missing'\n[execution.sandbox]\ntemplate='old-template:v1'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = pool.QueryRow(t.Context(), `SELECT count(*) FROM schedules WHERE profile='old-profile' AND template='old-template:v1'`).Scan(&count); err != nil || count != 3 {
		t.Fatal(count, err)
	}
	// Readiness and a repeated Up need no configuration even with existing rows.
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err = migrate.Ready(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	if _, err = p.DownTo(t.Context(), 5); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	var input schedule.Input
	if err = json.Unmarshal([]byte(oldInput), &input); err != nil {
		t.Fatal(err)
	}
	s := &store.Store{Pool: pool, DefaultProfile: "changed", DefaultTemplate: "changed"}
	replay, err := s.Create(t.Context(), access.Principal{ManageAll: true}, input, &key)
	if err != nil || replay.ID != id || replay.Profile != "old-profile" || replay.Template != "old-template:v1" {
		t.Fatal(replay, err)
	}
	if _, err = p.DownTo(t.Context(), 4); err != nil {
		t.Fatal(err)
	}
	var columns int
	if err = pool.QueryRow(t.Context(), `SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='schedules' AND column_name IN ('profile','template')`).Scan(&columns); err != nil || columns != 0 {
		t.Fatal(columns, err)
	}
}
