package eval

import "context"

// This file declares Target: the single contract for actively executing a
// scenario. A Target turns a Scenario into an Observation — it may wrap an
// inference client, an agent entry point, an HTTP endpoint, or a local process —
// so active execution is available without coupling the evaluator API to any
// particular runtime. The eval root package defines only the interface; concrete
// targets that reach real models or services live in the target/ subpackages
// behind their own dependencies and build tags.

// Target executes a scenario and reports the resulting observation. Name
// identifies the target for provenance and reporting. Observe runs the scenario
// and returns its observation; it takes a context so callers can bound
// execution, and it returns a typed error on failure rather than encoding
// failure as an observation. A target error is a stage error and must never be
// reported as a failed assessment.
type Target interface {
	Name() string
	Observe(context.Context, Scenario) (Observation, error)
}
