package dataset

import (
	"bytes"
	"encoding/json"
	"unicode/utf8"

	"github.com/looprig/core/content"
	"github.com/looprig/eval"
)

// This file is the JSON boundary of the dataset codec. It is the only place a
// wire discriminator (the envelope "version" and each message's "role") is
// read, and it narrows to strict domain types immediately. The "any"/RawMessage
// surface never travels deeper than this file.

// version1 is the sole wire version this codec implements. It is read FIRST from
// every record; an unknown or missing version is rejected before the payload is
// trusted (fail-closed).
const version1 = "dataset/v1"

// envelopeJSON is the wire shape of one dataset record: an explicit version
// discriminator plus a deferred scenario payload. Scenario is a RawMessage so
// the version can be checked before the payload is deeply decoded.
type envelopeJSON struct {
	Version  string          `json:"version"`
	Scenario json.RawMessage `json:"scenario"`
}

// scenarioJSON is the wire shape of a Scenario. Input is a slice of raw message
// objects decoded via role discrimination; every other field decodes directly
// into its strict domain type.
type scenarioJSON struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Revision    string            `json:"revision"`
	Input       []json.RawMessage `json:"input"`
	Expectation *eval.Expectation `json:"expectation,omitempty"`
	Labels      []eval.Label      `json:"labels,omitempty"`
}

// DecodeRecord decodes a single JSONL record into a validated scenario. It is
// the untrusted boundary and the fuzz target: for any input it returns either a
// valid scenario or a typed error, and never panics. Enforced in order: the
// per-record size bound, valid UTF-8, exactly one JSON value (no trailing data),
// a known version, a reconstructable conversation, and domain validation.
func DecodeRecord(data []byte) (eval.Scenario, error) {
	return decodeRecordAt(data, 0)
}

// decodeRecordAt is DecodeRecord with a 1-based line number for diagnostics. A
// line of 0 means "no line context" (the standalone DecodeRecord path).
func decodeRecordAt(data []byte, line int) (eval.Scenario, error) {
	sc, err := decodeRecord(data)
	if err != nil {
		return eval.Scenario{}, stampLine(err, line)
	}
	return sc, nil
}

// decodeRecord does the work with line-less errors; decodeRecordAt stamps the
// line onto the returned error.
func decodeRecord(data []byte) (eval.Scenario, error) {
	var zero eval.Scenario

	if len(data) > MaxRecordBytes {
		return zero, &RecordTooLargeError{Size: len(data), Max: MaxRecordBytes}
	}
	if !utf8.Valid(data) {
		return zero, &MalformedRecordError{Reason: reasonInvalidUTF8}
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	var env envelopeJSON
	if err := dec.Decode(&env); err != nil {
		return zero, &MalformedRecordError{Reason: reasonInvalidJSON}
	}
	// A record must be exactly one JSON value: reject any trailing token.
	if dec.More() {
		return zero, &MalformedRecordError{Reason: reasonTrailingData}
	}

	// Version FIRST, before the payload is trusted.
	if env.Version != version1 {
		return zero, &UnknownVersionError{Version: safeVersionToken(env.Version)}
	}
	if len(env.Scenario) == 0 {
		return zero, &MalformedRecordError{Reason: reasonEmptyScenario}
	}

	sc, err := decodeScenario(env.Scenario)
	if err != nil {
		return zero, err
	}
	if err := sc.Validate(); err != nil {
		return zero, &InvalidScenarioError{Cause: err}
	}
	return sc, nil
}

// decodeScenario decodes the deferred scenario payload into a strict
// eval.Scenario, reconstructing the conversation via role discrimination.
func decodeScenario(data json.RawMessage) (eval.Scenario, error) {
	// Strict at the scenario-object boundary: reject unknown scenario-level
	// fields, matching the report codec's payload strictness. This applies ONLY
	// to the scenario object and its directly-decoded domain fields; the message
	// objects in Input are json.RawMessage, so each still decodes through
	// core/content's own UnmarshalJSON (below), untouched by this strictness.
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var w scenarioJSON
	if err := dec.Decode(&w); err != nil {
		if isUnknownField(err) {
			return eval.Scenario{}, &MalformedRecordError{Reason: reasonUnknownField}
		}
		return eval.Scenario{}, &MalformedRecordError{Reason: reasonInvalidJSON}
	}
	if dec.More() {
		return eval.Scenario{}, &MalformedRecordError{Reason: reasonTrailingData}
	}

	var input content.AgenticMessages
	if len(w.Input) > 0 {
		input = make(content.AgenticMessages, 0, len(w.Input))
		for _, raw := range w.Input {
			msg, err := decodeConversation(raw)
			if err != nil {
				return eval.Scenario{}, err
			}
			input = append(input, msg)
		}
	}

	return eval.Scenario{
		ID:          w.ID,
		Name:        eval.Name(w.Name),
		Revision:    eval.Revision(w.Revision),
		Input:       input,
		Expectation: w.Expectation,
		Labels:      w.Labels,
	}, nil
}

// decodeConversation reads a message object's "role" discriminator, allocates
// the matching concrete pointer type, and decodes the same bytes into it via
// that type's UnmarshalJSON. It mirrors the block codec's discriminator pattern
// (core/content/block_json.go). core provides no codec for the interface slice,
// so this discrimination is the codec's responsibility.
func decodeConversation(data json.RawMessage) (content.Conversation, error) {
	var probe struct {
		Role content.Role `json:"role"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil, &MalformedRecordError{Reason: reasonInvalidMessage}
	}
	switch probe.Role {
	case content.RoleUser:
		return decodeMessage[content.UserMessage](data)
	case content.RoleAssistant:
		return decodeMessage[content.AIMessage](data)
	case content.RoleSystem:
		return decodeMessage[content.SystemMessage](data)
	case content.RoleTool:
		return decodeMessage[content.ToolResultMessage](data)
	default:
		return nil, &MalformedRecordError{Reason: reasonUnknownRole}
	}
}

// decodeMessage allocates a fresh *T and decodes data into it through the type's
// own UnmarshalJSON (each of the four message types implements it, directly or
// via the promoted *content.Message method).
func decodeMessage[T any](data []byte) (content.Conversation, error) {
	v := new(T)
	if err := json.Unmarshal(data, v); err != nil {
		return nil, &MalformedRecordError{Reason: reasonInvalidMessage}
	}
	msg, ok := any(v).(content.Conversation)
	if !ok {
		// Unreachable for the four instantiations above; fail closed regardless.
		return nil, &MalformedRecordError{Reason: reasonInvalidMessage}
	}
	return msg, nil
}

// EncodeRecord serializes a scenario to a single JSONL record (no trailing
// newline). It validates the scenario first, so only well-formed records are
// emitted, and enforces the per-record size bound on the result.
func EncodeRecord(sc eval.Scenario) ([]byte, error) {
	if err := sc.Validate(); err != nil {
		return nil, &InvalidScenarioError{Cause: err}
	}
	payload, err := encodeScenario(sc)
	if err != nil {
		return nil, &EncodeError{Cause: err}
	}
	out, err := json.Marshal(envelopeJSON{Version: version1, Scenario: payload})
	if err != nil {
		return nil, &EncodeError{Cause: err}
	}
	if len(out) > MaxRecordBytes {
		return nil, &RecordTooLargeError{Size: len(out), Max: MaxRecordBytes}
	}
	return out, nil
}

// encodeScenario serializes a scenario payload, marshaling each conversation
// message through its concrete MarshalJSON so type-specific fields (AI usage,
// tool-result flags) and nested blocks stay tagged.
func encodeScenario(sc eval.Scenario) (json.RawMessage, error) {
	input := make([]json.RawMessage, len(sc.Input))
	for i, m := range sc.Input {
		if m == nil {
			return nil, &MalformedRecordError{Reason: reasonInvalidMessage}
		}
		b, err := json.Marshal(m)
		if err != nil {
			return nil, err
		}
		input[i] = b
	}
	return json.Marshal(scenarioJSON{
		ID:          sc.ID,
		Name:        string(sc.Name),
		Revision:    string(sc.Revision),
		Input:       input,
		Expectation: sc.Expectation,
		Labels:      sc.Labels,
	})
}

// isUnknownField reports whether a json decode error was an unknown-field
// rejection from DisallowUnknownFields. encoding/json signals this only via the
// error string, so this is the one place a string match is unavoidable; it
// classifies the codec's OWN decoder output, never untrusted content.
func isUnknownField(err error) bool {
	return err != nil && bytes.Contains([]byte(err.Error()), []byte("unknown field"))
}

// safeVersionToken returns a bounded, safe rendering of an unknown version token
// for a diagnostic, or "" when the token is oversized or not valid UTF-8. The
// token can be attacker-supplied, so it is never rendered unbounded.
func safeVersionToken(v string) string {
	if v == "" || len(v) > maxVersionTokenBytes || !utf8.ValidString(v) {
		return ""
	}
	return v
}

// stampLine sets the 1-based line number on a line-bearing error produced by the
// line-less decodeRecord path. Errors without a line field pass through
// unchanged.
func stampLine(err error, line int) error {
	switch e := err.(type) {
	case *MalformedRecordError:
		e.Line = line
	case *UnknownVersionError:
		e.Line = line
	case *RecordTooLargeError:
		e.Line = line
	case *InvalidScenarioError:
		e.Line = line
	case *DuplicateScenarioError:
		e.Line = line
	}
	return err
}
