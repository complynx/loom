package sandbox_test

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/sandbox"
)

func TestFileHelperSerializesSourceAndReopensHandlesWithoutBytes(t *testing.T) {
	t.Parallel()
	var fetches, opens, reads atomic.Int32
	broker := sandbox.BrokerFunc(
		func(_ context.Context, module, method string, raw json.RawMessage) (json.RawMessage, error) {
			switch module + "." + method {
			case "artifacts.fetch":
				fetches.Add(1)
				return json.RawMessage(`{"artifacts":[{"artifact_id":"temporary","reference_id":"stable"}]}`), nil
			case "artifacts.open":
				opens.Add(1)
				require.JSONEq(t, `{"artifact_id":"stable"}`, string(raw))
				return json.RawMessage(`{"artifact_id":"current","handle_id":"fresh-handle"}`), nil
			case "files.text_read":
				reads.Add(1)
				require.JSONEq(t, `{"handle_id":"fresh-handle","limit":100}`, string(raw))
				return json.RawMessage(`{"data":{"text":"excerpt"}}`), nil
			default:
				t.Errorf("unexpected method %s.%s", module, method)
				return nil, nil
			}
		},
	)
	result, err := (sandbox.Inline{}).Execute(t.Context(), `
 const file=modules.artifacts.file({module:"documents",method:"fetch",external_id:"document-v1"});
 await Promise.all([file.handle(),file.handle()]);
 const text=await file.call("text_read",{limit:100,handle_id:"ignored"});
 const saved=JSON.parse(JSON.stringify(file));
 const restored=modules.artifacts.file(saved);
 await restored.handle();
 return {saved,text,current:file.currentId};`, []sandbox.Binding{{Module: "artifacts", Methods: []string{"fetch", "open"}}, {Module: "files", Methods: []string{"text_read"}}}, broker)
	require.NoError(t, err)
	require.EqualValues(t, 1, fetches.Load())
	require.EqualValues(t, 3, opens.Load())
	require.EqualValues(t, 1, reads.Load())
	require.Contains(t, string(result), "stable")
	require.Contains(t, string(result), "document-v1")
	require.Contains(t, string(result), "excerpt")
	require.NotContains(t, string(result), "base64")
}
