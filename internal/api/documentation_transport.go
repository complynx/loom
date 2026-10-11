package api

import (
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"
)

func historyAndStreamParameters(method, path string) []*huma.Param {
	if method != http.MethodGet {
		return nil
	}
	if strings.HasSuffix(path, "/history") {
		return []*huma.Param{
			{
				Name:        "after",
				In:          queryLocation,
				Description: "Return events after this sequence",
				Schema:      &huma.Schema{Type: schemaInteger, Format: "int64", Minimum: new(float64(0)), Default: 0},
			},
			{
				Name: "limit",
				In:   queryLocation,
				Schema: &huma.Schema{
					Type:    schemaInteger,
					Minimum: new(float64(1)),
					Maximum: new(float64(maxHistoryPageSize)),
					Default: defaultHistoryPageSize,
				},
			},
		}
	}
	if strings.HasPrefix(path, "/v1/runs/") && strings.HasSuffix(path, "/events") {
		return []*huma.Param{
			{
				Name:        "after",
				In:          queryLocation,
				Description: "Last consumed sequence; use either after or Last-Event-ID",
				Schema:      &huma.Schema{Type: schemaInteger, Format: "int64", Minimum: new(float64(0))},
			},
			{
				Name:        "Last-Event-ID",
				In:          "header",
				Description: "Nonnegative decimal sequence; mutually exclusive with after",
				Schema:      &huma.Schema{Type: schemaString, Pattern: "^[0-9]+$"},
			},
		}
	}
	return nil
}

func artifactUploadBody() *huma.RequestBody {
	content := map[string]*huma.MediaType{}
	for _, mime := range []string{"application/octet-stream", "image/png", "image/jpeg", "image/gif", "image/webp"} {
		content[mime] = &huma.MediaType{Schema: &huma.Schema{Type: schemaString, Format: "binary"}}
	}
	return &huma.RequestBody{
		Required:    true,
		Description: "Send raw bytes with the actual file MIME. Model image attachments require a matching supported image MIME.",
		Content:     content,
	}
}
