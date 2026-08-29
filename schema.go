package pi

import (
	"reflect"
	"strings"
)

// NoParams is the parameter type for a tool that takes no arguments.
//
//	pi.DefineTool[pi.NoParams]("get_time", "Get the current time", ...)
type NoParams struct{}

// schemaOf derives a JSON Schema object from a Go struct, so a tool's
// parameters are declared once, in Go, instead of twice.
//
// Field names come from the json tag, descriptions from a desc tag. A field is
// required unless it is a pointer or carries omitempty. Fields tagged json:"-"
// and unexported fields are skipped.
func schemaOf(v any) map[string]any {
	return objectSchema(reflect.TypeOf(v))
}

func objectSchema(t reflect.Type) map[string]any {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	schema := map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}
	if t == nil || t.Kind() != reflect.Struct {
		return schema
	}

	props := schema["properties"].(map[string]any)
	var required []string

	for i := range t.NumField() {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}

		name, optional, skip := fieldName(field)
		if skip {
			continue
		}

		node := typeSchema(field.Type)
		if desc := field.Tag.Get("desc"); desc != "" {
			node["description"] = desc
		}
		props[name] = node

		if !optional && field.Type.Kind() != reflect.Pointer {
			required = append(required, name)
		}
	}

	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

// fieldName resolves the JSON name of a field, whether it is optional, and
// whether it should be left out of the schema entirely.
func fieldName(field reflect.StructField) (name string, optional, skip bool) {
	tag := field.Tag.Get("json")
	if tag == "-" {
		return "", false, true
	}

	name = field.Name
	parts := strings.Split(tag, ",")
	if parts[0] != "" {
		name = parts[0]
	}
	for _, opt := range parts[1:] {
		if opt == "omitempty" {
			optional = true
		}
	}
	return name, optional, false
}

func typeSchema(t reflect.Type) map[string]any {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	switch t.Kind() {
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.Slice, reflect.Array:
		return map[string]any{"type": "array", "items": typeSchema(t.Elem())}
	case reflect.Map:
		return map[string]any{"type": "object"}
	case reflect.Struct:
		return objectSchema(t)
	default:
		// Anything the model cannot sensibly fill in is left unconstrained
		// rather than described wrongly.
		return map[string]any{}
	}
}
