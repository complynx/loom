package identity

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"github.com/complynx/clx-scripter/internal/config"
	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRotatedJWKSAndUnknownKeyFlood(t *testing.T) {
	old, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	rotated, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
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
			_ = json.NewEncoder(w).Encode(map[string]string{"issuer": issuer, "jwks_uri": issuer + "/keys", "introspection_endpoint": issuer + "/introspect"})
		case "/keys":
			requests.Add(1)
			mu.RLock()
			key, id := current, currentID
			mu.RUnlock()
			_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: id, Use: "sig"}}})
		case "/introspect":
			_ = json.NewEncoder(w).Encode(map[string]any{"active": true, "iss": issuer, "sub": "user", "aud": "harness", "exp": time.Now().Add(time.Hour).Unix()})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	issuer = srv.URL
	auth, e := New(config.Identity{Issuer: issuer, Audience: "harness", ClientID: "client", ClientSecret: "secret", JWKSCacheTTLSeconds: 300}, srv.Client(), nil)
	if e != nil {
		t.Fatal(e)
	}
	token := func(key *rsa.PrivateKey, kid string) string {
		signer, e := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: key, KeyID: kid}}, nil)
		if e != nil {
			t.Fatal(e)
		}
		s, e := jwt.Signed(signer).Claims(jwt.Claims{Issuer: issuer, Subject: "user", Audience: jwt.Audience{"harness"}, Expiry: jwt.NewNumericDate(time.Now().Add(time.Hour))}).Serialize()
		if e != nil {
			t.Fatal(e)
		}
		return s
	}
	authenticate := func(token string) error {
		r := httptest.NewRequest("GET", "http://harness/chat", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		_, e := auth.Authenticate(context.Background(), r)
		return e
	}
	if e := authenticate(token(old, "old")); e != nil {
		t.Fatal(e)
	}
	mu.Lock()
	current = rotated
	currentID = "rotated"
	mu.Unlock()
	if e := authenticate(token(rotated, "rotated")); e != nil {
		t.Fatalf("rotated issuer key rejected: %v", e)
	}
	if n := requests.Load(); n != 2 {
		t.Fatalf("rotation needed %d JWKS fetches, want 2 total", n)
	}
	unknown := token(old, "attacker-unknown")
	var wg sync.WaitGroup
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if e := authenticate(unknown); !errors.Is(e, ErrUnauthorized) {
				t.Errorf("unknown key accepted or wrong error: %v", e)
			}
		}()
	}
	wg.Wait()
	if n := requests.Load(); n != 2 {
		t.Fatalf("unknown-key flood caused extra JWKS requests: %d", n)
	}
}
