// Package migrate applies the Goose migrations shipped with Space.
package migrate

import (
	"context"
	"database/sql"
	"errors"
	"os"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

func Provider(db *sql.DB, directory string, options ...goose.ProviderOption) (*goose.Provider, error) {
	return goose.NewProvider(goose.DialectPostgres, db, os.DirFS(directory), options...)
}
func Run(ctx context.Context, dsn, command, directory string) error {
	if command != "up" && command != "down" && command != "reset" {
		return errors.New("migration command must be up, down or reset")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return err
	}
	provider, err := Provider(db, directory, goose.WithSessionLocker(locker))
	if err != nil {
		return err
	}
	switch command {
	case "up":
		_, err = provider.Up(ctx)
	case "down":
		_, err = provider.Down(ctx)
	case "reset":
		_, err = provider.DownTo(ctx, 0)
	}
	return err
}

// Ready checks every migration shipped with this binary, including version gaps.
func Ready(ctx context.Context, provider *goose.Provider) error {
	pending, err := provider.HasPending(ctx)
	if err != nil {
		return err
	}
	if pending {
		return errors.New("database migrations are pending")
	}
	return nil
}
