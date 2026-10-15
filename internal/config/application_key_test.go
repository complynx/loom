package config_test

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/config"
)

func TestStartupMigrationDefaultAndOptOut(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("../../examples/config.yaml")
	require.NoError(t, err)
	lookup := func(string) (string, bool) { return "configured", true }
	defaultYAML := strings.Replace(
		string(data),
		"  migrate_on_start: true # Apply schema before the API and queue workers start.",
		"",
		1,
	)
	cfg, err := config.Parse([]byte(defaultYAML), lookup)
	require.NoError(t, err)
	require.True(t, cfg.Database.MigrateOnStart)
	disabled := strings.Replace(string(data), "migrate_on_start: true", "migrate_on_start: false", 1)
	cfg, err = config.Parse([]byte(disabled), lookup)
	require.NoError(t, err)
	require.False(t, cfg.Database.MigrateOnStart)
}

func TestApplicationKeyCredentialsFromSecretOrFile(t *testing.T) {
	t.Parallel()
	private, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	data, err := json.Marshal(map[string]string{
		"type":     "application",
		"appId":    "application-1",
		"keyId":    "key-1",
		"clientId": "client-1",
		"key": string(
			pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(private)}),
		),
	})
	require.NoError(t, err)
	encoded := base64.StdEncoding.EncodeToString(data)
	identity, err := (config.Identity{ClientKeyBase64: encoded}).ResolveCredentials()
	require.NoError(t, err)
	require.Equal(t, "client-1", identity.ClientID)
	require.Equal(t, "key-1", identity.ClientKeyID)
	require.Empty(t, identity.ClientKeyBase64)
	require.True(t, private.Equal(identity.ClientPrivateKey))
	for _, whitespace := range []string{"\n", "\r\n", " ", "\t", " \r\n\t \n", "\u00a0"} {
		var wrapped strings.Builder
		wrapped.WriteString(whitespace)
		for offset := 0; offset < len(encoded); offset += 76 {
			wrapped.WriteString(encoded[offset:min(offset+76, len(encoded))])
			wrapped.WriteString(whitespace)
		}
		wrapped.WriteString(whitespace)
		resolved, resolveErr := (config.Identity{ClientKeyBase64: wrapped.String()}).ResolveCredentials()
		require.NoError(t, resolveErr)
		require.True(t, private.Equal(resolved.ClientPrivateKey))
	}
	path := filepath.Join(t.TempDir(), "application.json")
	require.NoError(t, os.WriteFile(path, data, 0600))
	fromFile, err := (config.Identity{ClientKeyFile: path, ClientID: "client-1"}).ResolveCredentials()
	require.NoError(t, err)
	require.True(t, fromFile.ClientPrivateKey.Equal(identity.ClientPrivateKey))
	for _, invalid := range []config.Identity{
		{ClientKeyBase64: encoded, ClientID: "other"},
		{ClientKeyBase64: encoded, ClientSecret: "conflicting-secret"},
		{ClientKeyBase64: encoded, ClientKeyFile: path},
		{ClientKeyBase64: "not-base64"},
		{ClientKeyBase64: " \r\n\t "},
		{ClientKeyBase64: encoded[:76] + " ! " + encoded[76:]},
		{ClientKeyBase64: base64.StdEncoding.EncodeToString([]byte(`{"type":"serviceaccount"}`))},
	} {
		_, resolveErr := invalid.ResolveCredentials()
		require.Error(t, resolveErr)
		require.NotContains(t, resolveErr.Error(), encoded)
		require.NotContains(t, resolveErr.Error(), "PRIVATE KEY")
	}
}
