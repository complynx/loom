package acceptance

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"

	"github.com/complynx/loom/internal/modules"
)

func (s *Service) fetchFile(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(w, r, false) {
		return
	}
	var in envelope
	if !decode(w, r, &in) {
		return
	}
	var args struct {
		ExternalID string `json:"external_id"`
	}
	if json.Unmarshal(in.Arguments, &args) != nil || in.Protocol != 1 || in.ID == "" ||
		(args.ExternalID != "fixture-file" && args.ExternalID != "fixture-image") {
		http.Error(w, "File not found", http.StatusNotFound)
		return
	}
	data, path, mimeType := fixtureFile(args.ExternalID)
	digest := sha256.Sum256(data)
	respond(w, map[string]any{
		fieldStatus:      stateSucceeded,
		fieldOperationID: in.ID,
		fieldResult:      map[string]string{fieldValue: args.ExternalID},
		"artifacts": []modules.ArtifactDescriptor{
			{
				ExternalID:   args.ExternalID,
				FetchMethod:  "fetch_file",
				DownloadPath: path,
				MIMEType:     mimeType,
				SizeBytes:    int64(len(data)),
				SHA256:       hex.EncodeToString(digest[:]),
			},
		},
	})
}

func fixtureFile(id string) ([]byte, string, string) {
	if id == "fixture-image" {
		data, _ := base64.StdEncoding.DecodeString(
			"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jfWQAAAAASUVORK5CYII=",
		)
		return data, "/fixture-image", "image/png"
	}
	return []byte(fixtureBlob), "/blob", "text/plain"
}

func (s *Service) fileImage(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(w, r, false) {
		return
	}
	data, _, mimeType := fixtureFile("fixture-image")
	w.Header().Set("Content-Type", mimeType)
	_, _ = w.Write(data)
}
