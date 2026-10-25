package app

import (
	"encoding/base64"
	"strings"

	"github.com/complynx/loom/internal/artifacts"
)

const modelContentTypeKey = "type"

// Keep each mask adjacent to its original so multiple attachments cannot be confused.
func modelImageContent(picture artifacts.ModelArtifact) []map[string]string {
	content := []map[string]string{
		{
			modelContentTypeKey: "input_image",
			"image_url": "data:" + strings.ToLower(
				picture.Metadata.MIME,
			) + ";base64," + base64.StdEncoding.EncodeToString(
				picture.Bytes,
			),
		},
	}
	if len(picture.AlphaPNG) > 0 {
		content = append(
			content,
			map[string]string{
				modelContentTypeKey: "input_text",
				"text":              "Alpha mask for the preceding original image, at the same dimensions: black means transparent, white means opaque, gray means partial transparency. Interpret the original together with this mask; the mask is not scene content.",
			},
			map[string]string{
				modelContentTypeKey: "input_image",
				"image_url":         "data:image/png;base64," + base64.StdEncoding.EncodeToString(picture.AlphaPNG),
			},
		)
	}
	return content
}
