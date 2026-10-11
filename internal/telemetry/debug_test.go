package telemetry_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/clx-scripter/internal/telemetry"

	"github.com/complynx/clx-scripter/internal/domain"
)

func TestDebugRequestsKeepClaimsButRemoveCredentials(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	observer := telemetry.New(slog.New(slog.NewJSONHandler(&out, &slog.HandlerOptions{Level: slog.LevelDebug})))
	observer.EnablePayloads(true)
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"user-42"}`))
	jwt := header + "." + payload + ".sensitiveSignature"
	request := httptest.NewRequest(http.MethodPost, "/v1/runs", nil)
	request.Header.Set("Authorization", "Bearer "+jwt)
	request.Header.Set("X-Subject-Token", "opaque-user-secret")
	observer.DebugRequest(
		context.Background(),
		request,
		domain.Principal{Subject: "user-42", Actor: "service", OrgID: "org"},
	)
	observer.DebugPayload(
		context.Background(),
		"http_request",
		map[string]any{
			"text":         "Help select an order",
			"access_token": jwt,
			"password":     "private-password",
			"api_key":      "private-api-key",
			"arguments":    `{"refresh_token":"opaque-secret"}`,
			"image_url":    "data:image/png;base64,private-bytes",
		},
	)
	logs := out.String()
	require.Contains(t, logs, "Help select an order")
	require.Contains(t, logs, "user-42")
	require.Contains(t, logs, header+"."+payload+".***")
	for _, secret := range []string{"sensitiveSignature", "opaque-user-secret", "private-password", "private-api-key", "opaque-secret", "private-bytes"} {
		require.NotContains(t, logs, secret)
	}
}
func TestInfoDoesNotEmitDiagnosticPayloads(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	observer := telemetry.New(slog.New(slog.NewJSONHandler(&out, nil)))
	observer.EnablePayloads(true)
	observer.DebugPayload(context.Background(), "http_request", map[string]string{"text": "private-prompt"})
	require.Empty(t, out.String())
}
