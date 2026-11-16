//go:build integration

package migrate_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/orpheus-agents/orpheus-space/internal/migrate"
	"github.com/orpheus-agents/orpheus-space/internal/testutil"
)

func TestManualOccurrenceMigrationPreservesHistory(t *testing.T) {
	pool := testutil.Database(t)
	sqlDB := stdlib.OpenDBFromPool(pool)
	defer func() { _ = sqlDB.Close() }()
	provider, err := migrate.Provider(sqlDB, "../../migrations")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = provider.DownTo(t.Context(), 10); err != nil {
		t.Fatal(err)
	}
	sid, oid := uuid.New(), uuid.New()
	if _, err = pool.Exec(t.Context(), `INSERT INTO schedules(id,name,prompt,cron,timezone,status,session_mode,profile,template,created_at,updated_at,cron_started_at)
VALUES ($1,'Task','Prompt','* * * * *','UTC','paused','reuse','profile','template',now(),now(),now())`, sid); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(t.Context(), `INSERT INTO schedule_occurrences(id,schedule_id,scheduled_at,state,created_at,updated_at,request_key,request_path,request_body,fingerprint,reusable)
VALUES ($1,$2,'2026-11-16T10:00:00Z','dispatching',now(),now(),$1,'/api/v1/sessions',$3,'fingerprint',true)`, oid, sid, []byte(`{ "saved": "bytes" }`)); err != nil {
		t.Fatal(err)
	}
	snapshot := func() []byte {
		t.Helper()
		var raw []byte
		if err := pool.QueryRow(t.Context(), `SELECT to_jsonb(o)-'trigger'-'manual_key' FROM schedule_occurrences o WHERE id=$1`, oid).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		return raw
	}
	before := snapshot()
	for range 2 {
		if _, err = provider.Up(t.Context()); err != nil {
			t.Fatal(err)
		}
		var trigger string
		var key *uuid.UUID
		if err = pool.QueryRow(t.Context(), `SELECT trigger,manual_key FROM schedule_occurrences WHERE id=$1`, oid).Scan(&trigger, &key); err != nil || trigger != "scheduled" || key != nil {
			t.Fatal(trigger, key, err)
		}
		if !bytes.Equal(before, snapshot()) {
			t.Fatal("migration rewrote existing history or request")
		}
		if _, err = provider.DownTo(t.Context(), 10); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(before, snapshot()) {
			t.Fatal("down migration rewrote existing history")
		}
	}
	if _, err = provider.Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Preserve the one-active constraint independently of trigger and timestamp uniqueness.
	insert := `INSERT INTO schedule_occurrences(id,schedule_id,scheduled_at,state,created_at,updated_at,request_key,trigger,manual_key)
VALUES ($1,$2,'2026-11-16T10:00:00Z','pending',now(),now(),$1,'manual',$3)`
	if _, err = pool.Exec(t.Context(), insert, uuid.New(), sid, uuid.New()); err == nil {
		t.Fatal("two unfinished occurrences accepted")
	}
	if _, err = pool.Exec(t.Context(), `UPDATE schedule_occurrences SET state='failed',completed_at=now() WHERE id=$1`, oid); err != nil {
		t.Fatal(err)
	}
	manualID, manualKey := uuid.New(), uuid.New()
	if _, err = pool.Exec(t.Context(), insert, manualID, sid, manualKey); err != nil {
		t.Fatal("manual time must coexist with cron time", err)
	}
	if _, err = pool.Exec(t.Context(), `UPDATE schedule_occurrences SET state='failed',completed_at=now() WHERE id=$1`, manualID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(t.Context(), insert, uuid.New(), sid, manualKey); err == nil {
		t.Fatal("duplicate manual key accepted")
	}
	if _, err = pool.Exec(t.Context(), insert, uuid.New(), sid, nil); err == nil {
		t.Fatal("manual occurrence without a key accepted")
	}
	if _, err = pool.Exec(t.Context(), `UPDATE schedule_occurrences SET manual_key=$2 WHERE id=$1`, oid, uuid.New()); err == nil {
		t.Fatal("scheduled occurrence with a manual key accepted")
	}
	if _, err = provider.DownTo(t.Context(), 10); err == nil || !strings.Contains(err.Error(), "manual history exists") {
		t.Fatal("unsafe rollback accepted", err)
	}
	var count int
	if err = pool.QueryRow(t.Context(), `SELECT count(*) FROM schedule_occurrences WHERE trigger='manual'`).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
}
