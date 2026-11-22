package mcpoauth

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	maxUpstreamTokenResponse = 64 << 10
	maxUpstreamTokenSeconds  = 3600
)

func (b *Broker) exchange(ctx context.Context, f flow, code string) (string, time.Time, error) {
	if code == "" || len(code) > 8192 {
		return "", time.Time{}, errors.New("invalid upstream authorization code")
	}
	form := url.Values{
		"grant_type": {authorizationCodeGrant}, fieldCode: {code},
		"redirect_uri": {b.callbackURL()}, "code_verifier": {f.Verifier},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		b.options.UpstreamIssuer+"/oauth/v2/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", time.Time{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(url.QueryEscape(b.options.ClientID), url.QueryEscape(b.options.ClientSecret))
	response, err := b.options.HTTP.Do(req)
	if err != nil {
		return "", time.Time{}, errors.New("upstream token endpoint unavailable")
	}
	defer response.Body.Close()
	var result struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if response.StatusCode != http.StatusOK ||
		json.NewDecoder(io.LimitReader(response.Body, maxUpstreamTokenResponse)).Decode(&result) != nil ||
		result.AccessToken == "" || len(result.AccessToken) > 8192 ||
		!strings.EqualFold(result.TokenType, "Bearer") || result.ExpiresIn <= 0 {
		return "", time.Time{}, errors.New("upstream rejected MCP authorization")
	}
	expires := time.Now().Add(time.Duration(min(result.ExpiresIn, maxUpstreamTokenSeconds)) * time.Second)
	return result.AccessToken, expires, nil
}

func validVerifier(value string) bool {
	return len(value) >= 43 && len(value) <= 128 && !strings.ContainsFunc(value, func(r rune) bool {
		return !strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_.~", r)
	})
}

func (b *Broker) token(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	if r.ParseForm() != nil || single(r.PostForm, "grant_type") != authorizationCodeGrant ||
		!b.validResource(r.PostForm) || r.Header.Get("Authorization") != "" ||
		len(r.PostForm["client_secret"]) != 0 {
		oauthError(w, "invalid_request")
		return
	}
	verifier := single(r.PostForm, "code_verifier")
	code := single(r.PostForm, fieldCode)
	b.mu.Lock()
	now := time.Now()
	b.prune(now)
	f, ok := b.codes[code]
	if !ok || f.ClientKey != hash(single(r.PostForm, "client_id")) ||
		f.Redirect != single(r.PostForm, "redirect_uri") || !validVerifier(verifier) ||
		subtle.ConstantTimeCompare([]byte(f.Challenge), []byte(hash(verifier))) != 1 {
		b.mu.Unlock()
		oauthError(w, "invalid_grant")
		return
	}
	delete(b.codes, code)
	b.mu.Unlock()
	seconds := int64(f.upstreamExpiry.Sub(now) / time.Second)
	if seconds <= 0 {
		oauthError(w, "invalid_grant")
		return
	}
	active, err := b.options.CheckAuthorization(r.Context(), f.owner, f.approvalID, f.Client)
	if err != nil {
		jsonResponse(w, http.StatusServiceUnavailable, map[string]string{fieldError: "temporarily_unavailable"})
		return
	}
	if !active {
		oauthError(w, "invalid_grant")
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{
		"access_token": accessEnvelopePrefix + b.encode(
			accessEnvelopePurpose,
			accessToken{f.upstream, f.Client, f.approvalID, f.upstreamExpiry},
		),
		"expires_in": seconds,
		"token_type": "Bearer",
		fieldScope:   scope,
	})
}
