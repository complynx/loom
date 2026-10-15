package app

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/config"
)

func startupDatabase(t *testing.T) (*pgxpool.Pool, config.Config) {
	t.Helper()
	dsn := os.Getenv("CLX_TEST_DATABASE_DSN")
	if dsn == "" {
		t.Skip("set CLX_TEST_DATABASE_DSN for real PostgreSQL")
	}
	admin, err := pgxpool.New(t.Context(), dsn)
	require.NoError(t, err)
	schema := "startup_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.Exec(t.Context(), "CREATE SCHEMA "+schema)
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	})
	u, err := url.Parse(dsn)
	require.NoError(t, err)
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	pool, err := pgxpool.New(t.Context(), u.String())
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool, config.Config{Database: config.Database{DSN: u.String(), MigrateOnStart: true}}
}

func TestStartupMigrationsRepeatAndSerializeWithoutDowngrade(t *testing.T) {
	t.Parallel()
	pool, cfg := startupDatabase(t)
	var wg sync.WaitGroup
	errors := make(chan error, 2)
	for range 2 {
		wg.Go(func() { errors <- Migrate(t.Context(), cfg) })
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	var tables bool
	require.NoError(
		t,
		pool.QueryRow(t.Context(), "SELECT to_regclass('auth_sessions') IS NOT NULL AND to_regclass('river_job') IS NOT NULL").
			Scan(&tables),
	)
	require.True(t, tables)
	_, err := pool.Exec(t.Context(), "INSERT INTO goose_db_version(version_id,is_applied) VALUES(999999,true)")
	require.NoError(t, err)
	require.ErrorContains(t, Migrate(t.Context(), cfg), "newer than this application")
	var future bool
	require.NoError(
		t,
		pool.QueryRow(t.Context(), "SELECT EXISTS(SELECT 1 FROM goose_db_version WHERE version_id=999999 AND is_applied)").
			Scan(&future),
	)
	require.True(t, future)
}

func TestStartupKeyIsNeverReplacedForStoredSessionsOrRecovery(t *testing.T) {
	t.Parallel()
	pool, cfg := startupDatabase(t)
	require.NoError(t, Migrate(t.Context(), cfg))
	identity := config.Identity{EncryptionKeyFile: filepath.Join(t.TempDir(), "keys", "session")}
	require.ErrorContains(t, ensureSessionKey(t.Context(), pool, identity, true), "recovery mode")
	require.NoFileExists(t, identity.EncryptionKeyFile)
	require.NoError(t, ensureSessionKey(t.Context(), pool, identity, false))
	first, err := os.ReadFile(identity.EncryptionKeyFile)
	require.NoError(t, err)
	require.NoError(t, ensureSessionKey(t.Context(), pool, identity, false))
	second, err := os.ReadFile(identity.EncryptionKeyFile)
	require.NoError(t, err)
	require.Equal(t, first, second)
	_, err = pool.Exec(
		t.Context(),
		"INSERT INTO auth_sessions(id,issuer,subject,actor,org_id,envelope,expires_at) VALUES($1,'issuer','user','user','org','{}',now())",
		uuid.NewString(),
	)
	require.NoError(t, err)
	require.NoError(t, os.Remove(identity.EncryptionKeyFile))
	require.ErrorContains(t, ensureSessionKey(t.Context(), pool, identity, false), "stored sessions")
	require.NoFileExists(t, identity.EncryptionKeyFile)
}

func TestStartupMigrationLockWaitHonorsCancellation(t *testing.T) {
	t.Parallel()
	pool, cfg := startupDatabase(t)
	lock, err := pool.Acquire(t.Context())
	require.NoError(t, err)
	defer lock.Release()
	_, err = lock.Exec(t.Context(), "SELECT pg_advisory_lock(1280266061)")
	require.NoError(t, err)
	defer func() { _, _ = lock.Exec(t.Context(), "SELECT pg_advisory_unlock(1280266061)") }()
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	require.Error(t, Migrate(ctx, cfg))
	require.Less(t, time.Since(started), 2*time.Second)
	var absent bool
	require.NoError(t, pool.QueryRow(t.Context(), "SELECT to_regclass('goose_db_version') IS NULL").Scan(&absent))
	require.True(t, absent)
}
