package eval

import "strconv"

// This file declares the concrete, classifiable error types returned by the
// package's Validate methods. Public failures are typed so callers classify
// them with errors.As, never by matching error strings. Diagnostic text is
// bounded and never embeds untrusted content (conversation text, tool output,
// judge explanations, or externally supplied enum tokens).

// ValidationError reports that a domain value failed validation. Field names
// the domain type or field that failed; Reason is a short, developer-facing
// explanation drawn only from a fixed vocabulary of package constants and
// bounds. Neither field ever contains the offending value, so an untrusted or
// oversized input cannot leak through an error.
type ValidationError struct {
	// Field is the domain type or field name, e.g. "Name" or "Revision".
	Field string
	// Reason is a bounded, safe explanation, e.g. "must not be empty".
	Reason string
}

func (e *ValidationError) Error() string {
	return "eval: invalid " + e.Field + ": " + e.Reason
}

// InvalidEnumError reports that a value of an enumerated type was not a known
// member. Enum is the type name. For integer-backed enums Value holds the
// underlying number, which is always safe to render. For string-backed enums
// the offending token is deliberately withheld, because it may originate from
// untrusted input; Value is left empty and only the type name is reported.
type InvalidEnumError struct {
	// Enum is the enumerated type name, e.g. "Scope" or "AssessmentStatus".
	Enum string
	// Value is a safe rendering of the invalid value (the numeric ordinal for
	// integer enums), or "" when the offending token is withheld.
	Value string
}

func (e *InvalidEnumError) Error() string {
	if e.Value == "" {
		return "eval: unknown " + e.Enum + " value"
	}
	return "eval: unknown " + e.Enum + " value " + e.Value
}

// IndexRangeError reports that an integer index or range lay outside the
// conversation it addresses. Field names the offending domain field; Index is
// the offending index (or range boundary); Len is the conversation length. All
// three are safe integers/constants — no conversation content is embedded.
type IndexRangeError struct {
	// Field is the domain field name, e.g. "MessageRange" or
	// "ConversationExcerpt.MessageIndex".
	Field string
	// Index is the offending index or range boundary.
	Index int
	// Len is the length of the conversation the index must fall within.
	Len int
}

func (e *IndexRangeError) Error() string {
	return "eval: " + e.Field + " index " + strconv.Itoa(e.Index) +
		" out of range [0," + strconv.Itoa(e.Len) + ")"
}

// DuplicateEvidenceError reports that an EvidenceID appeared more than once in a
// trace, which would corrupt evidence reference resolution and comparison. The
// offending identifier is deliberately withheld from the message: EvidenceIDs
// are caller-supplied and a hostile value must not leak through a diagnostic.
type DuplicateEvidenceError struct{}

func (e *DuplicateEvidenceError) Error() string {
	return "eval: duplicate evidence id in trace"
}

// UnknownEvidenceError reports that an EvidenceRef pointed at an EvidenceID that
// no evidence entry in the trace defines. The dangling identifier is withheld
// for the same reason as DuplicateEvidenceError.
type UnknownEvidenceError struct{}

func (e *UnknownEvidenceError) Error() string {
	return "eval: evidence reference to unknown evidence id"
}

// EvidencePayloadError reports that an Evidence value violated the tagged-union
// invariant: it carried no payload, more than one payload, or a payload that did
// not match its Kind. Reason is drawn only from the fixed vocabulary below, so
// no untrusted content is ever embedded.
type EvidencePayloadError struct {
	// Reason is one of the payloadReason* constants.
	Reason string
}

func (e *EvidencePayloadError) Error() string {
	return "eval: evidence payload invalid: " + e.Reason
}

const (
	payloadReasonNone     = "no payload set"
	payloadReasonMultiple = "multiple payloads set"
	payloadReasonMismatch = "payload does not match kind"
)

// DuplicateLabelError reports that a scenario carried two labels with the same
// key, which would make the label set ambiguous. The offending key is withheld
// from the message: label keys are caller-supplied and a hostile value must not
// leak through a diagnostic.
type DuplicateLabelError struct{}

func (e *DuplicateLabelError) Error() string {
	return "eval: duplicate scenario label key"
}

// SampleSubjectMismatchError reports that a sample's observation described a
// subject whose revision did not match the target revision the sample's scenario
// declares. This is a stage error — the target produced an observation for the
// wrong revision — not a failed assessment. Both revisions are withheld from the
// message: the subject revision originates with the target and must not leak
// through a diagnostic.
type SampleSubjectMismatchError struct{}

func (e *SampleSubjectMismatchError) Error() string {
	return "eval: observation subject revision does not match scenario revision"
}
