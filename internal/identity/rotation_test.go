package identity_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/complynx/loom/internal/config"
	"github.com/complynx/loom/internal/identity"
)

func TestRotatedJWKSAndUnknownKeyFlood(t *testing.T) {
	t.Parallel()
	old, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.RWMutex
	current := old
	currentID := "old"
	var requests atomic.Int64
	var issuer string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).
				Encode(map[string]string{"issuer": issuer, "jwks_uri": issuer + "/keys", "introspection_endpoint": issuer + "/introspect"})
		case "/keys":
			requests.Add(1)
			mu.RLock()
			key, id := current, currentID
			mu.RUnlock()
			_ = json.NewEncoder(w).
				Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: id, Use: "sig"}}})
		case "/introspect":
			_ = json.NewEncoder(w).
				Encode(map[string]any{"active": true, "iss": issuer, "sub": "user", "aud": "harness", "exp": time.Now().Add(time.Hour).Unix()})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	issuer = srv.URL
	auth, err := identity.New(
		config.Identity{
			Issuer:              issuer,
			Audience:            "harness",
			ClientID:            "client",
			ClientSecret:        "secret",
			JWKSCacheTTLSeconds: 300,
		},
		srv.Client(),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	token := func(key *rsa.PrivateKey, kid string) string { return signedRotationToken(t, key, kid, issuer) }
	authenticate := func(token string) error {
		r := httptest.NewRequest(http.MethodGet, "http://harness/chat", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		_, authenticateErr := auth.Authenticate(context.Background(), r)
		return authenticateErr
	}
	if authenticateErr := authenticate(token(old, "old")); authenticateErr != nil {
		t.Fatal(authenticateErr)
	}
	mu.Lock()
	current = rotated
	currentID = "rotated"
	mu.Unlock()
	if authenticateErr := authenticate(token(rotated, "rotated")); authenticateErr != nil {
		t.Fatalf("rotated issuer key rejected: %v", authenticateErr)
	}
	if n := requests.Load(); n != 2 {
		t.Fatalf("rotation needed %d JWKS fetches, want 2 total", n)
	}
	unknown := token(old, "attacker-unknown")
	var wg sync.WaitGroup
	for range 100 {
		wg.Go(func() {
			if authenticateErr := authenticate(unknown); !errors.Is(authenticateErr, identity.ErrUnauthorized) {
				t.Errorf("unknown key accepted or wrong error: %v", authenticateErr)
			}
		})
	}
	wg.Wait()
	if n := requests.Load(); n != 2 {
		t.Fatalf("unknown-key flood caused extra JWKS requests: %d", n)
	}
}

func signedRotationToken(t *testing.T, key *rsa.PrivateKey, kid, issuer string) string {
	t.Helper()

	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: key, KeyID: kid}},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	s, err := jwt.Signed(signer).
		Claims(jwt.Claims{Issuer: issuer, Subject: "user", Audience: jwt.Audience{"harness"}, Expiry: jwt.NewNumericDate(time.Now().Add(time.Hour))}).
		Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return s
}
