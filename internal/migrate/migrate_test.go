//go:build integration

package migrate_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/orpheus-agents/orpheus-space/internal/migrate"
	"github.com/orpheus-agents/orpheus-space/internal/testutil"
)

func TestUpDownUp(t *testing.T) {
	pool := testutil.Database(t)
	sqlDB := stdlib.OpenDB(*pool.Config().ConnConfig)
	defer func() { _ = sqlDB.Close() }()
	p, err := migrate.Provider(sqlDB, "../../migrations")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.DownTo(t.Context(), 0); err != nil {
		t.Fatal(err)
	}
	var tables int
	if err = pool.QueryRow(t.Context(), "SELECT count(*) FROM information_schema.tables WHERE table_schema=current_schema() AND table_name!='goose_db_version'").Scan(&tables); err != nil || tables != 0 {
		t.Fatal(tables, err)
	}
	for range 2 {
		if _, err = p.Up(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	for _, table := range []string{"schedules", "schedule_create_keys"} {
		var exists bool
		if err = pool.QueryRow(t.Context(), "SELECT to_regclass($1) IS NOT NULL", table).Scan(&exists); err != nil || !exists {
			t.Fatal(table, err)
		}
	}
}

func TestReadinessRequiresAllMigrations(t *testing.T) {
	pool := testutil.Database(t)
	db := stdlib.OpenDBFromPool(pool)
	defer func() { _ = db.Close() }()
	directory := t.TempDir()
	raw, err := os.ReadFile("../../migrations/00001_schedules.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(directory, "00001_schedules.sql"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(directory, "00002_next.sql"), []byte("-- +goose Up\nCREATE TABLE next_stage(id int);\n-- +goose Down\nDROP TABLE next_stage;\n"), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := migrate.Provider(db, directory)
	if err != nil {
		t.Fatal(err)
	}
	if err = migrate.Ready(t.Context(), p); err == nil {
		t.Fatal("ready with pending migration 2")
	}
	if _, err = p.Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = migrate.Ready(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Down(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = migrate.Ready(t.Context(), p); err == nil {
		t.Fatal("ready after rollback")
	}
	if _, err = p.DownTo(t.Context(), 0); err != nil {
		t.Fatal(err)
	}
	if err = migrate.Ready(t.Context(), p); err == nil {
		t.Fatal("ready without schema")
	}
	pool.Close()
	if err = migrate.Ready(t.Context(), p); err == nil {
		t.Fatal("ready without database")
	}
}
