package evaltest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/looprig/core/content"
	"github.com/looprig/eval"
)

// This file holds the evaltest tests. They exercise presentation and assertion
// against a fake recorder that implements the evaltest.TB subset (Helper, Logf,
// Errorf) WITHOUT being a *testing.T, so a failure is recorded as data rather
// than failing the real test. Because the recorder does not expose
// Run(string, func(*testing.T)) bool, evaltest.Run falls back to flat rendering
// against it; the subtest path is exercised separately with a real *testing.T.

// --- fake recorder implementing the evaltest.TB subset ---

// recorder implements TB and records every Helper/Logf/Errorf call so tests can
// assert on rendered output and on whether a failure was signalled, without a
// real test actually failing. It is safe for concurrent use because evaltest may
// present a report from goroutines in the concurrent runner (the default config
// is sequential, but the recorder must not itself introduce a race under -race).
type recorder struct {
	mu      sync.Mutex
	helpers int
	logs    []string
	errs    []string
}

func (r *recorder) Helper() {
	r.mu.Lock()
	r.helpers++
	r.mu.Unlock()
}

func (r *recorder) Logf(format string, args ...any) {
	r.append(&r.logs, format, args...)
}

func (r *recorder) Errorf(format string, args ...any) {
	r.append(&r.errs, format, args...)
}

func (r *recorder) append(dst *[]string, format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	r.mu.Lock()
	*dst = append(*dst, line)
	r.mu.Unlock()
}

// failed reports whether any Errorf was recorded (i.e. the assertion failed).
func (r *recorder) failed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.errs) > 0
}

// errLines returns a copy of the recorded Errorf lines.
func (r *recorder) errLines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.errs...)
}

// all returns every recorded line (logs then errors) so a test can scan the
// complete rendered output for a forbidden substring.
func (r *recorder) all() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.logs)+len(r.errs))
	out = append(out, r.logs...)
	out = append(out, r.errs...)
	return out
}

// recorder must satisfy TB but must NOT satisfy the subtest runner, so Run falls
// back to flat rendering against it.
var _ TB = (*recorder)(nil)

// --- fixtures ---

const testRev = eval.Revision("rev-1")

func userInput(text string) content.AgenticMessages {
	return content.AgenticMessages{
		&content.UserMessage{Message: content.Message{
			Role:   content.RoleUser,
			Blocks: []content.Block{&content.TextBlock{Text: text}},
		}},
	}
}

func testScenario(id string) eval.Scenario {
	return eval.Scenario{ID: id, Name: "agent", Revision: testRev, Input: userInput("prompt " + id)}
}

func testSuite(ids ...string) eval.Suite {
	scenarios := make([]eval.Scenario, 0, len(ids))
	for _, id := range ids {
		scenarios = append(scenarios, testScenario(id))
	}
	return eval.Suite{Name: "suite", Revision: "suite-v1", Scenarios: scenarios}
}

func testObservation() eval.Observation {
	return eval.Observation{
		Scope:   eval.ScopeCase,
		Subject: eval.Subject{ID: "subj", Kind: eval.SubjectModel, Name: "agent", Revision: testRev},
	}
}

func testDesc(name string) eval.Descriptor {
	return eval.Descriptor{Name: eval.Name(name), Revision: "v1", Method: eval.MethodProgrammatic}
}

// fakeTarget returns a fixed observation, or an error when err is set.
type fakeTarget struct {
	obs eval.Observation
	err error
}

func (f fakeTarget) Name() string { return "fake" }

func (f fakeTarget) Observe(context.Context, eval.Scenario) (eval.Observation, error) {
	if f.err != nil {
		return eval.Observation{}, f.err
	}
	return f.obs, nil
}

func okTarget() fakeTarget { return fakeTarget{obs: testObservation()} }

// fakeEvaluator returns a fixed assessment/error.
type fakeEvaluator struct {
	desc eval.Descriptor
	a    eval.Assessment
	err  error
}

func (e fakeEvaluator) Descriptor() eval.Descriptor { return e.desc }

func (e fakeEvaluator) Evaluate(context.Context, eval.Sample) (eval.Assessment, error) {
	return e.a, e.err
}

func passEvaluator(name string) fakeEvaluator {
	d := testDesc(name)
	return fakeEvaluator{desc: d, a: eval.Pass(d, eval.Measurement{Name: "score", Value: 0.9, Unit: eval.UnitRatio})}
}

func failEvaluator(name string, f eval.Finding) fakeEvaluator {
	d := testDesc(name)
	return fakeEvaluator{desc: d, a: eval.Fail(d, f)}
}

// --- report builders for the pure assertion tests (bypass the runner) ---

// sampleWith builds a SampleReport carrying assessments of the given statuses.
func sampleWith(id string, assessments ...eval.Assessment) eval.SampleReport {
	return eval.SampleReport{ScenarioID: id, TrialIndex: 0, Observation: testObservation(), Assessments: assessments}
}

func assessment(name string, status eval.AssessmentStatus) eval.Assessment {
	return eval.Assessment{Evaluator: eval.Name(name), Revision: "v1", Status: status}
}

func reportOf(samples ...eval.SampleReport) eval.Report {
	return eval.Report{ID: "suite@suite-v1", Samples: samples}
}

// --- naming (subtest names) ---

func TestScenarioName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		sample eval.SampleReport
		want   string
	}{
		{name: "id only", sample: eval.SampleReport{ScenarioID: "refund-017"}, want: "refund-017"},
		{name: "trial suffix", sample: eval.SampleReport{ScenarioID: "refund-017", TrialIndex: 2}, want: "refund-017#2"},
		{name: "empty id", sample: eval.SampleReport{ScenarioID: ""}, want: "(unnamed)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := scenarioName(tt.sample); got != tt.want {
				t.Fatalf("scenarioName = %q, want %q", got, tt.want)
			}
		})
	}
}

// --- assessment rendering: pass/fail/error/unverified ---

func TestRenderAssessmentStatuses(t *testing.T) {
	t.Parallel()
	desc := testDesc("relevance")
	tests := []struct {
		name       string
		a          eval.Assessment
		wantSubs   []string
		wantAbsent []string
	}{
		{
			name:     "pass with measurement",
			a:        eval.Pass(desc, eval.Measurement{Name: "score", Value: 0.75, Unit: eval.UnitRatio}),
			wantSubs: []string{"relevance@v1", "status=pass", "score=0.75", "ratio"},
		},
		{
			name:     "fail with finding",
			a:        eval.Fail(desc, eval.Finding{Code: "canary_leak", Severity: eval.SeverityCritical, Message: "leaked"}),
			wantSubs: []string{"status=fail", "canary_leak", "critical"},
			// the evaluator-authored message must never be rendered.
			wantAbsent: []string{"leaked"},
		},
		{
			name:       "error status",
			a:          eval.Errored(desc, eval.Finding{Code: "evaluator_error", Severity: eval.SeverityHigh, Message: "boom"}),
			wantSubs:   []string{"status=error", "evaluator_error", "high"},
			wantAbsent: []string{"boom"},
		},
		{
			name:       "unverified status",
			a:          eval.Unverified(desc, eval.Finding{Code: "missing_required_evidence", Severity: eval.SeverityMedium, Message: "no usage"}),
			wantSubs:   []string{"status=unverified", "missing_required_evidence", "medium"},
			wantAbsent: []string{"no usage"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := renderAssessment(tt.a)
			for _, sub := range tt.wantSubs {
				if !strings.Contains(got, sub) {
					t.Errorf("renderAssessment = %q, missing %q", got, sub)
				}
			}
			for _, sub := range tt.wantAbsent {
				if strings.Contains(got, sub) {
					t.Errorf("renderAssessment = %q, must not contain %q", got, sub)
				}
			}
		})
	}
}

// --- concise evidence rendering: counts and message indexes, never the id ---

func TestRenderAssessmentEvidenceConcise(t *testing.T) {
	t.Parallel()
	desc := testDesc("adherence")
	idx := 3
	a := eval.Assessment{
		Evaluator: desc.Name,
		Revision:  desc.Revision,
		Status:    eval.StatusFail,
		Findings: []eval.Finding{{
			Code:     "forbidden_action",
			Severity: eval.SeverityHigh,
			Evidence: []eval.EvidenceRef{
				{Evidence: "secret-evidence-id"},
				{MessageIndex: &idx},
			},
		}},
	}
	got := renderAssessment(a)
	// A concise reference: the count of refs and the safe integer message index.
	if !strings.Contains(got, "ev=2") {
		t.Errorf("renderAssessment = %q, want evidence count ev=2", got)
	}
	if !strings.Contains(got, "3") {
		t.Errorf("renderAssessment = %q, want message index 3", got)
	}
	// The caller-supplied EvidenceID is withheld (it may be untrusted).
	if strings.Contains(got, "secret-evidence-id") {
		t.Errorf("renderAssessment = %q leaked evidence id", got)
	}
}

// --- deterministic ordering ---

func TestRenderSummaryDeterministic(t *testing.T) {
	t.Parallel()
	s := eval.Summary{
		Samples:      3,
		TargetErrors: 1,
		Assessments: map[eval.AssessmentStatus]int{
			eval.StatusError:      1,
			eval.StatusPass:       2,
			eval.StatusFail:       1,
			eval.StatusUnverified: 1,
		},
	}
	first := renderSummary(s)
	for i := 0; i < 20; i++ {
		if got := renderSummary(s); got != first {
			t.Fatalf("renderSummary not deterministic: %q vs %q", got, first)
		}
	}
	// Fixed severity-independent status order: pass before fail before unverified
	// before error.
	if idxPass, idxErr := strings.Index(first, "pass="), strings.Index(first, "error="); idxPass > idxErr {
		t.Fatalf("summary status order not stable: %q", first)
	}
}

func TestPresentFlatOrdering(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	report := reportOf(
		sampleWith("s0", assessment("a", eval.StatusPass), assessment("b", eval.StatusFail)),
		sampleWith("s1", assessment("a", eval.StatusPass)),
	)
	present(rec, report)
	present(rec, report) // second pass must produce identical trailing output
	half := len(rec.logs) / 2
	if half == 0 {
		t.Fatalf("no output recorded")
	}
	for i := 0; i < half; i++ {
		if rec.logs[i] != rec.logs[half+i] {
			t.Fatalf("present not deterministic at %d: %q vs %q", i, rec.logs[i], rec.logs[half+i])
		}
	}
	// s0 must be rendered before s1.
	joined := strings.Join(rec.logs[:half], "\n")
	if strings.Index(joined, "s0") > strings.Index(joined, "s1") {
		t.Fatalf("sample order not preserved: %q", joined)
	}
}

// --- Run: complete report returned, flat fallback, preflight surfacing ---

func TestRunReturnsCompleteReportOnFailure(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	report := Run(rec, testSuite("s0", "s1"), okTarget(),
		passEvaluator("q"),
		failEvaluator("r", eval.Finding{Code: "bad", Severity: eval.SeverityHigh, Message: "x"}))
	if len(report.Samples) != 2 {
		t.Fatalf("got %d samples, want 2", len(report.Samples))
	}
	// Run itself only presents; it must not signal failure for a failing verdict.
	if rec.failed() {
		t.Fatalf("Run recorded a failure; presentation must not fail: %v", rec.errLines())
	}
	// Each sample carries both evaluators' assessments.
	for _, s := range report.Samples {
		if len(s.Assessments) != 2 {
			t.Fatalf("sample %s: got %d assessments, want 2", s.ScenarioID, len(s.Assessments))
		}
	}
}

func TestRunSurfacesPreflightError(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	// A nil target is rejected at preflight; Run must surface it via Errorf and
	// still return a (zero) report rather than panic.
	report := Run(rec, testSuite("s0"), nil, passEvaluator("q"))
	if !rec.failed() {
		t.Fatalf("Run did not surface the preflight error")
	}
	if len(report.Samples) != 0 {
		t.Fatalf("preflight failure should yield an empty report, got %d samples", len(report.Samples))
	}
	for _, line := range rec.errLines() {
		if !strings.Contains(line, "evaltest") {
			t.Errorf("error line not prefixed by evaltest: %q", line)
		}
	}
}

func TestRunSubtestPathWithRealT(t *testing.T) {
	t.Parallel()
	// A real *testing.T satisfies the subtest runner, so Run emits subtests. Run
	// never fails on a passing verdict, so the real subtests pass here.
	report := Run(t, testSuite("s0", "s1"), okTarget(), passEvaluator("q"))
	if len(report.Samples) != 2 {
		t.Fatalf("got %d samples, want 2", len(report.Samples))
	}
}

func TestRunScenarioWrapsSingleScenario(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	report := RunScenario(rec, testScenario("only"), okTarget(), passEvaluator("q"))
	if len(report.Samples) != 1 {
		t.Fatalf("got %d samples, want 1", len(report.Samples))
	}
	if report.Samples[0].ScenarioID != "only" {
		t.Fatalf("scenario id = %q, want only", report.Samples[0].ScenarioID)
	}
}

// --- RequirePass semantics ---

func TestRequirePass(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		report   eval.Report
		wantFail bool
	}{
		{name: "all pass", report: reportOf(sampleWith("s0", assessment("a", eval.StatusPass))), wantFail: false},
		{name: "skipped allowed", report: reportOf(sampleWith("s0", assessment("a", eval.StatusPass), assessment("b", eval.StatusSkipped))), wantFail: false},
		{name: "fail rejected", report: reportOf(sampleWith("s0", assessment("a", eval.StatusFail))), wantFail: true},
		{name: "unverified rejected", report: reportOf(sampleWith("s0", assessment("a", eval.StatusUnverified))), wantFail: true},
		{name: "error rejected", report: reportOf(sampleWith("s0", assessment("a", eval.StatusError))), wantFail: true},
		{name: "target error rejected", report: reportOf(eval.SampleReport{ScenarioID: "s0", TargetErr: &eval.TargetError{Cause: errors.New("boom")}}), wantFail: true},
		{name: "empty report rejected", report: reportOf(), wantFail: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rec := &recorder{}
			RequirePass(rec, tt.report)
			if rec.failed() != tt.wantFail {
				t.Fatalf("RequirePass failed=%v, want %v (%v)", rec.failed(), tt.wantFail, rec.errLines())
			}
		})
	}
}

// --- RequireVerified semantics ---

func TestRequireVerified(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		report   eval.Report
		wantFail bool
	}{
		{name: "pass accepted", report: reportOf(sampleWith("s0", assessment("a", eval.StatusPass))), wantFail: false},
		{name: "fail accepted (definite verdict)", report: reportOf(sampleWith("s0", assessment("a", eval.StatusFail))), wantFail: false},
		{name: "skipped accepted", report: reportOf(sampleWith("s0", assessment("a", eval.StatusSkipped))), wantFail: false},
		{name: "unverified rejected", report: reportOf(sampleWith("s0", assessment("a", eval.StatusUnverified))), wantFail: true},
		{name: "error rejected", report: reportOf(sampleWith("s0", assessment("a", eval.StatusError))), wantFail: true},
		{name: "target error rejected", report: reportOf(eval.SampleReport{ScenarioID: "s0", TargetErr: &eval.TargetError{Cause: errors.New("boom")}}), wantFail: true},
		{name: "empty report rejected", report: reportOf(), wantFail: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rec := &recorder{}
			RequireVerified(rec, tt.report)
			if rec.failed() != tt.wantFail {
				t.Fatalf("RequireVerified failed=%v, want %v (%v)", rec.failed(), tt.wantFail, rec.errLines())
			}
		})
	}
}

// --- lightweight: a passing assertion records nothing (no profiling) ---

func TestRequirePassIsLightweightOnSuccess(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	RequirePass(rec, reportOf(sampleWith("s0", assessment("a", eval.StatusPass))))
	if rec.failed() {
		t.Fatalf("unexpected failure: %v", rec.errLines())
	}
	rec.mu.Lock()
	logs := len(rec.logs)
	rec.mu.Unlock()
	if logs != 0 {
		t.Fatalf("RequirePass emitted %d log lines on success, want 0", logs)
	}
}

// --- the security guarantee: no secret/raw content in any rendered output ---

const secret = "SUPER-SECRET-CANARY-9f3a-do-not-render"

// secretTarget returns an observation whose conversation and trace evidence both
// embed the canary secret, modelling untrusted content the presenter must never
// echo.
func secretTarget() fakeTarget {
	obs := testObservation()
	obs.Conversation = content.AgenticMessages{
		&content.UserMessage{Message: content.Message{
			Role:   content.RoleUser,
			Blocks: []content.Block{&content.TextBlock{Text: secret}},
		}},
	}
	obs.Trace.Evidence = []eval.Evidence{{
		ID:   "ev-excerpt",
		Kind: eval.EvidenceConversationExcerpt,
		ConversationExcerpt: &eval.ConversationExcerpt{
			MessageIndex: 0,
			Role:         content.RoleUser,
			Redacted:     eval.RedactedExcerpt(secret),
		},
	}}
	return fakeTarget{obs: obs}
}

func TestNoSecretLeakInRenderedOutput(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	// A valid failing verdict whose evaluator-authored message carries the canary.
	failing := failEvaluator("judge", eval.Finding{
		Code:     "canary_leak",
		Severity: eval.SeverityCritical,
		Message:  secret,
	})
	report := Run(rec, testSuite("s0"), secretTarget(), failing)
	RequirePass(rec, report)
	RequireVerified(rec, report)

	if !rec.failed() {
		t.Fatalf("expected the failing verdict to be signalled")
	}
	for _, line := range rec.all() {
		if strings.Contains(line, secret) {
			t.Fatalf("secret leaked into rendered output: %q", line)
		}
	}
	// Sanity: the safe code was rendered, so we know rendering actually ran.
	found := false
	for _, line := range rec.all() {
		if strings.Contains(line, "canary_leak") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("finding code was not rendered; the no-leak assertion is vacuous")
	}
}
