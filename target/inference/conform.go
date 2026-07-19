package inference

// This file validates a structured-output JSON DOCUMENT against the bounded
// portable JSON-Schema subset that github.com/looprig/inference already enforces
// on a request's OutputSchema (see that module's output.go schemaValidator).
// The inference module only confirms a structured response is well-formed JSON;
// it never checks the response against the declared schema. The target does that
// here so exact.SchemaResult reports a REAL pass, never a claim it cannot back.
//
// The validator is content-free: it returns only a closed eval reason enum, never
// any byte of the document or schema. It fails secure: an unparsable schema node,
// an unknown type, or a hostile nesting depth is treated as a violation, never a
// pass.

import (
	"bytes"
	"encoding/json"
	"math"
	"reflect"

	"github.com/looprig/eval"
)

// maxConformDepth bounds recursion so a hostile deeply-nested document cannot
// blow the stack. It matches the schema-depth ceiling the inference module
// applies when it validates the schema itself, so any schema that validated there
// nests within this bound and a conforming document cannot exceed it either.
const maxConformDepth = 64

// conformOK is the sentinel reason meaning "no violation". A non-empty reason is
// always one of eval's closed StructuredErrorReason members.
const conformOK eval.StructuredErrorReason = ""

// conformNodeSchema is the portable-subset schema node the document is validated
// against. It mirrors the subset the inference module permits: a type, object
// properties/required (additionalProperties is always false in the subset and is
// enforced structurally here), an array items subschema, and a scalar enum.
// Unknown schema keywords (description, additionalProperties) are ignored — the
// schema already passed the inference validator.
type conformNodeSchema struct {
	Type       string                     `json:"type"`
	Properties map[string]json.RawMessage `json:"properties"`
	Required   []string                   `json:"required"`
	Items      json.RawMessage            `json:"items"`
	Enum       []json.RawMessage          `json:"enum"`
}

// conformsToSchema reports whether doc satisfies the portable-subset schema. On a
// violation it returns (false, reason) with reason drawn only from eval's closed
// StructuredErrorReason vocabulary; on success it returns (true, ""). It never
// returns any part of doc or schema. It is defensive: an unparsable schema node
// is a violation (schema mismatch), never a pass.
func conformsToSchema(schema json.RawMessage, doc json.RawMessage) (bool, eval.StructuredErrorReason) {
	if reason := conformNode(schema, doc, 1); reason != conformOK {
		return false, reason
	}
	return true, conformOK
}

// conformNode validates one document value against one schema node. It returns
// conformOK when the value conforms, or the classified reason otherwise.
func conformNode(schema json.RawMessage, doc json.RawMessage, depth int) eval.StructuredErrorReason {
	if depth > maxConformDepth {
		return eval.StructuredErrorSchemaMismatch
	}
	node, ok := parseConformNode(schema)
	if !ok {
		return eval.StructuredErrorSchemaMismatch
	}
	switch node.Type {
	case "object":
		return conformObject(node, doc, depth)
	case "array":
		return conformArray(node, doc, depth)
	case "string":
		return conformScalar(node, doc, jsonKindString)
	case "boolean":
		return conformScalar(node, doc, jsonKindBool)
	case "number":
		return conformScalar(node, doc, jsonKindNumber)
	case "integer":
		return conformInteger(node, doc)
	default:
		// Absent or unsupported type: fail secure rather than pass.
		return eval.StructuredErrorSchemaMismatch
	}
}

func conformObject(node conformNodeSchema, doc json.RawMessage, depth int) eval.StructuredErrorReason {
	if jsonKindOf(doc) != jsonKindObject {
		return eval.StructuredErrorSchemaMismatch
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(doc, &obj) != nil {
		return eval.StructuredErrorSchemaMismatch
	}
	// additionalProperties:false — every key present in the document must be a
	// declared property.
	for key := range obj {
		if _, declared := node.Properties[key]; !declared {
			return eval.StructuredErrorSchemaMismatch
		}
	}
	// Every required name must be present.
	for _, name := range node.Required {
		if _, present := obj[name]; !present {
			return eval.StructuredErrorMissingField
		}
	}
	// Each present property must conform to its subschema.
	for name, sub := range node.Properties {
		value, present := obj[name]
		if !present {
			continue
		}
		if reason := conformNode(sub, value, depth+1); reason != conformOK {
			return reason
		}
	}
	return conformOK
}

func conformArray(node conformNodeSchema, doc json.RawMessage, depth int) eval.StructuredErrorReason {
	if jsonKindOf(doc) != jsonKindArray {
		return eval.StructuredErrorSchemaMismatch
	}
	var arr []json.RawMessage
	if json.Unmarshal(doc, &arr) != nil {
		return eval.StructuredErrorSchemaMismatch
	}
	// An array node in the subset always carries an items subschema; a missing one
	// is an unvalidatable schema, so fail secure.
	if len(bytes.TrimSpace(node.Items)) == 0 {
		return eval.StructuredErrorSchemaMismatch
	}
	for _, element := range arr {
		if reason := conformNode(node.Items, element, depth+1); reason != conformOK {
			return reason
		}
	}
	return conformOK
}

// conformScalar validates a string, boolean, or number value: the document kind
// must match, and if the node carries an enum the value must equal a member.
func conformScalar(node conformNodeSchema, doc json.RawMessage, want jsonKind) eval.StructuredErrorReason {
	if jsonKindOf(doc) != want {
		return eval.StructuredErrorSchemaMismatch
	}
	return conformEnum(node, doc)
}

// conformInteger validates an integer value: the document must be a JSON number
// with no fractional part, and satisfy any enum.
func conformInteger(node conformNodeSchema, doc json.RawMessage) eval.StructuredErrorReason {
	if jsonKindOf(doc) != jsonKindNumber {
		return eval.StructuredErrorSchemaMismatch
	}
	if !isIntegralNumber(doc) {
		return eval.StructuredErrorSchemaMismatch
	}
	return conformEnum(node, doc)
}

// conformEnum enforces a scalar node's enum: when present, the document value
// must equal one of the enum members. A non-member is out of range.
func conformEnum(node conformNodeSchema, doc json.RawMessage) eval.StructuredErrorReason {
	if len(node.Enum) == 0 {
		return conformOK
	}
	if enumContains(node.Enum, doc) {
		return conformOK
	}
	return eval.StructuredErrorOutOfRange
}

// parseConformNode decodes a schema node. It returns ok=false when the node is
// not a JSON object or cannot be decoded, so callers fail secure.
func parseConformNode(schema json.RawMessage) (conformNodeSchema, bool) {
	if jsonKindOf(schema) != jsonKindObject {
		return conformNodeSchema{}, false
	}
	var node conformNodeSchema
	if json.Unmarshal(schema, &node) != nil {
		return conformNodeSchema{}, false
	}
	return node, true
}

// jsonKind is a coarse classification of a JSON value by its first significant
// byte. The document is already valid, compacted JSON (StructuredResult enforced
// it), and every fragment json.Unmarshal hands back is valid, so the first byte
// unambiguously names the value's kind.
type jsonKind int

const (
	jsonKindInvalid jsonKind = iota
	jsonKindObject
	jsonKindArray
	jsonKindString
	jsonKindNumber
	jsonKindBool
	jsonKindNull
)

func jsonKindOf(raw json.RawMessage) jsonKind {
	for _, b := range raw {
		switch b {
		case ' ', '\t', '\n', '\r':
			continue
		case '{':
			return jsonKindObject
		case '[':
			return jsonKindArray
		case '"':
			return jsonKindString
		case 't', 'f':
			return jsonKindBool
		case 'n':
			return jsonKindNull
		case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
			return jsonKindNumber
		default:
			return jsonKindInvalid
		}
	}
	return jsonKindInvalid
}

// isIntegralNumber reports whether a JSON number has no fractional part. It
// accepts integer forms (including exponent forms that resolve to an integer) and
// rejects a genuine fraction such as 1.5.
func isIntegralNumber(raw json.RawMessage) bool {
	var number json.Number
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if decoder.Decode(&number) != nil {
		return false
	}
	if _, err := number.Int64(); err == nil {
		return true
	}
	value, err := number.Float64()
	if err != nil {
		return false
	}
	return value == math.Trunc(value)
}

// enumContains reports whether doc equals one of the enum members. Both sides are
// decoded to a generic JSON value and compared structurally, so equivalent
// numeric renderings (1 and 1.0) match. A member that fails to decode is skipped.
func enumContains(members []json.RawMessage, doc json.RawMessage) bool {
	want, ok := decodeJSONValue(doc)
	if !ok {
		return false
	}
	for _, member := range members {
		got, ok := decodeJSONValue(member)
		if ok && reflect.DeepEqual(want, got) {
			return true
		}
	}
	return false
}

func decodeJSONValue(raw json.RawMessage) (any, bool) {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return nil, false
	}
	return value, true
}
