// Schema compilation for Workflow input/output contracts. An empty JSON
// Schema document imposes no constraints.

package registry

import (
	"fmt"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Schema is a compiled JSON Schema ready to validate values.
type Schema struct {
	compiled *jsonschema.Schema
	empty    bool
}

// CompileSchema validates and compiles doc. A nil or empty document compiles
// to a schema with no constraints, so a Workflow may omit either schema.
func CompileSchema(doc map[string]any) (*Schema, error) {
	if len(doc) == 0 {
		return &Schema{empty: true}, nil
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("schema.json", doc); err != nil {
		return nil, fmt.Errorf("schema: %w", err)
	}
	compiled, err := compiler.Compile("schema.json")
	if err != nil {
		return nil, fmt.Errorf("schema: %w", err)
	}
	return &Schema{compiled: compiled}, nil
}

// HasConstraints reports whether the schema imposes any requirements.
func (s *Schema) HasConstraints() bool {
	return s != nil && !s.empty
}

// Validate checks value against the schema, returning a detailed error on
// the first violation. A constraint-free schema accepts anything.
func (s *Schema) Validate(value any) error {
	if s == nil || s.empty {
		return nil
	}
	if err := s.compiled.Validate(value); err != nil {
		return fmt.Errorf("schema: %w", err)
	}
	return nil
}

// requiredSchemaKeys returns the top-level "required" property names of doc.
func requiredSchemaKeys(doc map[string]any) []string {
	raw, _ := doc["required"].([]any)
	keys := make([]string, 0, len(raw))
	for _, entry := range raw {
		if name, ok := entry.(string); ok {
			keys = append(keys, name)
		}
	}
	return keys
}
