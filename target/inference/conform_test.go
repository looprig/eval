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

// TestConformIntegerExact verifies integrality is tested EXACTLY on the original
// number token via math/big, not via a lossy float64 round-trip. High-magnitude
// and high-precision non-integers must VIOLATE (the audit found the old
// float-truncation test admitted them as a false pass), while integer forms that
// overflow float64 (1e309) must CONFORM.
func TestConformIntegerExact(t *testing.T) {
	t.Parallel()

	const intSchema = `{"type":"integer"}`

	tests := []struct {
		name       string
		doc        string
		wantOK     bool
		wantReason eval.StructuredErrorReason
	}{
		{"plain integer", `1`, true, conformOK},
		{"integer with .0", `1.0`, true, conformOK},
		{"integer exponent form", `1e0`, true, conformOK},
		{"exponent resolves to integer", `10e-1`, true, conformOK},
		{"negative zero", `-0`, true, conformOK},
		{"huge integer overflows float64", `1e309`, true, conformOK},

		{"simple fraction", `1.5`, false, eval.StructuredErrorSchemaMismatch},
		{"half", `2.5`, false, eval.StructuredErrorSchemaMismatch},
		// The three audit repro values: each was a FALSE PASS under the old
		// float-truncation test and must now VIOLATE.
		{"audit: explicit .5 at float precision limit", `9007199254740992.5`, false, eval.StructuredErrorSchemaMismatch},
		{"audit: fraction lost to float rounding", `1.0000000000000001`, false, eval.StructuredErrorSchemaMismatch},
		{"audit: value just below one", `0.9999999999999999999`, false, eval.StructuredErrorSchemaMismatch},

		// A non-number value for an integer node is a type mismatch.
		{"string for integer", `"5"`, false, eval.StructuredErrorSchemaMismatch},
		{"bool for integer", `true`, false, eval.StructuredErrorSchemaMismatch},
		{"null for integer", `null`, false, eval.StructuredErrorSchemaMismatch},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ok, reason := conformsToSchema(json.RawMessage(intSchema), json.RawMessage(tc.doc))
			if ok != tc.wantOK {
				t.Fatalf("integer %q: ok = %v, want %v (reason %q)", tc.doc, ok, tc.wantOK, reason)
			}
			if reason != tc.wantReason {
				t.Fatalf("integer %q: reason = %q, want %q", tc.doc, reason, tc.wantReason)
			}
		})
	}
}

// TestConformNumber verifies a number node accepts any JSON number (integral or
// fractional) and rejects non-numeric values.
func TestConformNumber(t *testing.T) {
	t.Parallel()

	const numberSchema = `{"type":"number"}`

	tests := []struct {
		name       string
		doc        string
		wantOK     bool
		wantReason eval.StructuredErrorReason
	}{
		{"fraction", `1.5`, true, conformOK},
		{"integer", `1`, true, conformOK},
		{"exponent", `1e2`, true, conformOK},

		{"bool", `true`, false, eval.StructuredErrorSchemaMismatch},
		{"string", `"5"`, false, eval.StructuredErrorSchemaMismatch},
		{"null", `null`, false, eval.StructuredErrorSchemaMismatch},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ok, reason := conformsToSchema(json.RawMessage(numberSchema), json.RawMessage(tc.doc))
			if ok != tc.wantOK {
				t.Fatalf("number %q: ok = %v, want %v (reason %q)", tc.doc, ok, tc.wantOK, reason)
			}
			if reason != tc.wantReason {
				t.Fatalf("number %q: reason = %q, want %q", tc.doc, reason, tc.wantReason)
			}
		})
	}
}

// TestConformEnumEquality verifies enum membership stays correct: numeric members
// compare by VALUE (1 and 1.0 are equal), while string members compare exactly
// ("a" and "a " differ).
func TestConformEnumEquality(t *testing.T) {
	t.Parallel()

	const numericEnum = `{"type":"number","enum":[1,2]}`
	const stringEnum = `{"type":"string","enum":["a","b"]}`

	tests := []struct {
		name       string
		schema     string
		doc        string
		wantOK     bool
		wantReason eval.StructuredErrorReason
	}{
		{"numeric enum value equality", numericEnum, `1.0`, true, conformOK},
		{"numeric enum member", numericEnum, `2`, true, conformOK},
		{"numeric enum non-member", numericEnum, `3`, false, eval.StructuredErrorOutOfRange},

		{"string enum member", stringEnum, `"a"`, true, conformOK},
		{"string enum exact mismatch on trailing space", stringEnum, `"a "`, false, eval.StructuredErrorOutOfRange},
		{"string enum non-member", stringEnum, `"z"`, false, eval.StructuredErrorOutOfRange},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ok, reason := conformsToSchema(json.RawMessage(tc.schema), json.RawMessage(tc.doc))
			if ok != tc.wantOK {
				t.Fatalf("enum %q: ok = %v, want %v (reason %q)", tc.doc, ok, tc.wantOK, reason)
			}
			if reason != tc.wantReason {
				t.Fatalf("enum %q: reason = %q, want %q", tc.doc, reason, tc.wantReason)
			}
		})
	}
}
