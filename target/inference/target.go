// Package inference implements the active-inference eval.Target: it drives a
// scenario's input thread through an inference.Client and projects the model's
// reply into an eval.Observation. It is one of the two eval packages (with
// judge/) permitted to depend on github.com/looprig/inference; the eval root
// package never imports it.
//
// Package-name / import-collision note: the directory is target/inference/, so
// the idiomatic package name (matching the directory) is "inference" — which
// collides with the module github.com/looprig/inference this package must
// import. The collision is resolved by aliasing the MODULE import as llm within
// this package. The public import path stays github.com/looprig/eval/target/
// inference; a caller that also imports the real inference module simply aliases
// one of them (the design's inferenceeval.NewTarget is such a caller alias).
//
// The target is fail-secure. It clones the request template before use so a
// concurrent Observe never mutates the caller's template or races on a shared
// backing array; it appends the read-only scenario input without writing back
// into the scenario; and every path that cannot produce a validated Observation
// returns a typed, content-free error rather than a partial observation. Secrets
// (API keys, auth headers, the template System prompt, raw provider error text)
// never reach the trace, subject, operation attributes, or evidence — only safe
// model identity, token counts, and timings do.
package inference

import (
	"context"
	"time"

	"github.com/looprig/core/content"
	"github.com/looprig/eval"
	llm "github.com/looprig/inference"
)

// target is the concrete inference eval.Target. It holds the client it calls, the
// request template that carries the Model and any System/Tools/sampling defaults,
// the derived safe identity used for the observation's subject and trace, and an
// injectable clock so operation timing is deterministic under test.
type target struct {
	client    llm.Client
	template  llm.Request
	name      eval.Name
	revision  eval.Revision
	subjectID string
	now       func() time.Time
}

// options holds the tunable target configuration set through Option values.
type options struct {
	name      eval.Name
	revision  eval.Revision
	subjectID string
	now       func() time.Time
}

// Option configures a target built by NewTarget.
type Option func(*options)

// WithName overrides the target's stable name (used for Name() and the
// observation subject). It defaults to the template model's wire id.
func WithName(name eval.Name) Option {
	return func(o *options) { o.name = name }
}

// WithRevision overrides the model revision recorded on the subject and trace. It
// defaults to the template model's wire id. A caller aligning the observation
// with a scenario's qualified revision sets this so Sample.Validate accepts it.
func WithRevision(rev eval.Revision) Option {
	return func(o *options) { o.revision = rev }
}

// WithSubjectID sets the subject's correlation identifier. It defaults to the
// target name, which is a stable, deterministic value.
func WithSubjectID(id string) Option {
	return func(o *options) { o.subjectID = id }
}

// WithClock injects the clock used to timestamp the inference operation. It
// defaults to time.Now; tests pin it to assert exact timing.
func WithClock(now func() time.Time) Option {
	return func(o *options) { o.now = now }
}

// NewTarget returns an inference eval.Target. The template supplies the Model and
// any System, Tools, or sampling defaults; Observe fills the request's Messages
// with the template messages followed by the scenario input on each call. By
// default the target's Name, subject Name, and subject/trace Revision are derived
// from the template model's wire id; options override each.
//
// NewTarget does not reject a misconfigured template: an invalid derived identity
// surfaces as a typed IdentityError from Observe (before any model is called), so
// a construction-time mistake is reported by the first Observe rather than
// panicking at construction. The client must be non-nil.
func NewTarget(client llm.Client, template llm.Request, opts ...Option) eval.Target {
	def := eval.Name(template.Model.Name)
	cfg := options{
		name:     def,
		revision: eval.Revision(template.Model.Name),
		now:      time.Now,
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.subjectID == "" {
		cfg.subjectID = string(cfg.name)
	}
	return &target{
		client:    client,
		template:  template,
		name:      cfg.name,
		revision:  cfg.revision,
		subjectID: cfg.subjectID,
		now:       cfg.now,
	}
}

// Name returns the target's stable name for provenance and reporting.
func (t *target) Name() string { return string(t.name) }

// Observe drives the scenario's input through the inference client and projects
// the reply into a validated Observation. The scenario is read-only: Observe
// copies its input message references into a freshly allocated request and never
// writes back into the scenario. On any failure it returns a typed, content-free
// error and no observation.
func (t *target) Observe(ctx context.Context, sc eval.Scenario) (eval.Observation, error) {
	// Validate the derived identity before spending a model call on a
	// misconfigured target; fail fast and fail secure.
	if err := t.validateIdentity(); err != nil {
		return eval.Observation{}, &IdentityError{Cause: err}
	}

	req := cloneRequest(t.template, sc.Input)

	start := t.now()
	resp, err := t.client.Invoke(ctx, req)
	end := t.now()
	if err != nil {
		return eval.Observation{}, &InferenceError{Cause: err}
	}
	if resp == nil {
		return eval.Observation{}, &EmptyResponseError{Reason: ReasonNilResponse}
	}
	if resp.Message == nil {
		return eval.Observation{}, &EmptyResponseError{Reason: ReasonNilMessage}
	}

	obs := t.project(sc.Input, resp, start, end)
	if err := obs.Validate(); err != nil {
		return eval.Observation{}, &ObservationInvalidError{Cause: err}
	}
	return obs, nil
}

// validateIdentity checks that the derived subject/model identity is well-formed,
// so the projected observation is guaranteed to carry a valid, non-empty subject.
func (t *target) validateIdentity() error {
	if err := t.name.Validate(); err != nil {
		return err
	}
	if err := t.revision.Validate(); err != nil {
		return err
	}
	sub := eval.Subject{ID: t.subjectID, Kind: eval.SubjectModel, Name: t.name, Revision: t.revision}
	return sub.Validate()
}

// cloneRequest returns an independent request whose Messages are the template
// messages followed by the scenario input, allocated in a fresh backing array so
// appending never mutates the caller's template.Messages or scenario input, and
// concurrent Observe calls never race on a shared backing. Tools are copied into
// a fresh slice for the same reason; all other fields (Model, System, Output,
// ToolChoice, Override) are read-only and safely shared.
func cloneRequest(tmpl llm.Request, input content.AgenticMessages) llm.Request {
	req := tmpl // value copy of the header; slices/pointers still alias until replaced

	msgs := make(content.AgenticMessages, 0, len(tmpl.Messages)+len(input))
	msgs = append(msgs, tmpl.Messages...)
	msgs = append(msgs, input...)
	req.Messages = msgs

	if len(tmpl.Tools) > 0 {
		tools := make([]llm.Tool, len(tmpl.Tools))
		copy(tools, tmpl.Tools)
		req.Tools = tools
	}
	return req
}
