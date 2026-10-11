package app

import (
	"errors"
	"net/http"

	"github.com/complynx/clx-scripter/internal/config"
	"github.com/complynx/clx-scripter/internal/provider"
)

// Transport selection does not change the model snapshot or its production tariff.
func configuredProvider(p config.Provider) (provider.Client, error) {
	switch p.Kind {
	case "", "responses":
		return provider.NewResponses(p.Endpoint, p.APIKey, &http.Client{Timeout: inferenceTimeout})
	case "mock":
		return provider.NewMock(p.Mailbox)
	case "codex":
		return provider.NewCodex(p.Executable, p.CodexHome)
	default:
		return nil, errors.New("unknown provider kind")
	}
}
