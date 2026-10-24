package runs

import (
	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/provider"
)

func profileTools(run domain.Run, tools []provider.Tool) []provider.Tool {
	allowed := tools[:0]
	for _, tool := range tools {
		if run.AllowsModelTool(tool.Name) {
			allowed = append(allowed, tool)
		}
	}
	return allowed
}
