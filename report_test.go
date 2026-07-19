package eval

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// This file tests Report.Validate: the report-level invariant check applied at
// the untrusted decode boundary and confirmed against the runner's own output.
// Every case is a mutation of one consistent baseline so exactly one invariant is
// under test at a time.

// validReport builds a consistent, runner-shaped report whose Summary and
// Provenance agree with its samples.
func validReport() Report {
	samples := []SampleReport{
		{
			ScenarioID:  "s0",
			TrialIndex:  0,
			Observation: runObservation(),
			Assessments: []Assessment{Pass(stubDesc("q"))},
		},
	}
	return Report{
		ID:         "smoke@suite-v1",
		Suite:      "suite-v1",
		Target:     runRev,
		StartedAt:  time.Unix(1000, 0).UTC(),
		EndedAt:    time.Unix(2000, 0).UTC(),
		Samples:    samples,
		Summary:    summarize(samples),
		Provenance: Provenance{Suite: "suite-v1", Target: runRev, Evaluators: []EvaluatorRevision{{Name: "q", Revision: "v1"}}},
	}
}

func TestReportValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mutate  func(*Report)
		wantErr bool
		// wantReason, when set, is the ReportValidationError.Reason expected.
		wantReason string
	}{
		{name: "valid baseline", mutate: func(*Report) {}, wantErr: false},
		{name: "empty id", mutate: func(r *Report) { r.ID = "" }, wantErr: true, wantReason: reportReasonEmptyID},
		{name: "oversize id", mutate: func(r *Report) { r.ID = strings.Repeat("x", MaxReportIDBytes+1) }, wantErr: true, wantReason: reportReasonIDTooLong},
		{name: "zero timestamps allowed", mutate: func(r *Report) { r.StartedAt = time.Time{}; r.EndedAt = time.Time{} }, wantErr: false},
		{name: "empty target allowed", mutate: func(r *Report) { r.Target = ""; r.Provenance.Target = "" }, wantErr: false},
		{name: "ended before started", mutate: func(r *Report) { r.EndedAt = r.StartedAt.Add(-time.Second) }, wantErr: true, wantReason: reportReasonEndBeforeStart},
		{name: "negative trial index", mutate: func(r *Report) { r.Samples[0].TrialIndex = -1 }, wantErr: true, wantReason: reportReasonNegativeTrial},
		{
			name: "duplicate sample identity",
			mutate: func(r *Report) {
				r.Samples = append(r.Samples, r.Samples[0])
			},
			wantErr:    true,
			wantReason: reportReasonDuplicateSample,
		},
		{
			name: "duplicate evaluator within sample",
			mutate: func(r *Report) {
				r.Samples[0].Assessments = append(r.Samples[0].Assessments, Pass(stubDesc("q")))
			},
			wantErr:    true,
			wantReason: reportReasonDuplicateEvaluator,
		},
		{
			// A sample that both errored at the target stage AND carries assessments
			// is contradictory (a target error skips assessment) and must be rejected
			// at the boundary. validateSamples fires before the summary check.
			name: "target error with assessments",
			mutate: func(r *Report) {
				r.Samples[0].TargetErr = &TargetError{Cause: errors.New("boom")}
			},
			wantErr:    true,
			wantReason: reportReasonTargetErrorWithAssessments,
		},
		{
			name:       "summary sample count mismatch",
			mutate:     func(r *Report) { r.Summary.Samples = 99 },
			wantErr:    true,
			wantReason: reportReasonSummaryMismatch,
		},
		{
			name:       "summary status count mismatch",
			mutate:     func(r *Report) { r.Summary.Assessments = map[AssessmentStatus]int{StatusFail: 1} },
			wantErr:    true,
			wantReason: reportReasonSummaryMismatch,
		},
		{
			name:    "invalid contained assessment",
			mutate:  func(r *Report) { r.Samples[0].Assessments[0].Status = AssessmentStatus("bogus") },
			wantErr: true,
		},
		{
			name:    "invalid provenance evaluator revision",
			mutate:  func(r *Report) { r.Provenance.Evaluators = []EvaluatorRevision{{Name: "q", Revision: ""}} },
			wantErr: true,
		},
		{
			name:       "provenance suite contradicts report suite",
			mutate:     func(r *Report) { r.Provenance.Suite = "other-suite" },
			wantErr:    true,
			wantReason: reportReasonProvenanceMismatch,
		},
		{
			name:       "provenance target contradicts report target",
			mutate:     func(r *Report) { r.Provenance.Target = "other-target" },
			wantErr:    true,
			wantReason: reportReasonProvenanceMismatch,
		},
		{
			// A phantom evaluator declared in provenance but never assessed (body is
			// non-empty) is a contradiction.
			name: "provenance declares an evaluator absent from the body",
			mutate: func(r *Report) {
				r.Provenance.Evaluators = []EvaluatorRevision{{Name: "q", Revision: "v1"}, {Name: "phantom", Revision: "v1"}}
			},
			wantErr:    true,
			wantReason: reportReasonProvenanceMismatch,
		},
		{
			// An assessed evaluator absent from provenance (and provenance carrying a
			// different identity entirely) is a contradiction in both directions.
			name:       "provenance evaluator set differs from the assessed set",
			mutate:     func(r *Report) { r.Provenance.Evaluators = []EvaluatorRevision{{Name: "z", Revision: "v9"}} },
			wantErr:    true,
			wantReason: reportReasonProvenanceMismatch,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := validReport()
			tt.mutate(&r)
			err := r.Validate()
			if tt.wantErr != (err != nil) {
				t.Fatalf("Validate() err = %v, wantErr = %v", err, tt.wantErr)
			}
			if tt.wantReason != "" {
				var rve *ReportValidationError
				if !errors.As(err, &rve) {
					t.Fatalf("error not a *ReportValidationError: %v", err)
				}
				if rve.Reason != tt.wantReason {
					t.Fatalf("reason = %q, want %q", rve.Reason, tt.wantReason)
				}
			}
		})
	}
}

// TestRunReportValidates confirms that a real Run's output passes Report.Validate
// — the runner must never emit a report its own validator rejects.
func TestRunReportValidates(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		suite      Suite
		target     Target
		evaluators []Evaluator
	}{
		{
			name:       "all pass",
			suite:      runSuite("a", "b"),
			target:     okTarget(),
			evaluators: []Evaluator{passEvaluator("q"), passEvaluator("r")},
		},
		{
			name:  "with target error (empty observed revision)",
			suite: runSuite("a", "b"),
			target: stubTarget{name: "flaky", observe: func(_ context.Context, sc Scenario) (Observation, error) {
				return Observation{}, errors.New("down")
			}},
			evaluators: []Evaluator{passEvaluator("q")},
		},
		{
			name:       "no evaluators",
			suite:      runSuite("a"),
			target:     okTarget(),
			evaluators: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			report, err := Run(context.Background(), RunConfig{}, tt.suite, tt.target, tt.evaluators...)
			if err != nil {
				t.Fatalf("Run returned error: %v", err)
			}
			if verr := report.Validate(); verr != nil {
				t.Fatalf("runner report failed Report.Validate: %v", verr)
			}
		})
	}
}
