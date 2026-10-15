package app

import (
	"context"
	"errors"
	"maps"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/complynx/loom/internal/config"
	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/identity"
	"github.com/complynx/loom/internal/store"
)

func loadSessionCipher(cfg config.Identity) (*identity.SessionCipher, error) {
	files := map[string]string{cfg.EncryptionKeyID: cfg.EncryptionKeyFile}
	maps.Copy(files, cfg.PreviousEncryptionKeys)
	keys := make(map[string][]byte, len(files))
	defer func() {
		for _, key := range keys {
			clear(key)
		}
	}()
	for id, path := range files {
		key, err := config.LoadEncryptionKey(path)
		if err != nil {
			return nil, errors.New("session encryption key unavailable")
		}
		keys[id] = key
	}
	return identity.NewSessionKeyring(cfg.EncryptionKeyID, keys, cfg.Audience, cfg.ClientID)
}

// RotateSessionKeys commits one bounded batch. Repeating the command resumes from
// the remaining envelopes. It never dispatches runs or contacts external services.
func RotateSessionKeys(ctx context.Context, cfg config.Config, limit int) (int, error) {
	if limit < 1 || limit > 1000 {
		return 0, errors.New("rotation batch must be within 1..1000")
	}
	cipher, err := loadSessionCipher(cfg.Identity)
	if err != nil {
		return 0, err
	}
	pc, err := pgxpool.ParseConfig(cfg.Database.DSN)
	if err != nil {
		return 0, errors.New("invalid database configuration")
	}
	pc.MaxConns = 1
	pc.ConnConfig.RuntimeParams["statement_timeout"] = strconv.Itoa(cfg.Database.StatementTimeoutMS)
	pc.ConnConfig.RuntimeParams["lock_timeout"] = strconv.Itoa(cfg.Database.LockTimeoutMS)
	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return 0, errors.New("database unavailable")
	}
	defer pool.Close()
	repository := store.New(pool, nil)
	if err = repository.CheckSessionKeys(ctx, cipher.HasKey); err != nil {
		return 0, err
	}
	return repository.RotateSessionKeys(
		ctx,
		cipher.ActiveKeyID(),
		limit,
		func(session domain.AuthSession) ([]byte, error) {
			return cipher.Rewrap(
				domain.Principal{
					Issuer:    session.Issuer,
					Subject:   session.Subject,
					Actor:     session.Actor,
					OrgID:     session.OrgID,
					ExpiresAt: session.ExpiresAt,
				},
				session.Envelope,
			)
		},
	)
}
