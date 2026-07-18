package inference

// This file declares the typed, classifiable errors the inference target
// returns. Every distinct failure mode is a concrete struct with an Error()
// method (and Unwrap() when it carries a cause), so callers classify with
// errors.As, never by matching strings. No error echoes untrusted content: not a
// provider's error text, not model output, not conversation text. Only safe,
// fixed classifications appear in a message. A target error is a stage error and
// must never be reported as a failed assessment.

// InferenceError reports that the inference call itself failed: an unreachable
// provider, a transport error, a cancelled context, or an exceeded deadline. The
// underlying error is available via Unwrap — so callers can test for
// context.Canceled or context.DeadlineExceeded with errors.Is — but is never
// rendered, since it may originate outside the process and carry untrusted text.
type InferenceError struct {
	Cause error
}

func (e *InferenceError) Error() string { return "inference target: inference call failed" }

func (e *InferenceError) Unwrap() error { return e.Cause }

// EmptyResponseReason classifies a structurally empty inference response. There
// is no valid zero value: an unclassified empty response is never constructed.
type EmptyResponseReason string

const (
	// ReasonNilResponse: Invoke returned a nil *Response with no error.
	ReasonNilResponse EmptyResponseReason = "nil_response"
	// ReasonNilMessage: the response carried no assistant message.
	ReasonNilMessage EmptyResponseReason = "nil_message"
)

// EmptyResponseError reports that Invoke returned success but no usable result:
// a nil *Response, or a response without an assistant message. The target fails
// secure — it never fabricates an empty observation from a missing result.
// Reason is a closed-enum classification, always safe to render.
type EmptyResponseError struct {
	Reason EmptyResponseReason
}

func (e *EmptyResponseError) Error() string {
	return "inference target: empty inference response: " + string(e.Reason)
}

// IdentityError reports that the target's configured model identity (its derived
// Name, Revision, or subject ID) is not well-formed, so no valid Observation
// could be produced. It is a configuration failure detected before any model is
// called. Cause is the underlying eval validation error — drawn from eval's safe
// vocabulary and carrying no untrusted content — available via Unwrap.
type IdentityError struct {
	Cause error
}

func (e *IdentityError) Error() string { return "inference target: invalid model identity" }

func (e *IdentityError) Unwrap() error { return e.Cause }

// ObservationInvalidError reports that the target assembled an observation that
// failed eval.Observation.Validate. It is a defensive guard: the projection is
// built to be valid, so this signals an internal inconsistency rather than a
// caller error. Cause is the eval validation error (safe vocabulary), available
// via Unwrap.
type ObservationInvalidError struct {
	Cause error
}

func (e *ObservationInvalidError) Error() string {
	return "inference target: assembled observation is invalid"
}

func (e *ObservationInvalidError) Unwrap() error { return e.Cause }
