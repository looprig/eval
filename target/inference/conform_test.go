package inference

import (
	"encoding/json"
	"testing"

	"github.com/looprig/eval"
)

// TestConformsToSchema exercises the document-vs-schema validator directly across
// the portable subset: objects (required/additionalProperties), arrays, scalars,
// enums, and integers, plus fail-secure edge cases.
func TestConformsToSchema(t *testing.T) {
	t.Parallel()

	const objectSchema = `{
		"type":"object",
		"properties":{"answer":{"type":"string"}},
		"required":["answer"],
		"additionalProperties":false
	}`
	const nestedSchema = `{
		"type":"object",
		"properties":{"inner":{"type":"object","properties":{"n":{"type":"number"}},"required":["n"],"additionalProperties":false}},
		"required":["inner"],
		"additionalProperties":false
	}`
	const arrayScalarSchema = `{
		"type":"object",
		"properties":{"tags":{"type":"array","items":{"type":"string"}}},
		"required":["tags"],
		"additionalProperties":false
	}`
	const enumSchema = `{
		"type":"object",
		"properties":{"color":{"type":"string","enum":["red","green"]}},
		"required":["color"],
		"additionalProperties":false
	}`
	const intSchema = `{
		"type":"object",
		"properties":{"count":{"type":"integer"}},
		"required":["count"],
		"additionalProperties":false
	}`
	const boolSchema = `{
		"type":"object",
		"properties":{"ok":{"type":"boolean"}},
		"required":["ok"],
		"additionalProperties":false
	}`

	tests := []struct {
		name       string
		schema     string
		doc        string
		wantOK     bool
		wantReason eval.StructuredErrorReason
	}{
		{"object conforms", objectSchema, `{"answer":"yes"}`, true, conformOK},
		{"object missing required", objectSchema, `{}`, false, eval.StructuredErrorMissingField},
		{"object unknown extra field", objectSchema, `{"answer":"yes","extra":1}`, false, eval.StructuredErrorSchemaMismatch},
		{"object wrong scalar type", objectSchema, `{"answer":123}`, false, eval.StructuredErrorSchemaMismatch},
		{"object doc is not an object", objectSchema, `[1,2]`, false, eval.StructuredErrorSchemaMismatch},

		{"nested object conforms", nestedSchema, `{"inner":{"n":1.5}}`, true, conformOK},
		{"nested object violates", nestedSchema, `{"inner":{"n":"not a number"}}`, false, eval.StructuredErrorSchemaMismatch},
		{"nested object missing inner required", nestedSchema, `{"inner":{}}`, false, eval.StructuredErrorMissingField},

		{"array of scalars conforms", arrayScalarSchema, `{"tags":["a","b"]}`, true, conformOK},
		{"array element violates", arrayScalarSchema, `{"tags":["a",2]}`, false, eval.StructuredErrorSchemaMismatch},
		{"array field is not an array", arrayScalarSchema, `{"tags":"a"}`, false, eval.StructuredErrorSchemaMismatch},

		{"enum member ok", enumSchema, `{"color":"red"}`, true, conformOK},
		{"enum non-member out of range", enumSchema, `{"color":"blue"}`, false, eval.StructuredErrorOutOfRange},

		{"integer whole ok", intSchema, `{"count":1}`, true, conformOK},
		{"integer fractional violates", intSchema, `{"count":1.5}`, false, eval.StructuredErrorSchemaMismatch},

		{"boolean ok", boolSchema, `{"ok":true}`, true, conformOK},
		{"boolean wrong type", boolSchema, `{"ok":"yes"}`, false, eval.StructuredErrorSchemaMismatch},

		// Fail-secure edges: an empty schema, a typeless schema node, and a
		// non-object document against an object schema all deny rather than pass.
		{"empty schema fails secure", ``, `{"answer":"yes"}`, false, eval.StructuredErrorSchemaMismatch},
		{"typeless schema fails secure", `{}`, `{"answer":"yes"}`, false, eval.StructuredErrorSchemaMismatch},
		{"null document against object", objectSchema, `null`, false, eval.StructuredErrorSchemaMismatch},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ok, reason := conformsToSchema(json.RawMessage(tc.schema), json.RawMessage(tc.doc))
			if ok != tc.wantOK {
				t.Fatalf("conformsToSchema ok = %v, want %v (reason %q)", ok, tc.wantOK, reason)
			}
			if reason != tc.wantReason {
				t.Fatalf("conformsToSchema reason = %q, want %q", reason, tc.wantReason)
			}
			// A returned reason must always be a valid closed-enum member.
			if !tc.wantOK {
				if err := reason.Validate(); err != nil {
					t.Fatalf("returned reason %q is not a valid StructuredErrorReason: %v", reason, err)
				}
			}
		})
	}
}
