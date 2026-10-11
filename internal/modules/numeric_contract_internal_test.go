package modules

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNumericContractPreservesExactBoundsAndArguments(t *testing.T) {
	t.Parallel()
	schema := json.RawMessage(
		`{"type":"object","properties":{"id":{"type":"integer","maximum":9007199254740993}},"required":["id"],"additionalProperties":false}`,
	)
	arguments := json.RawMessage(`{"id":9007199254740993}`)
	doc, err := validateReadArguments(schema, arguments)
	require.NoError(t, err)
	envelope, err := json.Marshal(doc)
	require.NoError(t, err)
	require.Equal(t, string(arguments), string(envelope))
	_, err = validateReadArguments(schema, json.RawMessage(`{"id":9007199254740994}`))
	require.Error(t, err)
	_, err = validateReadArguments(
		json.RawMessage(`{"type":"object","properties":{"id":{"type":"integer","maximum":9007199254740992}}}`),
		arguments,
	)
	require.Error(t, err)
	_, err = validateReadArguments(schema, json.RawMessage(`{"id":1} {}`))
	require.Error(t, err)
}
