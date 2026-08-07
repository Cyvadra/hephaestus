package toolkit

import "fmt"

// validateArgs validates args against a JSON-Schema-like map with optional
// "properties", "required", "additionalProperties" keys, plus "minimum" and
// "maximum" numeric bounds on integer/number properties. It only checks
// presence/type/bounds at the top level; it is not a full JSON Schema
// validator.
func validateArgs(schema map[string]any, args map[string]any) error {
	if len(schema) == 0 {
		return nil
	}
	if args == nil {
		args = map[string]any{}
	}

	if err := checkRequired(schema, args); err != nil {
		return err
	}

	propsRaw, ok := schema["properties"]
	if !ok {
		return nil
	}
	props, ok := propsRaw.(map[string]any)
	if !ok {
		return nil
	}

	additional := allowsAdditional(schema)
	for key, val := range args {
		propSchemaRaw, known := props[key]
		if !known {
			if !additional {
				return fmt.Errorf("unexpected property %q", key)
			}
			continue
		}
		propSchema, ok := propSchemaRaw.(map[string]any)
		if !ok {
			continue
		}
		if err := checkType(key, val, propSchema); err != nil {
			return err
		}
	}
	return nil
}

func checkRequired(schema map[string]any, args map[string]any) error {
	reqRaw, ok := schema["required"]
	if !ok {
		return nil
	}

	var required []string
	switch r := reqRaw.(type) {
	case []string:
		required = r
	case []any:
		for _, v := range r {
			if s, ok := v.(string); ok {
				required = append(required, s)
			}
		}
	default:
		return nil
	}

	for _, field := range required {
		if _, present := args[field]; !present {
			return fmt.Errorf("missing required property %q", field)
		}
	}
	return nil
}

func allowsAdditional(schema map[string]any) bool {
	v, ok := schema["additionalProperties"]
	if !ok {
		return true
	}
	b, ok := v.(bool)
	if !ok {
		return true
	}
	return b
}

func checkType(key string, val any, propSchema map[string]any) error {
	typeRaw, ok := propSchema["type"]
	if !ok {
		return nil
	}
	typeName, ok := typeRaw.(string)
	if !ok {
		return nil
	}
	if val == nil {
		return nil
	}

	switch typeName {
	case "string":
		if _, ok := val.(string); !ok {
			return fmt.Errorf("property %q must be a string", key)
		}
	case "boolean":
		if _, ok := val.(bool); !ok {
			return fmt.Errorf("property %q must be a boolean", key)
		}
	case "integer", "number":
		if !isNumber(val) {
			return fmt.Errorf("property %q must be a number", key)
		}
		return checkBounds(key, toFloat(val), propSchema)
	case "array":
		if _, ok := val.([]any); !ok {
			return fmt.Errorf("property %q must be an array", key)
		}
	case "object":
		if _, ok := val.(map[string]any); !ok {
			return fmt.Errorf("property %q must be an object", key)
		}
	}
	return nil
}

// checkBounds enforces optional "minimum"/"maximum" on numeric properties.
func checkBounds(key string, value float64, propSchema map[string]any) error {
	if min, ok := numProp(propSchema, "minimum"); ok && value < min {
		return fmt.Errorf("property %q must be >= %v", key, min)
	}
	if max, ok := numProp(propSchema, "maximum"); ok && value > max {
		return fmt.Errorf("property %q must be <= %v", key, max)
	}
	return nil
}

func numProp(schema map[string]any, name string) (float64, bool) {
	raw, ok := schema[name]
	if !ok {
		return 0, false
	}
	switch v := raw.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	default:
		return 0, false
	}
}

func isNumber(val any) bool {
	switch val.(type) {
	case float64, float32, int, int32, int64, uint, uint32, uint64:
		return true
	default:
		return false
	}
}

func toFloat(val any) float64 {
	switch v := val.(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int32:
		return float64(v)
	case int64:
		return float64(v)
	case uint:
		return float64(v)
	case uint32:
		return float64(v)
	case uint64:
		return float64(v)
	default:
		return 0
	}
}
