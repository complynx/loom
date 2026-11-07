package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/api"
	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/store"
)

func fundingAPIStore(t *testing.T) *store.Store {
	t.Helper()
	dsn := os.Getenv("CLX_TEST_DATABASE_DSN")
	if dsn == "" {
		t.Skip("CLX_TEST_DATABASE_DSN is required for PostgreSQL behavior tests")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	schema := "funding_api_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.Exec(ctx, "CREATE SCHEMA "+schema)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, cleanupErr := admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		require.NoError(t, cleanupErr)
		admin.Close()
	})
	parsed, err := url.Parse(dsn)
	require.NoError(t, err)
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	require.NoError(t, store.Migrate(ctx, parsed.String()))
	pool, err := pgxpool.New(ctx, parsed.String())
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	repository := store.New(pool, nil)
	require.NoError(t, repository.Bootstrap(ctx, domain.Settings{
		Models: []domain.Model{
			{ID: "test", ProviderModel: "test", Efforts: []string{"low"}, MaxEffort: "low", MaxOutputTokens: 100},
		},
		DefaultModelID:         "test",
		DefaultEffort:          "low",
		InitialCredits:         10000,
		MaxRunMicrocredits:     1000,
		ActiveRunLimit:         8,
		HistoryRecentLimit:     50,
		MaxSteps:               10,
		QuestionTimeoutSeconds: 60,
	}))

	return repository
}

type fundingOperatorAuth struct{ authFunc }

func (a fundingOperatorAuth) AuthenticateOperator(ctx context.Context, r *http.Request) (domain.Principal, error) {
	return a.Authenticate(ctx, r)
}
func TestMonthlyFundingHTTPAdministration(t *testing.T) {
	t.Parallel()
	s := fundingAPIStore(t)
	ctx := t.Context()
	p, err := allowed(ctx, nil)
	require.NoError(t, err)
	p.Permissions["billing.adjust"] = true
	p.Permissions["settings.read"] = true
	p.Permissions["settings.write"] = true
	uid, err := s.ResolveUser(ctx, p)
	require.NoError(t, err)
	settings, err := s.GetSettings(ctx)
	require.NoError(t, err)
	settings.InitialCredits = 9007199254740993
	_, err = s.UpdateSettings(ctx, p, settings.Version, settings)
	require.NoError(t, err)
	auth := fundingOperatorAuth{authFunc(func(_ context.Context, r *http.Request) (domain.Principal, error) {
		got := p
		if r.Header.Get("X-Test-No-Privilege") == "1" {
			got.Permissions = map[string]bool{"chat.read": true}
		}
		return got, nil
	})}
	handler := api.New(s, auth, api.Options{AdminOrganizationID: p.OrgID, OperatorProxyOrigin: "https://panel.example"})
	defaults := request(handler, "GET", "/v1/admin/settings/monthly-funding", "", "", "")
	require.Equal(t, 200, defaults.Code)
	var config domain.MonthlyFundingConfiguration
	require.NoError(t, json.Unmarshal(defaults.Body.Bytes(), &config))
	config.Defaults = domain.MonthlyFundingDefaults{Enabled: true, BaseMicrocredits: 1000000, FactorPPM: 1000000}
	raw, err := json.Marshal(config)
	require.NoError(t, err)
	saved := request(handler, "PATCH", "/v1/admin/settings/monthly-funding", string(raw), "application/json", "")
	require.Equal(t, 200, saved.Code, saved.Body.String())
	settings, err = s.GetSettings(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 9007199254740993, settings.InitialCredits)
	stale := request(handler, "PATCH", "/v1/admin/settings/monthly-funding", string(raw), "application/json", "")
	require.Equal(t, 409, stale.Code)
	path := "/v1/admin/billing/" + uid + "/monthly-funding"
	response := request(
		handler,
		"PATCH",
		path,
		`{"enabled":true,"base_microcredits":2000000,"factor_ppm":1500000,"version":0}`,
		"application/json",
		"",
	)
	require.Equal(t, 200, response.Code, response.Body.String())
	var status domain.MonthlyFundingStatus
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &status))
	require.EqualValues(t, 3000000, status.TargetMicrocredits)
	for _, tc := range []struct {
		origin, header string
		want           int
	}{
		{"", "", 403}, {"https://other.example", "1", 403}, {"https://panel.example", "", 403}, {"https://panel.example", "1", 200},
	} {
		r := httptest.NewRequest(
			http.MethodPatch,
			"/admin/api/billing/"+uid+"/monthly-funding",
			strings.NewReader(`{"enabled":false,"base_microcredits":null,"factor_ppm":null,"version":1}`),
		)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", tc.origin)
		r.Header.Set("X-Loom-Admin-Request", tc.header)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		require.Equal(t, tc.want, w.Code, w.Body.String())
	}
	r := httptest.NewRequest(http.MethodPatch, path, strings.NewReader(`{"enabled":true,"version":2}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Test-No-Privilege", "1")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	require.Equal(t, 403, w.Code)
	response = request(handler, "GET", "/v1/users/me/credits", "", "", "")
	require.Equal(t, 200, response.Code)
	require.Contains(t, response.Body.String(), "monthly_funding")
}
