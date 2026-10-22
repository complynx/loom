package modules

import (
	"errors"
	"slices"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
)

// InvalidArgumentsError reports local caller-input failures without raw values or validator messages.
type InvalidArgumentsError struct {
	Reason string   `json:"reason"`
	Fields []string `json:"fields,omitempty"`
}

func (*InvalidArgumentsError) Error() string { return "invalid module arguments" }

// argumentSchemaError exposes only bounded, declared top-level field names.
// Nested or additional-property keys can contain caller data and are never copied.
func argumentSchemaError(schema *jsonschema.Schema, err error) error {
	failure := &InvalidArgumentsError{Reason: "schema_validation"}
	validation, ok := errors.AsType[*jsonschema.ValidationError](err)
	if !ok {
		return failure
	}
	const maxFields, maxCauses = 8, 32
	add := func(name string) {
		if schema.Properties[name] != nil && identifier.MatchString(name) && len(failure.Fields) < maxFields &&
			!slices.Contains(failure.Fields, name) {
			failure.Fields = append(failure.Fields, name)
		}
	}
	queue := []*jsonschema.ValidationError{validation}
	for visited := 0; len(queue) > 0 && visited < maxCauses; visited++ {
		current := queue[0]
		queue = queue[1:]
		if len(current.InstanceLocation) > 0 {
			add(current.InstanceLocation[0])
		} else if required, isRequired := current.ErrorKind.(*kind.Required); isRequired {
			for _, name := range required.Missing {
				add(name)
			}
		}
		remaining := maxCauses - visited - len(queue) - 1
		queue = append(queue, current.Causes[:min(len(current.Causes), remaining)]...)
	}
	slices.Sort(failure.Fields)
	return failure
}
