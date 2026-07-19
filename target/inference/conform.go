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
	"math/big"
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

// isIntegralNumber reports whether a JSON number has no fractional part. It tests
// integrality EXACTLY on the number's original textual token via math/big, with no
// float64 round-trip: big.Rat represents the token's value with arbitrary
// magnitude and precision, so a genuine fraction (1.5, 1.0000000000000001,
// 9007199254740992.5) is rejected and an integer that overflows float64 (1e309) is
// accepted. It accepts integer forms including exponent forms that resolve to an
// integer (1, 1.0, 1e0, 10e-1). A token that fails to parse fails secure (false).
func isIntegralNumber(raw json.RawMessage) bool {
	rat, ok := numberRat(raw)
	return ok && rat.IsInt()
}

// numberRat parses a JSON number token into an exact big.Rat. It first decodes the
// token as a json.Number (validating it and stripping any surrounding whitespace),
// then parses that literal with big.Rat, which handles arbitrary magnitude, a
// decimal point, and an exponent with no precision loss and no overflow. It
// returns ok=false when the value is not a well-formed JSON number.
func numberRat(raw json.RawMessage) (*big.Rat, bool) {
	var number json.Number
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if decoder.Decode(&number) != nil {
		return nil, false
	}
	return new(big.Rat).SetString(string(number))
}

// enumContains reports whether doc equals one of the enum members. Equality is
// per-kind: two JSON numbers compare by VALUE via big.Rat, so equivalent numeric
// renderings (1 and 1.0) match without a lossy float round-trip, while string,
// boolean, and null members compare structurally (so "a" and "a " differ). A
// member that fails to decode is skipped.
func enumContains(members []json.RawMessage, doc json.RawMessage) bool {
	for _, member := range members {
		if scalarEqual(member, doc) {
			return true
		}
	}
	return false
}

// scalarEqual reports whether two scalar JSON values are equal. Numbers compare by
// exact rational value; every other kind compares structurally. Values of
// differing kinds are never equal.
func scalarEqual(a, b json.RawMessage) bool {
	if jsonKindOf(a) != jsonKindOf(b) {
		return false
	}
	if jsonKindOf(a) == jsonKindNumber {
		ra, oka := numberRat(a)
		rb, okb := numberRat(b)
		return oka && okb && ra.Cmp(rb) == 0
	}
	va, oka := decodeJSONValue(a)
	vb, okb := decodeJSONValue(b)
	return oka && okb && reflect.DeepEqual(va, vb)
}

func decodeJSONValue(raw json.RawMessage) (any, bool) {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return nil, false
	}
	return value, true
}
