package migrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/orpheus-agents/orpheus-space/internal/config"
	"github.com/pelletier/go-toml/v2"
	"github.com/pressly/goose/v3"
)

// Selection data is installation-specific. Load only the two defaults, only
// when Up finds rows to backfill; readiness and empty databases need no TOML.
func selectionMigration() *goose.Migration {
	return goose.NewGoMigration(6, &goose.GoFunc{RunTx: func(ctx context.Context, tx *sql.Tx) error {
		var needed bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM schedules WHERE profile IS NULL OR template IS NULL)
   OR EXISTS (SELECT 1 FROM schedule_create_keys WHERE response IS NOT NULL AND (response->>'profile' IS NULL OR response->>'template' IS NULL))`).Scan(&needed); err != nil {
			return err
		}
		if !needed {
			return nil
		}
		profile, template, err := selectionDefaults(config.FilePath())
		if err != nil {
			// Only configuration diagnostics are safe to log; never SQL error text.
			slog.Error("Cannot load schedule selection defaults", "reason", err.Error())
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE schedules SET profile=COALESCE(profile,$1),template=COALESCE(template,$2) WHERE profile IS NULL OR template IS NULL`, profile, template); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE schedule_create_keys SET response=response || jsonb_build_object(
   'profile', COALESCE(response->>'profile',$1::text), 'template', COALESCE(response->>'template',$2::text))
   WHERE response IS NOT NULL AND (response->>'profile' IS NULL OR response->>'template' IS NULL)`, profile, template)
		return err
	}}, nil)
}

func selectionDefaults(path string) (string, string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", "", fmt.Errorf("read schedule selection config: %w", err)
	}
	var cfg struct {
		Execution struct {
			Agent struct {
				Profile string `toml:"profile"`
			} `toml:"agent"`
			Sandbox struct {
				Template string `toml:"template"`
			} `toml:"sandbox"`
		} `toml:"execution"`
	}
	if err := toml.Unmarshal(raw, &cfg); err != nil {
		// Decoder messages may contain literal configuration values.
		if decoded, ok := errors.AsType[*toml.DecodeError](err); ok {
			row, column := decoded.Position()
			return "", "", fmt.Errorf("invalid TOML in schedule selection config %q at line %d, column %d", path, row, column)
		}
		return "", "", fmt.Errorf("invalid TOML in schedule selection config %q (%T)", path, err)
	}
	profile, template := cfg.Execution.Agent.Profile, cfg.Execution.Sandbox.Template
	if strings.TrimSpace(profile) == "" || strings.TrimSpace(template) == "" {
		return "", "", fmt.Errorf("schedule selection config %q requires execution.agent.profile and execution.sandbox.template", path)
	}
	return profile, template, nil
}
