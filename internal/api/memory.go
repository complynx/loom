package api

import (
	"net/http"
	"reflect"
	"strconv"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/complynx/clx-scripter/internal/domain"
)

const (
	memoryPath         = "/v1/users/me/memory"
	memoryLimitQuery   = "limit"
	memoryPathLocation = "path"
)

type memoryVersionRequest struct {
	Version int64 `json:"version"`
}
type memoryRevisionRequest struct {
	Revision int64 `json:"revision"`
}

func (s *server) registerMemory(mux *http.ServeMux) {
	s.memoryRoute(mux, "GET", "", s.memoryState, nil, domain.MemoryState{})
	s.memoryRoute(mux, "PUT", "/micro", s.memoryMicro, domain.MemoryWrite{}, domain.MemoryDocument{})
	s.memoryRoute(mux, "PUT", "/index", s.memoryIndex, domain.MemoryWrite{}, domain.MemoryDocument{})
	s.memoryRoute(mux, "DELETE", "", s.memoryClear, memoryRevisionRequest{}, nil)
	s.memoryRoute(
		mux,
		"POST",
		"/search",
		s.memorySearch,
		domain.MemoryListRequest{},
		domain.MemoryListResult{},
	)
	s.memoryRoute(mux, "GET", "/entries/{key}", s.memoryRead, nil, domain.MemoryReadResult{})
	s.memoryRoute(
		mux,
		"PUT",
		"/entries/{key}",
		s.memoryPut,
		domain.MemoryEntryWrite{},
		domain.MemoryEntry{},
	)
	s.memoryRoute(mux, "DELETE", "/entries/{key}", s.memoryDelete, memoryVersionRequest{}, nil)
}

// memoryRoute adds typed documentation to the standard authenticated route.
func (s *server) memoryRoute(
	mux *http.ServeMux,
	method, suffix string,
	handler endpoint,
	input, output any,
) {
	path := memoryPath + suffix
	s.route(mux, method+" "+path, "", handler)
	item := s.docs.OpenAPI().Paths[path]
	var op *huma.Operation
	switch method {
	case http.MethodGet:
		op = item.Get
	case http.MethodPut:
		op = item.Put
	case http.MethodPost:
		op = item.Post
	case http.MethodDelete:
		op = item.Delete
	}
	schema := func(value any) *huma.Schema {
		return huma.SchemaFromType(s.docs.OpenAPI().Components.Schemas, reflect.TypeOf(value))
	}
	if input != nil {
		op.RequestBody = &huma.RequestBody{
			Required: true,
			Content:  map[string]*huma.MediaType{jsonMediaType: {Schema: schema(input)}},
		}
	}
	if output != nil {
		op.Responses["200"].Content = map[string]*huma.MediaType{jsonMediaType: {Schema: schema(output)}}
	}
	if strings.Contains(suffix, "{key}") {
		op.Parameters = append(
			op.Parameters,
			&huma.Param{Name: "key", In: memoryPathLocation, Required: true, Schema: &huma.Schema{Type: schemaString}},
		)
	}
	if method == http.MethodGet && suffix != "" {
		for _, name := range []string{"offset", memoryLimitQuery} {
			op.Parameters = append(
				op.Parameters,
				&huma.Param{Name: name, In: queryLocation, Schema: &huma.Schema{Type: schemaInteger}},
			)
		}
	}
}

func (s *server) memoryState(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	result, err := s.store.MemoryState(r.Context(), p)
	if err == nil {
		write(w, http.StatusOK, result)
	}
	return err
}
func (s *server) memoryMicro(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	var input domain.MemoryWrite
	if err := s.decode(w, r, &input); err != nil {
		return err
	}
	result, err := s.store.SetMemoryMicro(r.Context(), p, input)
	if err == nil {
		write(w, http.StatusOK, result)
	}
	return err
}
func (s *server) memoryIndex(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	var input domain.MemoryWrite
	if err := s.decode(w, r, &input); err != nil {
		return err
	}
	result, err := s.store.SetMemoryIndex(r.Context(), p, input)
	if err == nil {
		write(w, http.StatusOK, result)
	}
	return err
}
func (s *server) memoryClear(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	var input memoryRevisionRequest
	if err := s.decode(w, r, &input); err != nil {
		return err
	}
	if err := s.store.ClearUserMemory(r.Context(), p, input.Revision); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
func (s *server) memorySearch(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	var input domain.MemoryListRequest
	if err := s.decode(w, r, &input); err != nil {
		return err
	}
	result, err := s.store.ListMemoryEntries(r.Context(), p, input)
	if err == nil {
		write(w, http.StatusOK, result)
	}
	return err
}
func (s *server) memoryRead(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	var input domain.MemoryReadRequest
	for name, target := range map[string]*int{"offset": &input.Offset, memoryLimitQuery: &input.Limit} {
		raw := r.URL.Query().Get(name)
		if raw == "" {
			continue
		}
		value, err := strconv.Atoi(raw)
		if err != nil {
			return domain.NewError(http.StatusUnprocessableEntity, "invalid_memory_query")
		}
		*target = value
	}
	result, err := s.store.ReadMemoryEntry(r.Context(), p, r.PathValue("key"), input)
	if err == nil {
		write(w, http.StatusOK, result)
	}
	return err
}
func (s *server) memoryPut(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	var input domain.MemoryEntryWrite
	if err := s.decode(w, r, &input); err != nil {
		return err
	}
	result, err := s.store.PutMemoryEntry(r.Context(), p, r.PathValue("key"), input)
	if err == nil {
		write(w, http.StatusOK, result)
	}
	return err
}
func (s *server) memoryDelete(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	var input memoryVersionRequest
	if err := s.decode(w, r, &input); err != nil {
		return err
	}
	if err := s.store.DeleteMemoryEntry(r.Context(), p, r.PathValue("key"), input.Version); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
