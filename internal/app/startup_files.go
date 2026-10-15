package app

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"runtime"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/complynx/loom/internal/config"
)

const sessionKeyBytes = 32

// ensureSessionKey creates the first key only for a database without stored sessions.
// Missing keys on an existing installation require recovery, never silent rotation.
func ensureSessionKey(ctx context.Context, pool *pgxpool.Pool, identity config.Identity, recovery bool) error {
	info, err := os.Lstat(identity.EncryptionKeyFile)
	if err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("session key must be a regular file")
		}
		key, keyErr := config.LoadEncryptionKey(identity.EncryptionKeyFile)
		clear(key)
		if keyErr != nil {
			return errors.New("existing session key is invalid; restore it instead of regenerating")
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return errors.New("session key unavailable")
	}
	if recovery {
		return errors.New("session key is missing; recovery mode does not generate keys")
	}
	var sessions bool
	if err = pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM auth_sessions)").Scan(&sessions); err != nil {
		return errors.New("cannot check session key bootstrap safety")
	}
	if sessions {
		return errors.New("session key is missing for stored sessions; restore the original key")
	}
	if err = createSessionKey(identity.EncryptionKeyFile); err != nil {
		return errors.New("cannot create initial session key")
	}
	return nil
}

func createSessionKey(path string) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	key := make([]byte, sessionKeyBytes)
	defer clear(key)
	if _, err := rand.Read(key); err != nil {
		return err
	}
	file, err := os.CreateTemp(directory, ".session-key-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(file.Name()) }()
	if _, err = file.Write(key); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Link(file.Name(), path); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		winner, readErr := config.LoadEncryptionKey(path)
		clear(winner)
		return readErr
	}
	if runtime.GOOS == "windows" {
		return nil
	}
	dir, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}

// PrepareData creates only the known data directories, before the unprivileged API starts.
// It never generates credentials, contacts PostgreSQL or scans/chowns artifact contents.
func PrepareData(root string, uid, gid int) error {
	if !filepath.IsAbs(root) || uid < 0 || gid < 0 {
		return errors.New("prepare-data requires an absolute root and nonnegative UID/GID")
	}
	for _, path := range []string{root, filepath.Join(root, "keys"), filepath.Join(root, "artifacts")} {
		if err := os.MkdirAll(path, 0700); err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("data directory must be a real directory")
		}
		if err = os.Chown(path, uid, gid); err != nil {
			return err
		}
		// #nosec G302 -- Directories need owner search permission; no group/other access.
		if err = os.Chmod(path, 0700); err != nil {
			return err
		}
	}
	return nil
}
