package llm

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestSanitizeSchemaForGemini(t *testing.T) {
	in := map[string]any{
		"$schema":              "https://json-schema.org/draft/2020-12/schema",
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"name":    map[string]any{"type": []any{"string", "null"}},
			"age":     map[string]any{"type": "integer"},
			"email":   map[string]any{"type": "string", "format": "email"},
			"created": map[string]any{"type": "string", "format": "date-time"},
			"untyped": map[string]any{"description": "no type given"},
			"tags": map[string]any{
				"type":  "array",
				"items": map[string]any{"type": []any{"string", "null"}, "format": "uri"},
			},
			"nested": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"city": map[string]any{"type": []any{"string", "null"}},
					"zip":  map[string]any{"$ref": "#/definitions/zip"},
				},
			},
		},
	}

	// Snapshot the input so we can verify the function is pure.
	inSnapshot, _ := json.Marshal(in)

	got := sanitizeSchemaForGemini("demo_tool", in)

	postSnapshot, _ := json.Marshal(in)
	if string(inSnapshot) != string(postSnapshot) {
		t.Fatalf("sanitize mutated input")
	}

	props := got["properties"].(map[string]any)

	// type: [T, null] → type:T + nullable:true
	name := props["name"].(map[string]any)
	if name["type"] != "string" {
		t.Errorf("name.type: want string, got %v", name["type"])
	}
	if name["nullable"] != true {
		t.Errorf("name.nullable: want true, got %v", name["nullable"])
	}

	// untyped property defaults to "string"
	untyped := props["untyped"].(map[string]any)
	if untyped["type"] != "string" {
		t.Errorf("untyped.type: want default string, got %v", untyped["type"])
	}

	// unsupported "format" dropped
	email := props["email"].(map[string]any)
	if _, ok := email["format"]; ok {
		t.Errorf("email.format should be removed, got %v", email["format"])
	}

	// supported "format" kept
	created := props["created"].(map[string]any)
	if created["format"] != "date-time" {
		t.Errorf("created.format: want date-time, got %v", created["format"])
	}

	// array items recursed: type union + bad format
	tags := props["tags"].(map[string]any)
	tagItems := tags["items"].(map[string]any)
	if tagItems["type"] != "string" || tagItems["nullable"] != true {
		t.Errorf("tags.items: want string+nullable, got %+v", tagItems)
	}
	if _, ok := tagItems["format"]; ok {
		t.Errorf("tags.items.format should be removed")
	}

	// nested properties recursed
	nested := props["nested"].(map[string]any)
	nestedProps := nested["properties"].(map[string]any)
	city := nestedProps["city"].(map[string]any)
	if city["type"] != "string" || city["nullable"] != true {
		t.Errorf("nested.city: want string+nullable, got %+v", city)
	}
	zip := nestedProps["zip"].(map[string]any)
	if _, ok := zip["$ref"]; ok {
		t.Errorf("nested.zip.$ref should be removed")
	}

	// top-level unsupported keys gone
	for _, k := range []string{"$schema", "additionalProperties"} {
		if _, ok := got[k]; ok {
			t.Errorf("top-level %q should be removed", k)
		}
	}
}

func TestSanitizeSchemaForGemini_NormalizeType(t *testing.T) {
	cases := []struct {
		in           any
		wantType     any
		wantNullable bool
	}{
		{"string", "string", false},
		{[]any{"string", "null"}, "string", true},
		{[]any{"null", "integer"}, "integer", true},
		{[]any{"null"}, "string", true}, // empty after stripping null
		{[]any{}, "string", false},
	}
	for _, c := range cases {
		got, nullable, _ := normalizeSchemaType(c.in)
		if !reflect.DeepEqual(got, c.wantType) || nullable != c.wantNullable {
			t.Errorf("normalizeSchemaType(%v) = (%v, %v); want (%v, %v)", c.in, got, nullable, c.wantType, c.wantNullable)
		}
	}
}