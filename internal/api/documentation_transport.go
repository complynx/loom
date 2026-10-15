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
				In:          headerLocation,
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
		content[mime] = &huma.MediaType{Schema: &huma.Schema{Type: schemaString, Format: binaryFormat}}
	}
	return &huma.RequestBody{
		Required:    true,
		Description: "Send raw bytes with the actual file MIME. Model image attachments require a matching supported image MIME.",
		Content:     content,
	}
}

func artifactContentOperation(op *huma.Operation) bool {
	if op.Method != http.MethodGet || !strings.HasSuffix(op.Path, "/content") ||
		!strings.HasPrefix(op.Path, "/v1/artifact") {
		return false
	}
	for _, name := range []string{"Range", "If-Range", "If-None-Match", "If-Match", "If-Modified-Since", "If-Unmodified-Since"} {
		op.Parameters = append(
			op.Parameters,
			&huma.Param{Name: name, In: headerLocation, Schema: &huma.Schema{Type: schemaString}},
		)
	}
	content := map[string]*huma.MediaType{
		"application/octet-stream": {Schema: &huma.Schema{Type: schemaString, Format: binaryFormat}},
		"*/*":                      {Schema: &huma.Schema{Type: schemaString, Format: binaryFormat}},
	}
	headers := map[string]*huma.Param{}
	for _, name := range []string{"Accept-Ranges", "Content-Range", "ETag", "Last-Modified"} {
		headers[name] = &huma.Param{Schema: &huma.Schema{Type: schemaString}}
	}
	op.Responses["200"] = &huma.Response{
		Description: "Original bytes; HEAD returns headers only",
		Content:     content,
		Headers:     headers,
	}
	op.Responses["206"] = &huma.Response{
		Description: "Requested byte range or multipart byte ranges",
		Content:     content,
		Headers:     headers,
	}
	op.Responses["304"] = &huma.Response{
		Description: "Authorized conditional read: content has not changed",
		Headers:     headers,
	}
	op.Responses["412"] = &huma.Response{Description: "Conditional read precondition failed"}
	op.Responses["416"] = &huma.Response{Description: "Range is invalid or unsatisfiable", Headers: headers}
	return true
}

func artifactHeadOperation(api huma.API, source *huma.Operation) {
	head := *source
	head.Method = http.MethodHead
	head.OperationID += "-head"
	head.Parameters = nil
	for _, parameter := range source.Parameters {
		if parameter.In != headerLocation || (parameter.Name != "Range" && parameter.Name != "If-Range") {
			head.Parameters = append(head.Parameters, parameter)
		}
	}
	head.Responses = make(map[string]*huma.Response, len(source.Responses))
	for status, response := range source.Responses {
		if status == "206" || status == "416" {
			continue
		}
		value := *response
		value.Content = nil
		head.Responses[status] = &value
	}
	api.OpenAPI().AddOperation(&head)
}
