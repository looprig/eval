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

// EvaluatorRevisionDriftError reports that a SINGLE report carried the same
// evaluator name under two different revisions across its samples. Within one
// report a name must identify exactly one revision; comparison keys a case by
// evaluator name, so a name mapped to two revisions cannot be gathered into one
// case without silently absorbing one revision as a trial of the other.
// Comparison rejects it fail-closed rather than corrupt the case. This is
// distinct from a legitimate cross-report revision change (baseline E@v1 vs
// candidate E@v2, each internally consistent), which surfaces as an incompatible
// case, not this error. No name or revision is embedded — both are report-supplied.
type EvaluatorRevisionDriftError struct{}

func (e *EvaluatorRevisionDriftError) Error() string {
	return "eval/compare: evaluator name maps to more than one revision within a report"
}
