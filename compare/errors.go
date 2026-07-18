package compare

// This file declares the concrete, classifiable error types the comparison
// returns. Every distinct failure mode is a typed struct so callers classify
// with errors.As, never by matching strings. Diagnostics are content-free: the
// comparison operates over safe report fields and never embeds report content.

// NonFiniteMeasurementError reports that a report carried a measurement whose
// value was NaN or ±Inf. A well-formed report never contains one (the runner
// validates measurements at its boundary); comparison rejects it fail-closed
// rather than propagate a poisoned mean or min/max. No value is embedded — it is
// not finite and not safe to render as a number.
type NonFiniteMeasurementError struct{}

func (e *NonFiniteMeasurementError) Error() string {
	return "eval/compare: measurement value must be finite"
}
