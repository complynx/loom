package modules

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestArgumentDiagnosticsExcludeValuesAndUndeclaredKeys(t *testing.T) {
	t.Parallel()
	schema, err := compileSchema(json.RawMessage(
		`{"type":"object","properties":{"expected_version":{"type":"integer"},"record":{"type":"object","additionalProperties":false}},"additionalProperties":false}`,
	))
	require.NoError(t, err)
	err = schema.Validate(map[string]any{
		"expected_version": "PRIVATE_VALUE",
		"PRIVATE_KEY":      "SECRET",
		"record":           map[string]any{"NESTED_PRIVATE_KEY": "SECRET"},
	})
	require.Error(t, err)
	failure := argumentSchemaError(schema, err)
	raw, err := json.Marshal(failure)
	require.NoError(t, err)
	require.JSONEq(t, `{"reason":"schema_validation","fields":["expected_version","record"]}`, string(raw))
	require.EqualError(t, failure, "invalid module arguments")
}

func TestArgumentDiagnosticsBoundMissingFields(t *testing.T) {
	t.Parallel()
	properties := map[string]any{}
	required := []string{}
	for i := range 20 {
		name := fmt.Sprintf("field_%02d", i)
		properties[name] = map[string]string{"type": "integer"}
		required = append(required, name)
	}
	longName := "field_" + strings.Repeat("a", 200)
	properties[longName] = map[string]string{"type": "integer"}
	required = append([]string{longName}, required...)
	raw, err := json.Marshal(map[string]any{"type": "object", "properties": properties, "required": required})
	require.NoError(t, err)
	schema, err := compileSchema(raw)
	require.NoError(t, err)
	err = schema.Validate(map[string]any{})
	require.Error(t, err)
	failure, ok := errors.AsType[*InvalidArgumentsError](argumentSchemaError(schema, err))
	require.True(t, ok)
	require.Len(t, failure.Fields, 8)
	require.NotContains(t, failure.Fields, longName)
}
