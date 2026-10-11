package app

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"

	"github.com/complynx/clx-scripter/internal/config"
	"github.com/complynx/clx-scripter/internal/store"
)

// Migrate is a separate deployment step; the HTTP runtime does not run DDL.
func Migrate(ctx context.Context, cfg config.Config) error {
	if err := store.Migrate(ctx, cfg.Database.DSN); err != nil {
		return fmt.Errorf("application migrations: %w", err)
	}
	pool, err := pgxpool.New(ctx, cfg.Database.DSN)
	if err != nil {
		return err
	}
	defer pool.Close()
	migrator, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err != nil {
		return err
	}
	_, err = migrator.Migrate(ctx, rivermigrate.DirectionUp, nil)
	if err != nil {
		return fmt.Errorf("job migrations: %w", err)
	}
	return nil
}
