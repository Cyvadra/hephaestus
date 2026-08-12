package registry

import (
	"testing"
)

func TestCompileSchema_EmptyHasNoConstraints(t *testing.T) {
	s, err := CompileSchema(nil)
	if err != nil {
		t.Fatalf("CompileSchema(nil): %v", err)
	}
	if s.HasConstraints() {
		t.Fatal("empty schema should have no constraints")
	}
	if err := s.Validate(map[string]any{}); err != nil {
		t.Fatalf("Validate on empty schema: %v", err)
	}
}

func TestCompileSchema_RejectsMalformedSchema(t *testing.T) {
	if _, err := CompileSchema(map[string]any{"type": "not-a-json-type"}); err == nil {
		t.Fatal("expected error for invalid schema type")
	}
}

func TestCompileSchema_ValidatesValues(t *testing.T) {
	s, err := CompileSchema(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"topic": map[string]any{"type": "string"},
		},
		"required": []any{"topic"},
	})
	if err != nil {
		t.Fatalf("CompileSchema: %v", err)
	}
	if !s.HasConstraints() {
		t.Fatal("schema with required should have constraints")
	}
	if err := s.Validate(map[string]any{"topic": "hi"}); err != nil {
		t.Fatalf("expected valid input: %v", err)
	}
	if err := s.Validate(map[string]any{}); err == nil {
		t.Fatal("expected missing required key to fail")
	}
	if err := s.Validate(map[string]any{"topic": 42}); err == nil {
		t.Fatal("expected type mismatch to fail")
	}
}

func TestRequiredSchemaKeys(t *testing.T) {
	keys := requiredSchemaKeys(map[string]any{"required": []any{"a", "b"}})
	if len(keys) != 2 || keys[0] != "a" || keys[1] != "b" {
		t.Fatalf("unexpected required keys: %v", keys)
	}
	if got := requiredSchemaKeys(map[string]any{}); len(got) != 0 {
		t.Fatalf("expected no required keys, got %v", got)
	}
}
