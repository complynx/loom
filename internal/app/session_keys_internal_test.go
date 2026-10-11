package app

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/complynx/clx-scripter/internal/config"
	"github.com/complynx/clx-scripter/internal/domain"
)

func TestDeploymentSessionKeysPreserveRollingReads(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "old")
	newPath := filepath.Join(dir, "new")
	oldKey := make([]byte, 32)
	newKey := make([]byte, 32)
	newKey[0] = 1
	require.NoError(t, os.WriteFile(oldPath, []byte(base64.StdEncoding.EncodeToString(oldKey)), 0600))
	require.NoError(t, os.WriteFile(newPath, newKey, 0600))
	cfg := config.Identity{EncryptionKeyID: "old", EncryptionKeyFile: oldPath, Audience: "aud", ClientID: "client"}
	old, err := loadSessionCipher(cfg)
	require.NoError(t, err)
	p := domain.Principal{Issuer: "issuer", Subject: "user", Actor: "user", ExpiresAt: time.Now().Add(time.Hour)}
	raw, err := old.Seal(p, "credential")
	require.NoError(t, err)
	cfg.EncryptionKeyID = "new"
	cfg.EncryptionKeyFile = newPath
	cfg.PreviousEncryptionKeys = map[string]string{"old": oldPath}
	rotated, err := loadSessionCipher(cfg)
	require.NoError(t, err)
	value, err := rotated.Open(p, raw)
	require.NoError(t, err)
	require.Equal(t, "credential", value)
	cfg.PreviousEncryptionKeys["old"] = filepath.Join(dir, "missing")
	_, err = loadSessionCipher(cfg)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "missing")
}
