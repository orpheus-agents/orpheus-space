//go:build integration

package migrate_test

import (
	"bytes"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/orpheus-agents/orpheus-space/internal/migrate"
	"github.com/orpheus-agents/orpheus-space/internal/testutil"
)

func TestServicesMigrationPreservesSchedulesAndFrozenRequests(t *testing.T) {
	pool := testutil.Database(t)
	sqlDB := stdlib.OpenDBFromPool(pool)
	defer func() { _ = sqlDB.Close() }()
	p, err := migrate.Provider(sqlDB, "../../migrations")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.DownTo(t.Context(), 7); err != nil {
		t.Fatal(err)
	}
	for i, status := range []string{"active", "paused", "deleted"} {
		id := uuid.New()
		_, err = pool.Exec(t.Context(), `INSERT INTO schedules
   (id,name,prompt,cron,timezone,status,session_mode,owner_email,env_from,profile,template,created_at,updated_at,cron_started_at,next_run_at,deleted_at,reusable_session_id,reusable_fingerprint)
   VALUES ($1,'Report','Prompt','0 12 * * *','Europe/Moscow',CASE WHEN $2='deleted' THEN 'paused' ELSE $2 END,'reuse','alice@example.com',ARRAY['OLD_API_KEY'],'profile','template',now(),now(),now(),CASE WHEN $2='active' THEN now()+interval '2 days' ELSE NULL END,CASE WHEN $2='deleted' THEN now() ELSE NULL END,$3,'old-fingerprint')`, id, status, uuid.New())
		if err != nil {
			t.Fatal(err)
		}
		state := []string{"dispatching", "accepted", "failed"}[i]
		// Deliberately retain whitespace: a replay must use the exact old bytes.
		body := []byte("{ \"configuration\": {\"sandbox\": {\"env_from\": [\"OLD_API_KEY\"]}} }")
		_, err = pool.Exec(t.Context(), `INSERT INTO schedule_occurrences
   (id,schedule_id,scheduled_at,state,created_at,updated_at,completed_at,request_path,request_body,request_key,fingerprint,reusable,uncertain,attempts,next_attempt_at,session_id,run_id)
   VALUES ($1,$2,now()-interval '1 day',$3,now(),now(),CASE WHEN $3='failed' THEN now() ELSE NULL END,'/api/v1/sessions',$4,$5,'frozen-fingerprint',true,$3='dispatching',2,now(),$6,$7)`, uuid.New(), id, state, body, uuid.New(), uuid.New(), uuid.New())
		if err != nil {
			t.Fatal(err)
		}
		_, err = pool.Exec(t.Context(), `INSERT INTO schedule_create_keys(key,fingerprint,response) VALUES ($1,'legacy-input-fingerprint',jsonb_build_object('id',$2::text,'env_from',jsonb_build_array('OLD_API_KEY'),'status',$3::text))`, uuid.New(), id.String(), status)
		if err != nil {
			t.Fatal(err)
		}
	}
	snapshot := func(query string) []byte {
		t.Helper()
		var raw []byte
		if err := pool.QueryRow(t.Context(), query).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		return raw
	}
	const tasks = `SELECT jsonb_agg(to_jsonb(s)-'env_from'-'services'-'reusable_session_id'-'reusable_fingerprint' ORDER BY id) FROM schedules s`
	const occurrences = `SELECT jsonb_agg(to_jsonb(o) ORDER BY id) FROM schedule_occurrences o`
	const keys = `SELECT jsonb_agg(jsonb_build_object('key',key,'fingerprint',fingerprint,'response',response-'env_from'-'services') ORDER BY key) FROM schedule_create_keys`
	beforeTasks, beforeOccurrences, beforeKeys := snapshot(tasks), snapshot(occurrences), snapshot(keys)
	verify := func(column, removed string) {
		t.Helper()
		for _, pair := range []struct {
			query  string
			before []byte
		}{{tasks, beforeTasks}, {occurrences, beforeOccurrences}, {keys, beforeKeys}} {
			if !bytes.Equal(snapshot(pair.query), pair.before) {
				t.Fatalf("migration changed preserved data: %s", pair.query)
			}
		}
		var count int
		if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM schedules WHERE reusable_session_id IS NOT NULL OR reusable_fingerprint IS NOT NULL`).Scan(&count); err != nil || count != 0 {
			t.Fatal(count, err)
		}
		if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='schedules' AND column_name=$1`, removed).Scan(&count); err != nil || count != 0 {
			t.Fatal(count, err)
		}
		if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM schedules s WHERE to_jsonb(s)->$1 = '[]'::jsonb`, column).Scan(&count); err != nil || count != 3 {
			t.Fatal(count, err)
		}
		if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM schedule_create_keys WHERE response->$1='[]'::jsonb AND NOT response ? $2`, column, removed).Scan(&count); err != nil || count != 3 {
			t.Fatal(count, err)
		}
	}
	for range 2 {
		if _, err = p.Up(t.Context()); err != nil {
			t.Fatal(err)
		}
		verify("services", "env_from")
		if _, err = p.DownTo(t.Context(), 7); err != nil {
			t.Fatal(err)
		}
		verify("env_from", "services")
	}
	if _, err = p.Up(t.Context()); err != nil {
		t.Fatal(err)
	}
}
