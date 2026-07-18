package eval

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
