package eval

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/looprig/core/content"
)

// This file holds the functional (single-goroutine-observable) tests for the
// execution engine: success, stage-separated target and evaluator errors,
// timeouts, cancellation, trial expansion, preflight, and missing-evidence
// handling. Concurrency, ordering, and input-immutability assertions live in
// run_race_test.go so the whole suite runs under -race.

// runRev is the target revision every stub scenario declares and every stub
// observation reports, so Sample.Validate's revision check passes.
const runRev = Revision("rev-1")

// runScenario builds a minimal valid scenario with the given stable ID.
func runScenario(id string) Scenario {
	return Scenario{
		ID:       id,
		Name:     "target-agent",
		Revision: runRev,
		Input:    userInput("prompt for " + id),
	}
}

// runSuite builds a valid suite over the given scenario IDs.
func runSuite(ids ...string) Suite {
	scenarios := make([]Scenario, 0, len(ids))
	for _, id := range ids {
		scenarios = append(scenarios, runScenario(id))
	}
	return Suite{Name: "smoke", Revision: "suite-v1", Scenarios: scenarios}
}

// runObservation builds a minimal valid observation whose subject revision
// matches runRev. It carries no evidence.
func runObservation() Observation {
	return Observation{
		Scope:   ScopeCase,
		Subject: Subject{ID: "subj-1", Kind: SubjectModel, Name: "target-agent", Revision: runRev},
	}
}

// runObservationWithUsage is a valid observation carrying one usage-evidence
// entry, so a descriptor requiring EvidenceUsage is satisfied.
func runObservationWithUsage() Observation {
	o := runObservation()
	o.Trace.Evidence = []Evidence{
		{ID: "ev-usage", Kind: EvidenceUsage, Usage: &UsageEvidence{Usage: content.Usage{InputTokens: 1}}},
	}
	return o
}

// stubTarget is a Target whose Observe behaviour is supplied by a closure.
type stubTarget struct {
	name    string
	observe func(context.Context, Scenario) (Observation, error)
}

func (s stubTarget) Name() string { return s.name }

func (s stubTarget) Observe(ctx context.Context, sc Scenario) (Observation, error) {
	return s.observe(ctx, sc)
}

// okTarget always returns a fresh valid observation.
func okTarget() stubTarget {
	return stubTarget{name: "ok", observe: func(context.Context, Scenario) (Observation, error) {
		return runObservation(), nil
	}}
}

// stubEvaluator is an Evaluator whose Evaluate behaviour is supplied by a
// closure.
type stubEvaluator struct {
	desc Descriptor
	eval func(context.Context, Sample) (Assessment, error)
}

func (e stubEvaluator) Descriptor() Descriptor { return e.desc }

func (e stubEvaluator) Evaluate(ctx context.Context, s Sample) (Assessment, error) {
	return e.eval(ctx, s)
}

// stubDesc builds a valid descriptor with no evidence requirements.
func stubDesc(name string) Descriptor {
	return Descriptor{Name: Name(name), Revision: "v1", Method: MethodProgrammatic}
}

// passEvaluator always returns a passing assessment.
func passEvaluator(name string) stubEvaluator {
	d := stubDesc(name)
	return stubEvaluator{desc: d, eval: func(context.Context, Sample) (Assessment, error) {
		return Pass(d), nil
	}}
}

// errEvaluator always returns a non-nil evaluator (infrastructure) error.
func errEvaluator(name string) stubEvaluator {
	d := stubDesc(name)
	return stubEvaluator{desc: d, eval: func(context.Context, Sample) (Assessment, error) {
		return Assessment{}, errors.New("judge unreachable")
	}}
}

// invalidEvaluator returns the given (invalid) assessment with a nil error,
// modelling a buggy or hostile evaluator that reports a well-typed but
// ill-formed verdict.
func invalidEvaluator(name string, a Assessment) stubEvaluator {
	return stubEvaluator{desc: stubDesc(name), eval: func(context.Context, Sample) (Assessment, error) {
		return a, nil
	}}
}

func TestRunSuccess(t *testing.T) {
	t.Parallel()
	report, err := Run(context.Background(), RunConfig{}, runSuite("s0"), okTarget(), passEvaluator("q"))
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(report.Samples) != 1 {
		t.Fatalf("got %d samples, want 1", len(report.Samples))
	}
	s := report.Samples[0]
	if s.ScenarioID != "s0" || s.TrialIndex != 0 {
		t.Fatalf("unexpected sample identity: id=%q trial=%d", s.ScenarioID, s.TrialIndex)
	}
	if s.TargetErr != nil {
		t.Fatalf("unexpected target error: %v", s.TargetErr)
	}
	if len(s.Assessments) != 1 || s.Assessments[0].Status != StatusPass {
		t.Fatalf("unexpected assessments: %+v", s.Assessments)
	}
	if report.Target != runRev {
		t.Fatalf("report target revision = %q, want %q", report.Target, runRev)
	}
	if report.Suite != "suite-v1" {
		t.Fatalf("report suite revision = %q, want suite-v1", report.Suite)
	}
	if report.Summary.Assessments[StatusPass] != 1 {
		t.Fatalf("summary pass count = %d, want 1", report.Summary.Assessments[StatusPass])
	}
}

func TestRunTargetErrorIsStageError(t *testing.T) {
	t.Parallel()
	// The target fails only for scenario "bad"; siblings must still be assessed
	// and their assessments retained.
	target := stubTarget{name: "flaky", observe: func(_ context.Context, sc Scenario) (Observation, error) {
		if sc.ID == "bad" {
			return Observation{}, errors.New("backend down")
		}
		return runObservation(), nil
	}}
	report, err := Run(context.Background(), RunConfig{}, runSuite("a", "bad", "z"), target, passEvaluator("q"))
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(report.Samples) != 3 {
		t.Fatalf("got %d samples, want 3", len(report.Samples))
	}
	byID := map[string]SampleReport{}
	for _, s := range report.Samples {
		byID[s.ScenarioID] = s
	}
	bad := byID["bad"]
	if bad.TargetErr == nil {
		t.Fatalf("expected target stage error on 'bad'")
	}
	var te *TargetError
	if !errors.As(error(bad.TargetErr), &te) {
		t.Fatalf("target error not a *TargetError: %v", bad.TargetErr)
	}
	if len(bad.Assessments) != 0 {
		t.Fatalf("target error must skip evaluators, got %d assessments", len(bad.Assessments))
	}
	// A target failure must not masquerade as a failed quality assessment.
	for _, id := range []string{"a", "z"} {
		s := byID[id]
		if s.TargetErr != nil {
			t.Fatalf("sibling %q unexpectedly has target error: %v", id, s.TargetErr)
		}
		if len(s.Assessments) != 1 || s.Assessments[0].Status != StatusPass {
			t.Fatalf("sibling %q lost its assessment: %+v", id, s.Assessments)
		}
	}
	if report.Summary.TargetErrors != 1 {
		t.Fatalf("summary target errors = %d, want 1", report.Summary.TargetErrors)
	}
}

func TestRunEvaluatorErrorBesideSuccess(t *testing.T) {
	t.Parallel()
	// One evaluator errors; its sibling still produces a passing assessment. The
	// error surfaces as an error-status assessment, never a fail.
	report, err := Run(context.Background(), RunConfig{}, runSuite("s0"), okTarget(),
		errEvaluator("broken"), passEvaluator("good"))
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	as := report.Samples[0].Assessments
	if len(as) != 2 {
		t.Fatalf("got %d assessments, want 2", len(as))
	}
	if as[0].Status != StatusError {
		t.Fatalf("first assessment status = %q, want error", as[0].Status)
	}
	if as[0].Evaluator != "broken" {
		t.Fatalf("first assessment evaluator = %q, want broken", as[0].Evaluator)
	}
	if as[1].Status != StatusPass {
		t.Fatalf("sibling assessment status = %q, want pass (not discarded)", as[1].Status)
	}
}

func TestRunInvalidAssessmentIsContained(t *testing.T) {
	t.Parallel()
	// A buggy/hostile evaluator returns a nil error with an ill-formed verdict.
	// The runner must never trust it: it is contained as an evaluator-stage error
	// (fail-secure), the raw invalid verdict never reaches the report, and the
	// sibling's completed assessment is retained.
	tests := []struct {
		name    string
		invalid Assessment
	}{
		{
			name:    "zero value",
			invalid: Assessment{},
		},
		{
			name: "pass carrying critical finding",
			invalid: Assessment{
				Evaluator: "broken", // valid identity, but status is inconsistent
				Revision:  "v1",
				Status:    StatusPass,
				Findings:  []Finding{{Code: "crit", Severity: SeverityCritical, Message: "should not pass"}},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Sanity: the fixture must actually be invalid, else the test proves nothing.
			if err := tt.invalid.Validate(); err == nil {
				t.Fatalf("fixture assessment unexpectedly valid; test would be vacuous")
			}
			report, err := Run(context.Background(), RunConfig{}, runSuite("s0"), okTarget(),
				invalidEvaluator("broken", tt.invalid), passEvaluator("good"))
			if err != nil {
				t.Fatalf("Run returned error: %v", err)
			}
			as := report.Samples[0].Assessments
			if len(as) != 2 {
				t.Fatalf("got %d assessments, want 2", len(as))
			}
			// The invalid verdict is contained as an evaluator-stage error, never echoed.
			if as[0].Status != StatusError {
				t.Fatalf("contained assessment status = %q, want error", as[0].Status)
			}
			if as[0].Evaluator != "broken" {
				t.Fatalf("contained assessment evaluator = %q, want broken", as[0].Evaluator)
			}
			if len(as[0].Findings) != 1 || as[0].Findings[0].Code != FindingEvaluatorInvalidAssessment {
				t.Fatalf("contained assessment findings = %+v, want single %q", as[0].Findings, FindingEvaluatorInvalidAssessment)
			}
			// The raw invalid status must not have leaked through.
			if as[0].Status == tt.invalid.Status && tt.invalid.Status != StatusError {
				t.Fatalf("raw invalid verdict leaked into report: %q", as[0].Status)
			}
			// The sibling's completed assessment is unaffected.
			if as[1].Status != StatusPass || as[1].Evaluator != "good" {
				t.Fatalf("sibling assessment altered: %+v", as[1])
			}
			// Every assessment the runner emits must itself be valid.
			for i, a := range as {
				if verr := a.Validate(); verr != nil {
					t.Fatalf("assessment[%d] emitted by Run is invalid: %v", i, verr)
				}
			}
		})
	}
}

func TestRunEvaluatorIdentityMismatchIsContained(t *testing.T) {
	t.Parallel()
	// A buggy/hostile evaluator returns a well-formed assessment stamped with
	// ANOTHER evaluator's identity. Its own Assessment.Validate passes (Validate
	// has no descriptor to compare against), so the runner must reject the
	// mismatched identity itself: contain it as an evaluator-stage error under the
	// DESCRIPTOR's identity, never let the masqueraded verdict or the wrong
	// identity into the report, and leave the sibling untouched.
	desc := stubDesc("exact/a") // Name exact/a, Revision v1
	tests := []struct {
		name     string
		returned Assessment
	}{
		{
			name:     "wrong name",
			returned: Assessment{Evaluator: "exact/b", Revision: "v1", Status: StatusPass},
		},
		{
			name:     "wrong revision",
			returned: Assessment{Evaluator: "exact/a", Revision: "v2", Status: StatusPass},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Sanity: the returned assessment is itself valid — only its identity is
			// wrong, else the test would prove nothing (it would be caught as a plain
			// invalid assessment instead).
			if err := tt.returned.Validate(); err != nil {
				t.Fatalf("fixture assessment unexpectedly invalid; test would be vacuous: %v", err)
			}
			masq := stubEvaluator{desc: desc, eval: func(context.Context, Sample) (Assessment, error) {
				return tt.returned, nil
			}}
			report, err := Run(context.Background(), RunConfig{}, runSuite("s0"), okTarget(),
				masq, passEvaluator("good"))
			if err != nil {
				t.Fatalf("Run returned error: %v", err)
			}
			as := report.Samples[0].Assessments
			if len(as) != 2 {
				t.Fatalf("got %d assessments, want 2", len(as))
			}
			// Contained as an evaluator-stage error under the DESCRIPTOR's identity.
			if as[0].Status != StatusError {
				t.Fatalf("contained assessment status = %q, want error", as[0].Status)
			}
			if as[0].Evaluator != desc.Name || as[0].Revision != desc.Revision {
				t.Fatalf("contained identity = %q@%q, want %q@%q", as[0].Evaluator, as[0].Revision, desc.Name, desc.Revision)
			}
			if len(as[0].Findings) != 1 || as[0].Findings[0].Code != FindingEvaluatorIdentityMismatch {
				t.Fatalf("contained findings = %+v, want single %q", as[0].Findings, FindingEvaluatorIdentityMismatch)
			}
			// The masqueraded verdict (a pass) must never have reached the report.
			if as[0].Status == StatusPass {
				t.Fatalf("masqueraded verdict leaked into report")
			}
			// The attacker-chosen identity must not be echoed into the finding message.
			for _, f := range as[0].Findings {
				if strings.Contains(f.Message, string(tt.returned.Evaluator)) && string(tt.returned.Evaluator) != string(desc.Name) {
					t.Fatalf("finding message echoed the attacker-chosen name: %q", f.Message)
				}
				if strings.Contains(f.Message, string(tt.returned.Revision)) && string(tt.returned.Revision) != string(desc.Revision) {
					t.Fatalf("finding message echoed the attacker-chosen revision: %q", f.Message)
				}
			}
			// The sibling's completed assessment is unaffected.
			if as[1].Status != StatusPass || as[1].Evaluator != "good" {
				t.Fatalf("sibling assessment altered: %+v", as[1])
			}
			// Every assessment the runner emits must itself be valid.
			for i, a := range as {
				if verr := a.Validate(); verr != nil {
					t.Fatalf("assessment[%d] emitted by Run is invalid: %v", i, verr)
				}
			}
		})
	}
}

func TestRunTargetTimeout(t *testing.T) {
	t.Parallel()
	target := stubTarget{name: "slow", observe: func(ctx context.Context, _ Scenario) (Observation, error) {
		select {
		case <-ctx.Done():
			return Observation{}, ctx.Err()
		case <-time.After(10 * time.Second):
			return runObservation(), nil
		}
	}}
	cfg := RunConfig{TargetTimeout: 20 * time.Millisecond}
	report, err := Run(context.Background(), cfg, runSuite("s0"), target, passEvaluator("q"))
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	s := report.Samples[0]
	if s.TargetErr == nil {
		t.Fatalf("expected target timeout stage error")
	}
	if !errors.Is(s.TargetErr, context.DeadlineExceeded) {
		t.Fatalf("target error does not wrap DeadlineExceeded: %v", s.TargetErr)
	}
	if len(s.Assessments) != 0 {
		t.Fatalf("timed-out target must skip evaluators")
	}
}

func TestRunEvaluatorTimeout(t *testing.T) {
	t.Parallel()
	slow := stubEvaluator{desc: stubDesc("slow"), eval: func(ctx context.Context, _ Sample) (Assessment, error) {
		select {
		case <-ctx.Done():
			return Assessment{}, ctx.Err()
		case <-time.After(10 * time.Second):
			return Pass(stubDesc("slow")), nil
		}
	}}
	cfg := RunConfig{EvaluatorTimeout: 20 * time.Millisecond}
	report, err := Run(context.Background(), cfg, runSuite("s0"), okTarget(), slow, passEvaluator("q"))
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	as := report.Samples[0].Assessments
	if len(as) != 2 {
		t.Fatalf("got %d assessments, want 2", len(as))
	}
	if as[0].Status != StatusError {
		t.Fatalf("timed-out evaluator status = %q, want error", as[0].Status)
	}
	if as[1].Status != StatusPass {
		t.Fatalf("sibling of timed-out evaluator lost: %q", as[1].Status)
	}
}

func TestRunCancelledBeforeStart(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	report, err := Run(ctx, RunConfig{}, runSuite("s0", "s1"), okTarget(), passEvaluator("q"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context.Canceled", err)
	}
	if len(report.Samples) != 0 {
		t.Fatalf("cancelled-before-start must produce no samples, got %d", len(report.Samples))
	}
}

func TestRunCancellationStopsNewWorkAndKeepsCompleted(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	// The first target call cancels the run; sequential execution then stops
	// starting new units. The first sample's assessment must be retained.
	target := stubTarget{name: "self-cancel", observe: func(_ context.Context, sc Scenario) (Observation, error) {
		if sc.ID == "s0" {
			cancel()
		}
		return runObservation(), nil
	}}
	// This evaluator ignores ctx so the first, already-started sample completes.
	d := stubDesc("q")
	ev := stubEvaluator{desc: d, eval: func(context.Context, Sample) (Assessment, error) {
		return Pass(d), nil
	}}
	cfg := RunConfig{Concurrency: 1}
	report, err := Run(ctx, cfg, runSuite("s0", "s1", "s2"), target, ev)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context.Canceled", err)
	}
	if len(report.Samples) != 1 {
		t.Fatalf("got %d samples, want 1 completed before cancel", len(report.Samples))
	}
	if report.Samples[0].ScenarioID != "s0" {
		t.Fatalf("completed sample = %q, want s0", report.Samples[0].ScenarioID)
	}
	if len(report.Samples[0].Assessments) != 1 || report.Samples[0].Assessments[0].Status != StatusPass {
		t.Fatalf("completed assessment not retained: %+v", report.Samples[0].Assessments)
	}
}

func TestRunCancellationAfterCompletionReturnsNilError(t *testing.T) {
	t.Parallel()
	// Cancellation lands during the only unit's evaluation, but the evaluator
	// ignores ctx and completes, so every slot is filled. A run that finished all
	// its work must return a nil error even though ctx is now cancelled, so a
	// caller with the common `if err != nil { discard }` idiom keeps the whole
	// report.
	ctx, cancel := context.WithCancel(context.Background())
	d := stubDesc("q")
	ev := stubEvaluator{desc: d, eval: func(context.Context, Sample) (Assessment, error) {
		cancel() // cancel after all work is effectively done
		return Pass(d), nil
	}}
	report, err := Run(ctx, RunConfig{Concurrency: 1}, runSuite("s0"), okTarget(), ev)
	if err != nil {
		t.Fatalf("completed run must return nil error even when ctx is later cancelled, got %v", err)
	}
	if ctx.Err() == nil {
		t.Fatalf("test precondition failed: ctx should be cancelled by now")
	}
	if len(report.Samples) != 1 {
		t.Fatalf("got %d samples, want 1 (full report)", len(report.Samples))
	}
	if len(report.Samples[0].Assessments) != 1 || report.Samples[0].Assessments[0].Status != StatusPass {
		t.Fatalf("completed assessment not retained: %+v", report.Samples[0].Assessments)
	}
}

func TestRunEmptyEvaluators(t *testing.T) {
	t.Parallel()
	report, err := Run(context.Background(), RunConfig{}, runSuite("s0"), okTarget())
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(report.Samples) != 1 {
		t.Fatalf("got %d samples, want 1", len(report.Samples))
	}
	if len(report.Samples[0].Assessments) != 0 {
		t.Fatalf("empty evaluators must yield no assessments, got %d", len(report.Samples[0].Assessments))
	}
	if report.Samples[0].TargetErr != nil {
		t.Fatalf("unexpected target error: %v", report.Samples[0].TargetErr)
	}
}

func TestRunMissingRequiredEvidenceIsUnverified(t *testing.T) {
	t.Parallel()
	d := Descriptor{Name: "needs-usage", Revision: "v1", Method: MethodProgrammatic, Requires: []EvidenceKind{EvidenceUsage}}
	ev := stubEvaluator{desc: d, eval: func(context.Context, Sample) (Assessment, error) {
		t.Error("Evaluate must not be called when required evidence is missing")
		return Pass(d), nil
	}}
	// okTarget's observation carries no usage evidence.
	report, err := Run(context.Background(), RunConfig{}, runSuite("s0"), okTarget(), ev)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	as := report.Samples[0].Assessments
	if len(as) != 1 || as[0].Status != StatusUnverified {
		t.Fatalf("missing required evidence must be unverified, got %+v", as)
	}
}

func TestRunPresentRequiredEvidenceEvaluates(t *testing.T) {
	t.Parallel()
	d := Descriptor{Name: "needs-usage", Revision: "v1", Method: MethodProgrammatic, Requires: []EvidenceKind{EvidenceUsage}}
	ev := stubEvaluator{desc: d, eval: func(context.Context, Sample) (Assessment, error) {
		return Pass(d), nil
	}}
	target := stubTarget{name: "usage", observe: func(context.Context, Scenario) (Observation, error) {
		return runObservationWithUsage(), nil
	}}
	report, err := Run(context.Background(), RunConfig{}, runSuite("s0"), target, ev)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	as := report.Samples[0].Assessments
	if len(as) != 1 || as[0].Status != StatusPass {
		t.Fatalf("present required evidence should evaluate to pass, got %+v", as)
	}
}

func TestRunDefaultOneTrial(t *testing.T) {
	t.Parallel()
	report, err := Run(context.Background(), RunConfig{}, runSuite("s0", "s1"), okTarget(), passEvaluator("q"))
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(report.Samples) != 2 {
		t.Fatalf("default one trial over 2 scenarios = %d samples, want 2", len(report.Samples))
	}
	for _, s := range report.Samples {
		if s.TrialIndex != 0 {
			t.Fatalf("default trial index = %d, want 0", s.TrialIndex)
		}
	}
}

func TestRunThreeTrials(t *testing.T) {
	t.Parallel()
	cfg := RunConfig{Trials: 3}
	report, err := Run(context.Background(), cfg, runSuite("s0", "s1"), okTarget(), passEvaluator("q"))
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(report.Samples) != 6 {
		t.Fatalf("2 scenarios x 3 trials = %d samples, want 6", len(report.Samples))
	}
	// Stable order: scenario-major, trial-minor.
	want := []struct {
		id    string
		trial int
	}{
		{"s0", 0}, {"s0", 1}, {"s0", 2},
		{"s1", 0}, {"s1", 1}, {"s1", 2},
	}
	for i, w := range want {
		got := report.Samples[i]
		if got.ScenarioID != w.id || got.TrialIndex != w.trial {
			t.Fatalf("sample[%d] = (%q,%d), want (%q,%d)", i, got.ScenarioID, got.TrialIndex, w.id, w.trial)
		}
	}
}

func TestRunFailedTrialBesideSuccessfulTrials(t *testing.T) {
	t.Parallel()
	// Fail only the second trial of s0 via a per-scenario call counter.
	var calls int
	target := stubTarget{name: "trial-flaky", observe: func(_ context.Context, sc Scenario) (Observation, error) {
		calls++
		if sc.ID == "s0" && calls == 2 {
			return Observation{}, errors.New("transient")
		}
		return runObservation(), nil
	}}
	cfg := RunConfig{Trials: 3, Concurrency: 1}
	report, err := Run(context.Background(), cfg, runSuite("s0"), target, passEvaluator("q"))
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(report.Samples) != 3 {
		t.Fatalf("got %d samples, want 3", len(report.Samples))
	}
	failures := 0
	passes := 0
	for _, s := range report.Samples {
		if s.TargetErr != nil {
			failures++
			continue
		}
		if len(s.Assessments) == 1 && s.Assessments[0].Status == StatusPass {
			passes++
		}
	}
	if failures != 1 || passes != 2 {
		t.Fatalf("got %d failed / %d passed trials, want 1 / 2", failures, passes)
	}
}

func TestRunInvalidTrials(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		trials int
	}{
		{"negative", -1},
		{"too large", MaxTrials + 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			report, err := Run(context.Background(), RunConfig{Trials: tt.trials}, runSuite("s0"), okTarget())
			if err == nil {
				t.Fatalf("expected preflight error for trials=%d", tt.trials)
			}
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("error = %v, want *ValidationError", err)
			}
			if len(report.Samples) != 0 {
				t.Fatalf("preflight failure must return zero report, got %d samples", len(report.Samples))
			}
		})
	}
}

func TestRunInvalidConcurrency(t *testing.T) {
	t.Parallel()
	_, err := Run(context.Background(), RunConfig{Concurrency: -1}, runSuite("s0"), okTarget())
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want *ValidationError", err)
	}
}

func TestRunPreflightDuplicateScenarioIDs(t *testing.T) {
	t.Parallel()
	suite := runSuite("dup", "dup")
	report, err := Run(context.Background(), RunConfig{}, suite, okTarget())
	var de *DuplicateScenarioError
	if !errors.As(err, &de) {
		t.Fatalf("error = %v, want *DuplicateScenarioError", err)
	}
	if len(report.Samples) != 0 {
		t.Fatalf("preflight failure must return zero report")
	}
}

func TestRunPreflightNilTarget(t *testing.T) {
	t.Parallel()
	_, err := Run(context.Background(), RunConfig{}, runSuite("s0"), nil, passEvaluator("q"))
	var ne *NilTargetError
	if !errors.As(err, &ne) {
		t.Fatalf("error = %v, want *NilTargetError", err)
	}
}

func TestRunPreflightNilEvaluator(t *testing.T) {
	t.Parallel()
	_, err := Run(context.Background(), RunConfig{}, runSuite("s0"), okTarget(), nil)
	var ne *NilEvaluatorError
	if !errors.As(err, &ne) {
		t.Fatalf("error = %v, want *NilEvaluatorError", err)
	}
}

func TestRunPreflightDuplicateEvaluatorName(t *testing.T) {
	t.Parallel()
	// A second evaluator with the given descriptor, used to build a same-name pair.
	evWith := func(d Descriptor) stubEvaluator {
		return stubEvaluator{desc: d, eval: func(context.Context, Sample) (Assessment, error) {
			return Pass(d), nil
		}}
	}
	tests := []struct {
		name       string
		evaluators []Evaluator
	}{
		{
			name:       "identical evaluators (same name, same revision)",
			evaluators: []Evaluator{passEvaluator("q"), passEvaluator("q")},
		},
		{
			name: "same name, different revision",
			evaluators: []Evaluator{
				evWith(Descriptor{Name: "q", Revision: "v1", Method: MethodProgrammatic}),
				evWith(Descriptor{Name: "q", Revision: "v2", Method: MethodProgrammatic}),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// A target that fails the test if it is ever observed: a duplicate-name
			// evaluator set must be rejected at preflight, before any execution.
			target := stubTarget{name: "must-not-run", observe: func(context.Context, Scenario) (Observation, error) {
				t.Error("Observe called: duplicate-name evaluators must be rejected before execution")
				return runObservation(), nil
			}}
			report, err := Run(context.Background(), RunConfig{}, runSuite("s0"), target, tt.evaluators...)
			var de *DuplicateEvaluatorNameError
			if !errors.As(err, &de) {
				t.Fatalf("error = %v, want *DuplicateEvaluatorNameError", err)
			}
			if len(report.Samples) != 0 {
				t.Fatalf("preflight failure must return zero report, got %d samples", len(report.Samples))
			}
		})
	}
}

// TestRunDistinctEvaluatorNamesReportValidates confirms a normal, distinct-name
// evaluator set still runs and its report passes Report.Validate.
func TestRunDistinctEvaluatorNamesReportValidates(t *testing.T) {
	t.Parallel()
	report, err := Run(context.Background(), RunConfig{}, runSuite("s0", "s1"), okTarget(),
		passEvaluator("q"), passEvaluator("r"))
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if verr := report.Validate(); verr != nil {
		t.Fatalf("distinct-name run report failed Report.Validate: %v", verr)
	}
}

func TestRunPreflightInvalidDescriptor(t *testing.T) {
	t.Parallel()
	bad := stubEvaluator{desc: Descriptor{Name: "", Revision: "v1"}} // empty name
	_, err := Run(context.Background(), RunConfig{}, runSuite("s0"), okTarget(), bad)
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want *ValidationError from descriptor", err)
	}
}

func TestRunPreflightEmptySuite(t *testing.T) {
	t.Parallel()
	_, err := Run(context.Background(), RunConfig{}, Suite{Name: "empty", Revision: "v1"}, okTarget())
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want *ValidationError for empty suite", err)
	}
}

func TestRunClockInjection(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	var n int
	cfg := RunConfig{}
	cfg.now = func() time.Time {
		n++
		if n == 1 {
			return start
		}
		return start.Add(5 * time.Second)
	}
	report, err := Run(context.Background(), cfg, runSuite("s0"), okTarget(), passEvaluator("q"))
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !report.StartedAt.Equal(start) {
		t.Fatalf("StartedAt = %v, want %v", report.StartedAt, start)
	}
	if !report.EndedAt.Equal(start.Add(5 * time.Second)) {
		t.Fatalf("EndedAt = %v, want %v", report.EndedAt, start.Add(5*time.Second))
	}
}
