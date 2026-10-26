package modules_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/modules"
)

func TestStaticAndDynamicMethodSkillsFollowAuthority(t *testing.T) {
	t.Parallel()
	for _, dynamic := range []bool{false, true} {
		t.Run(map[bool]string{false: "static", true: "dynamic"}[dynamic], func(t *testing.T) {
			t.Parallel()
			manifest := example(t)
			method := manifest.Modules[0].Methods[0]
			method.Visibility = "hidden"
			method.Skills = []domain.SkillDefinition{
				{
					Name:    "find-slots",
					Usecase: "Find suitable appointments",
					Body:    "Read available slots first",
					Require: []string{"massage.list_slots"},
				},
			}
			manifest.Modules = manifest.Modules[:1]
			manifest.Modules[0].Visibility = "list"
			manifest.Modules[0].Methods = []modules.Method{method}
			if dynamic {
				manifest.Modules[0].Discovery = "dynamic"
				manifest.Modules[0].DiscoveryPath = "/discover"
				manifest.Modules[0].CacheTTLSeconds = 1
				manifest.Modules[0].Methods = nil
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/manifest" {
					_ = json.NewEncoder(w).Encode(manifest)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"methods": []modules.Method{method}})
			}))
			defer server.Close()
			registry, err := modules.NewRegistry(modules.SourceFunc(func(context.Context) ([]modules.Endpoint, error) {
				return []modules.Endpoint{endpoint(t, server.URL, "skills")}, nil
			}), modules.Options{})
			require.NoError(t, err)
			require.NoError(t, registry.Refresh(t.Context()))
			p := principal()
			tools, err := registry.List(t.Context(), p)
			require.NoError(t, err)
			require.Len(t, tools, 1)
			require.Equal(t, "hidden", tools[0].Visibility)
			require.Equal(t, method.Skills, tools[0].Skills)
			p.Permissions = nil
			tools, err = registry.List(t.Context(), p)
			require.NoError(t, err)
			require.Empty(t, tools)
		})
	}
}
