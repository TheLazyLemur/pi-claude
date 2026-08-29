package pi

import (
	"testing"
)

type simpleParams struct {
	Path  string `json:"path" desc:"Path relative to the project root"`
	Limit int    `json:"limit,omitempty"`
}

type nestedParams struct {
	Region  regionParams `json:"region"`
	Tags    []string     `json:"tags,omitempty"`
	Verbose *bool        `json:"verbose,omitempty"`
	Ignored string       `json:"-"`
}

type regionParams struct {
	Start int `json:"start" desc:"First line"`
	End   int `json:"end" desc:"Last line"`
}

func TestSchemaOf_Fields(t *testing.T) {
	// given
	// ... a params struct with a described string and an optional int
	var p simpleParams

	// when
	// ... a JSON Schema is derived from it
	got := schemaOf(p)

	// then
	// ... both fields are typed and the description is carried over
	props := got["properties"].(map[string]any)
	path := props["path"].(map[string]any)
	if path["type"] != "string" {
		t.Fatalf("path type = %v, want string", path["type"])
	}
	if path["description"] != "Path relative to the project root" {
		t.Fatalf("path description = %v", path["description"])
	}
	if props["limit"].(map[string]any)["type"] != "integer" {
		t.Fatalf("limit type = %v, want integer", props["limit"])
	}
}

func TestSchemaOf_RequiredOmitsOptional(t *testing.T) {
	// given
	// ... a struct where one field is omitempty
	var p simpleParams

	// when
	// ... a schema is derived
	got := schemaOf(p)

	// then
	// ... only the non-omitempty field is required
	req := got["required"].([]string)
	if len(req) != 1 || req[0] != "path" {
		t.Fatalf("required = %v, want [path]", req)
	}
}

func TestSchemaOf_NestedSliceAndPointer(t *testing.T) {
	// given
	// ... a struct with a nested struct, a slice, a pointer and a skipped field
	var p nestedParams

	// when
	// ... a schema is derived
	got := schemaOf(p)

	// then
	// ... each shape maps to the right JSON Schema node and json:"-" is dropped
	props := got["properties"].(map[string]any)

	region := props["region"].(map[string]any)
	if region["type"] != "object" {
		t.Fatalf("region type = %v, want object", region["type"])
	}
	if region["properties"].(map[string]any)["start"].(map[string]any)["type"] != "integer" {
		t.Fatalf("nested start not an integer: %v", region)
	}

	tags := props["tags"].(map[string]any)
	if tags["type"] != "array" {
		t.Fatalf("tags type = %v, want array", tags["type"])
	}
	if tags["items"].(map[string]any)["type"] != "string" {
		t.Fatalf("tags items = %v, want string", tags["items"])
	}

	if props["verbose"].(map[string]any)["type"] != "boolean" {
		t.Fatalf("verbose type = %v, want boolean", props["verbose"])
	}
	if _, found := props["Ignored"]; found {
		t.Fatal(`field tagged json:"-" leaked into the schema`)
	}

	req := got["required"].([]string)
	if len(req) != 1 || req[0] != "region" {
		t.Fatalf("required = %v, want [region]", req)
	}
}

func TestSchemaOf_NoParams(t *testing.T) {
	// given
	// ... a tool that takes no parameters
	var p NoParams

	// when
	// ... a schema is derived
	got := schemaOf(p)

	// then
	// ... it is a valid empty object schema, not nil
	if got["type"] != "object" {
		t.Fatalf("type = %v, want object", got["type"])
	}
	if len(got["properties"].(map[string]any)) != 0 {
		t.Fatalf("properties = %v, want empty", got["properties"])
	}
	if _, found := got["required"]; found {
		t.Fatal("empty schema should not carry a required list")
	}
}
