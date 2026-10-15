package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"

	"github.com/complynx/loom/internal/config"
	"github.com/complynx/loom/internal/store"
	"github.com/complynx/loom/migrations"
)

const migrationTimeout = 5 * time.Minute

func migrateOnStartup(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	if !cfg.Database.MigrateOnStart || cfg.Execution.RecoveryMode {
		return nil
	}
	logger.InfoContext(ctx, "startup migrations started")
	if err := Migrate(ctx, cfg); err != nil {
		return err
	}
	logger.InfoContext(ctx, "startup migrations completed")
	return nil
}

// Migrate serializes the complete application/River upgrade before workers start.
func Migrate(ctx context.Context, cfg config.Config) error {
	ctx, cancel := context.WithTimeout(ctx, migrationTimeout)
	defer cancel()
	lock, err := pgx.Connect(ctx, cfg.Database.DSN)
	if err != nil {
		return errors.New("migration database unavailable")
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer stop()
		_ = lock.Close(cleanup)
	}()
	if _, err = lock.Exec(ctx, "SELECT pg_advisory_lock(1280266061)"); err != nil {
		return errors.New("migration lock unavailable")
	}
	if err = checkApplicationVersion(ctx, lock); err != nil {
		return err
	}
	return migrateLocked(ctx, cfg)
}

func migrateLocked(ctx context.Context, cfg config.Config) error {
	pool, err := pgxpool.New(ctx, cfg.Database.DSN)
	if err != nil {
		return errors.New("migration pool unavailable")
	}
	defer pool.Close()
	migrator, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err != nil {
		return err
	}
	// ExistingVersions rejects versions unknown to this River release.
	if _, err = migrator.ExistingVersions(ctx); err != nil {
		return fmt.Errorf("job schema compatibility: %w", err)
	}
	if err = store.Migrate(ctx, cfg.Database.DSN); err != nil {
		return fmt.Errorf("application migrations: %w", err)
	}
	_, err = migrator.Migrate(ctx, rivermigrate.DirectionUp, nil)
	if err != nil {
		return fmt.Errorf("job migrations: %w", err)
	}
	return nil
}

func checkApplicationVersion(ctx context.Context, connection *pgx.Conn) error {
	var present bool
	if err := connection.QueryRow(ctx, "SELECT to_regclass('goose_db_version') IS NOT NULL").
		Scan(&present); err != nil {
		return err
	}
	if !present {
		return nil
	}
	files, err := fs.ReadDir(migrations.Files, ".")
	if err != nil {
		return err
	}
	var latest int64
	for _, file := range files {
		if !strings.HasSuffix(file.Name(), ".sql") {
			continue
		}
		version, parseErr := strconv.ParseInt(strings.Split(file.Name(), "_")[0], 10, 64)
		if parseErr != nil {
			return parseErr
		}
		latest = max(latest, version)
	}
	var current int64
	if err = connection.QueryRow(ctx, "SELECT coalesce(max(version_id),0) FROM goose_db_version WHERE is_applied").
		Scan(&current); err != nil {
		return err
	}
	if current > latest {
		return errors.New("database schema is newer than this application; automatic downgrade refused")
	}
	return nil
}
