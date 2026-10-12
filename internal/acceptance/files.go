package acceptance

import (
	"crypto/sha256"
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
		args.ExternalID != "fixture-file" {
		http.Error(w, "File not found", http.StatusNotFound)
		return
	}
	digest := sha256.Sum256([]byte(fixtureBlob))
	respond(w, map[string]any{
		fieldStatus:      stateSucceeded,
		fieldOperationID: in.ID,
		fieldResult:      map[string]string{fieldValue: "fixture-file"},
		"artifacts": []modules.ArtifactDescriptor{
			{
				ExternalID:   args.ExternalID,
				DownloadPath: "/blob",
				MIMEType:     "text/plain",
				SizeBytes:    int64(len(fixtureBlob)),
				SHA256:       hex.EncodeToString(digest[:]),
			},
		},
	})
}
