package runs

import (
	"encoding/json"

	"github.com/complynx/loom/internal/provider"
)

// Compaction checkpoints must not become a second durable blob store.
func omitCompactedFileBytes(items []provider.Item) []provider.Item {
	out := make([]provider.Item, 0, len(items))
	for _, raw := range items {
		var item map[string]json.RawMessage
		if json.Unmarshal(raw, &item) != nil {
			out = append(out, raw)
			continue
		}
		var content []map[string]json.RawMessage
		if json.Unmarshal(item["content"], &content) != nil {
			out = append(out, raw)
			continue
		}
		for i, part := range content {
			var kind string
			_ = json.Unmarshal(part["type"], &kind)
			if kind == "input_image" || kind == "input_file" || kind == "input_audio" {
				content[i] = map[string]json.RawMessage{
					"type": json.RawMessage(`"input_text"`),
					"text": json.RawMessage(
						`"Temporary file omitted from checkpoint. Reopen a live artifact or fetch again from its source service."`,
					),
				}
			}
		}
		item["content"], _ = json.Marshal(content)
		encoded, _ := json.Marshal(item)
		out = append(out, encoded)
	}
	return out
}
