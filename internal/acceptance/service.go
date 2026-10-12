// Package acceptance implements a persistent local QA service, never a production adapter.
package acceptance

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/complynx/loom/internal/modules"
)

//go:embed manifest.json
var manifest []byte

const maxDocumentBytes = 1 << 20
const maxQueryChars = 1000
const fieldStatus = "status"
const fieldOperationID = "operation_id"
const fieldResult = "result"
const fieldValue = "value"
const stateSucceeded = "succeeded"
const maxOperations = 1000
const prepareTTL = 5 * time.Minute
const fixtureBlob = "Acceptance blob. Original bytes bypass JavaScript.\n"

type envelope struct {
	Protocol       int             `json:"protocol_version"`
	ID             string          `json:"operation_id"`
	RunID          string          `json:"run_id"`
	Arguments      json.RawMessage `json:"arguments"`
	IdempotencyKey string          `json:"idempotency_key"`
	Confirmation   struct {
		Mode string `json:"mode"`
		ID   string `json:"confirmation_id"`
	} `json:"confirmation"`
}
type record struct {
	Actor        string          `json:"actor"`
	RunID        string          `json:"run_id"`
	Arguments    json.RawMessage `json:"arguments"`
	Confirmation string          `json:"confirmation_id"`
	Expires      time.Time       `json:"expires_at"`
	Applied      bool            `json:"applied"`
}

type Service struct {
	mu                          sync.Mutex
	path, readToken, writeToken string
	operations                  map[string]record
	dropNext                    bool
}

// New loads deduplication state. Tokens belong only to the isolated QA stand.
func New(path, readToken, writeToken string) (*Service, error) {
	if path == "" || readToken == "" || writeToken == "" || readToken == writeToken {
		return nil, errors.New("state path and two distinct QA tokens required")
	}
	service := &Service{path: path, readToken: readToken, writeToken: writeToken, operations: map[string]record{}}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return service, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	raw, err := io.ReadAll(io.LimitReader(file, maxDocumentBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxDocumentBytes {
		return nil, errors.New("QA state exceeds limit")
	}
	if err = json.Unmarshal(raw, &service.operations); err != nil {
		return nil, err
	}
	if service.operations == nil || len(service.operations) > maxOperations {
		return nil, errors.New("invalid QA state")
	}
	return service, nil
}

func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /manifest", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(manifest)
	})
	mux.HandleFunc("POST /read", s.read)
	mux.HandleFunc("POST /fetch-file", s.fetchFile)
	mux.HandleFunc("POST /discover", s.discover)
	mux.HandleFunc("GET /blob", s.blob)
	mux.HandleFunc("POST /prepare", s.prepare)
	mux.HandleFunc("POST /write", s.write)
	mux.HandleFunc("POST /status", s.status)
	mux.HandleFunc("POST /qa/faults", s.faults)
	return mux
}
func (s *Service) authorized(w http.ResponseWriter, r *http.Request, write bool) bool {
	token := r.Header.Get("Authorization")
	isWrite := subtle.ConstantTimeCompare([]byte(token), []byte("Bearer "+s.writeToken)) == 1
	isRead := subtle.ConstantTimeCompare([]byte(token), []byte("Bearer "+s.readToken)) == 1
	if !isWrite && (write || !isRead) {
		http.Error(w, "QA token denied", http.StatusForbidden)
		return false
	}
	return true
}
func decode(w http.ResponseWriter, r *http.Request, out any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxDocumentBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		http.Error(w, "Invalid request", http.StatusUnprocessableEntity)
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		http.Error(w, "One JSON document required", http.StatusUnprocessableEntity)
		return false
	}
	return true
}
func respond(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
func identity(r *http.Request) string {
	digest := sha256.Sum256([]byte(r.Header.Get("Authorization")))
	return hex.EncodeToString(digest[:])
}
func arguments(raw json.RawMessage) (string, error) {
	var input struct {
		Query *string `json:"query"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return "", err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) || input.Query == nil {
		return "", errors.New("one query document required")
	}
	if utf8.RuneCountInString(*input.Query) > maxQueryChars {
		return "", errors.New("query too long")
	}
	return *input.Query, nil
}
func normalizedArguments(raw json.RawMessage) (json.RawMessage, error) {
	query, err := arguments(raw)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]string{"query": query})
}
func (s *Service) read(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(w, r, false) {
		return
	}
	var in envelope
	if !decode(w, r, &in) {
		return
	}
	value, err := arguments(in.Arguments)
	if err != nil || in.Protocol != 1 || in.ID == "" {
		http.Error(w, "Invalid envelope", http.StatusUnprocessableEntity)
		return
	}
	digest := sha256.Sum256([]byte(fixtureBlob))
	respond(
		w,
		map[string]any{
			fieldStatus:      stateSucceeded,
			fieldOperationID: in.ID,
			fieldResult:      map[string]string{fieldValue: "Fixture read: " + value},
			"artifacts": []modules.ArtifactDescriptor{
				{
					DownloadPath: "/blob",
					MIMEType:     "text/plain",
					SizeBytes:    int64(len(fixtureBlob)),
					SHA256:       hex.EncodeToString(digest[:]),
				},
			},
		},
	)
}
func (s *Service) discover(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(w, r, false) {
		return
	}
	var doc modules.Manifest
	if err := json.Unmarshal(manifest, &doc); err != nil {
		http.Error(w, "Manifest unavailable", http.StatusInternalServerError)
		return
	}
	method := doc.Modules[0].Methods[0]
	method.ID = "lookup"
	respond(w, map[string]any{"methods": []modules.Method{method}})
}
func (s *Service) blob(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(w, r, false) {
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	_, _ = io.WriteString(w, fixtureBlob)
}
