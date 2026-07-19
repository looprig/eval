package compare_test

import (
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/looprig/eval"
	"github.com/looprig/eval/compare"
)

func desc(name, rev string) eval.Descriptor {
	return eval.Descriptor{Name: eval.Name(name), Revision: eval.Revision(rev), Method: eval.MethodProgrammatic}
}

func measure(name string, v float64) eval.Measurement {
	return eval.Measurement{Name: eval.Name(name), Value: v, Unit: eval.UnitRatio}
}

func sample(scenario string, trial int, as ...eval.Assessment) eval.SampleReport {
	return eval.SampleReport{ScenarioID: scenario, TrialIndex: trial, Assessments: as}
}

// report builds a fully Report.Validate-clean report from the given samples:
// Compare now validates both inputs, so every fixture must carry a consistent
// Summary and Provenance derived from its samples, not just the raw sample list.
func report(samples ...eval.SampleReport) eval.Report {
	return eval.Report{
		ID:         "r",
		Suite:      eval.Revision("s@1"),
		Target:     eval.Revision("t@1"),
		Samples:    samples,
		Summary:    summaryOf(samples),
		Provenance: provenanceOf(samples),
	}
}

// summaryOf recomputes the minimal Summary the report boundary expects from the
// samples (sample count, target-error count, per-status tally).
func summaryOf(samples []eval.SampleReport) eval.Summary {
	counts := make(map[eval.AssessmentStatus]int)
	targetErrs := 0
	for _, s := range samples {
		if s.TargetErr != nil {
			targetErrs++
		}
		for _, a := range s.Assessments {
			counts[a.Status]++
		}
	}
	return eval.Summary{Samples: len(samples), TargetErrors: targetErrs, Assessments: counts}
}

// provenanceOf assembles a provenance whose evaluator set equals the assessed
// set, in first-seen order, so a valid report passes validateProvenanceConsistency.
// For a valid report each evaluator name maps to exactly one revision, so keying
// by name is sufficient.
func provenanceOf(samples []eval.SampleReport) eval.Provenance {
	var evs []eval.EvaluatorRevision
	seen := make(map[eval.Name]struct{})
	for _, s := range samples {
		for _, a := range s.Assessments {
			if _, ok := seen[a.Evaluator]; ok {
				continue
			}
			seen[a.Evaluator] = struct{}{}
			evs = append(evs, eval.EvaluatorRevision{Name: a.Evaluator, Revision: a.Revision})
		}
	}
	return eval.Provenance{Suite: eval.Revision("s@1"), Target: eval.Revision("t@1"), Evaluators: evs}
}

func findCase(t *testing.T, c compare.Comparison, scenario, evaluator string) compare.CaseComparison {
	t.Helper()
	for _, cc := range c.Cases {
		if cc.Key.ScenarioID == scenario && cc.Key.Evaluator == eval.Name(evaluator) {
			return cc
		}
	}
	t.Fatalf("case (%s,%s) not found in %+v", scenario, evaluator, c.Cases)
	return compare.CaseComparison{}
}

func TestCompareClassifies(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		baseline  eval.Report
		candidate eval.Report
		scenario  string
		evaluator string
		want      compare.CaseClass
	}{
		{
			name:      "added",
			baseline:  report(sample("s1", 0, eval.Pass(desc("e", "1")))),
			candidate: report(sample("s1", 0, eval.Pass(desc("e", "1"))), sample("s2", 0, eval.Pass(desc("e", "1")))),
			scenario:  "s2", evaluator: "e", want: compare.CaseAdded,
		},
		{
			name:      "removed",
			baseline:  report(sample("s1", 0, eval.Pass(desc("e", "1"))), sample("s2", 0, eval.Pass(desc("e", "1")))),
			candidate: report(sample("s1", 0, eval.Pass(desc("e", "1")))),
			scenario:  "s2", evaluator: "e", want: compare.CaseRemoved,
		},
		{
			name:      "changed by distribution",
			baseline:  report(sample("s1", 0, eval.Pass(desc("e", "1"), measure("score", 0.9)))),
			candidate: report(sample("s1", 0, eval.Pass(desc("e", "1"), measure("score", 0.5)))),
			scenario:  "s1", evaluator: "e", want: compare.CaseChanged,
		},
		{
			name:      "failed",
			baseline:  report(sample("s1", 0, eval.Pass(desc("e", "1")))),
			candidate: report(sample("s1", 0, eval.Fail(desc("e", "1"), eval.Finding{Code: eval.FindingCode("x"), Severity: eval.SeverityHigh}))),
			scenario:  "s1", evaluator: "e", want: compare.CaseFailed,
		},
		{
			name:      "errored",
			baseline:  report(sample("s1", 0, eval.Pass(desc("e", "1")))),
			candidate: report(sample("s1", 0, eval.Errored(desc("e", "1")))),
			scenario:  "s1", evaluator: "e", want: compare.CaseErrored,
		},
		{
			name:      "unverified",
			baseline:  report(sample("s1", 0, eval.Pass(desc("e", "1")))),
			candidate: report(sample("s1", 0, eval.Unverified(desc("e", "1")))),
			scenario:  "s1", evaluator: "e", want: compare.CaseUnverified,
		},
		{
			name:      "unchanged",
			baseline:  report(sample("s1", 0, eval.Pass(desc("e", "1"), measure("score", 1)))),
			candidate: report(sample("s1", 0, eval.Pass(desc("e", "1"), measure("score", 1)))),
			scenario:  "s1", evaluator: "e", want: compare.CaseUnchanged,
		},
		{
			name:      "incompatible evaluator revision",
			baseline:  report(sample("s1", 0, eval.Pass(desc("e", "1"), measure("score", 1)))),
			candidate: report(sample("s1", 0, eval.Pass(desc("e", "2"), measure("score", 1)))),
			scenario:  "s1", evaluator: "e", want: compare.CaseIncompatible,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cmp, err := compare.Compare(tt.baseline, tt.candidate)
			if err != nil {
				t.Fatalf("Compare: %v", err)
			}
			cc := findCase(t, cmp, tt.scenario, tt.evaluator)
			if cc.Class != tt.want {
				t.Fatalf("class = %q, want %q (case %+v)", cc.Class, tt.want, cc)
			}
		})
	}
}

func TestCompareDistributionsCompatible(t *testing.T) {
	t.Parallel()

	// Two trials each side, same evaluator identity -> compatible -> distributions.
	baseline := report(
		sample("s1", 0, eval.Pass(desc("e", "1"), measure("score", 0.8))),
		sample("s1", 1, eval.Pass(desc("e", "1"), measure("score", 1.0))),
	)
	candidate := report(
		sample("s1", 0, eval.Pass(desc("e", "1"), measure("score", 0.4))),
		sample("s1", 1, eval.Pass(desc("e", "1"), measure("score", 0.6))),
	)
	cmp, err := compare.Compare(baseline, candidate)
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	cc := findCase(t, cmp, "s1", "e")
	if !cc.Compatible {
		t.Fatalf("expected compatible case, got %+v", cc)
	}
	if len(cc.Distributions) != 1 {
		t.Fatalf("distributions = %d, want 1", len(cc.Distributions))
	}
	d := cc.Distributions[0]
	if d.Name != eval.Name("score") {
		t.Fatalf("distribution name = %q", d.Name)
	}
	if d.Baseline.Count != 2 || d.Candidate.Count != 2 {
		t.Fatalf("counts baseline=%d candidate=%d, want 2/2", d.Baseline.Count, d.Candidate.Count)
	}
	if math.Abs(d.Baseline.Mean-0.9) > 1e-9 {
		t.Fatalf("baseline mean = %v, want 0.9", d.Baseline.Mean)
	}
	if math.Abs(d.Candidate.Mean-0.5) > 1e-9 {
		t.Fatalf("candidate mean = %v, want 0.5", d.Candidate.Mean)
	}
	// Individual trial results are retained so variance is visible.
	if len(cc.Baseline) != 2 || len(cc.Candidate) != 2 {
		t.Fatalf("per-trial detail not retained: baseline=%d candidate=%d", len(cc.Baseline), len(cc.Candidate))
	}
}

func TestCompareIncompatibleNotAveraged(t *testing.T) {
	t.Parallel()

	baseline := report(sample("s1", 0, eval.Pass(desc("e", "1"), measure("score", 1.0))))
	candidate := report(sample("s1", 0, eval.Pass(desc("e", "2"), measure("score", 0.0))))
	cmp, err := compare.Compare(baseline, candidate)
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	cc := findCase(t, cmp, "s1", "e")
	if cc.Compatible {
		t.Fatal("expected incompatible case")
	}
	if len(cc.Distributions) != 0 {
		t.Fatalf("incompatible case must not compare distributions, got %d", len(cc.Distributions))
	}
	if cc.BaselineRevision != eval.Revision("1") || cc.CandidateRevision != eval.Revision("2") {
		t.Fatalf("revisions not surfaced: %q vs %q", cc.BaselineRevision, cc.CandidateRevision)
	}
}

func TestCompareUnitMismatchForcesChanged(t *testing.T) {
	t.Parallel()

	// Same measurement name and equal numeric value, but incompatible units:
	// 1 second is not 1 count. The mismatch must force CaseChanged (never
	// CaseUnchanged) and be surfaced on the delta.
	latency := func(unit eval.Unit, v float64) eval.Measurement {
		return eval.Measurement{Name: eval.Name("latency"), Value: v, Unit: unit}
	}
	baseline := report(sample("s1", 0, eval.Pass(desc("e", "1"), latency(eval.UnitSecond, 1.0))))
	candidate := report(sample("s1", 0, eval.Pass(desc("e", "1"), latency(eval.UnitCount, 1.0))))

	cmp, err := compare.Compare(baseline, candidate)
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	cc := findCase(t, cmp, "s1", "e")
	if cc.Class != compare.CaseChanged {
		t.Fatalf("class = %q, want %q (unit-mismatched measurements must not be unchanged)", cc.Class, compare.CaseChanged)
	}
	if len(cc.Distributions) != 1 {
		t.Fatalf("distributions = %d, want 1", len(cc.Distributions))
	}
	d := cc.Distributions[0]
	if !d.UnitMismatch {
		t.Fatalf("delta must surface the unit mismatch: %+v", d)
	}
	if d.BaselineUnit != eval.UnitSecond || d.CandidateUnit != eval.UnitCount {
		t.Fatalf("delta must expose both units: baseline=%q candidate=%q", d.BaselineUnit, d.CandidateUnit)
	}
}

func TestCompareIntraSideUnitDriftForcesChanged(t *testing.T) {
	t.Parallel()

	// The baseline reports latency consistently in seconds. The candidate reports
	// latency across TWO trials with DIFFERENT units — one second, one count —
	// while the numeric values are equal. The first trial's unit alone must not
	// stand for the whole side: the drift makes the candidate distribution
	// internally incomparable, so the case must be CaseChanged (never Unchanged)
	// and the delta must surface UnitMismatch.
	latency := func(unit eval.Unit, v float64) eval.Measurement {
		return eval.Measurement{Name: eval.Name("latency"), Value: v, Unit: unit}
	}
	baseline := report(
		sample("s1", 0, eval.Pass(desc("e", "1"), latency(eval.UnitSecond, 1.0))),
		sample("s1", 1, eval.Pass(desc("e", "1"), latency(eval.UnitSecond, 1.0))),
	)
	candidate := report(
		sample("s1", 0, eval.Pass(desc("e", "1"), latency(eval.UnitSecond, 1.0))),
		sample("s1", 1, eval.Pass(desc("e", "1"), latency(eval.UnitCount, 1.0))),
	)

	cmp, err := compare.Compare(baseline, candidate)
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	cc := findCase(t, cmp, "s1", "e")
	if cc.Class != compare.CaseChanged {
		t.Fatalf("class = %q, want %q (intra-side unit drift must not be unchanged)", cc.Class, compare.CaseChanged)
	}
	if len(cc.Distributions) != 1 {
		t.Fatalf("distributions = %d, want 1", len(cc.Distributions))
	}
	if !cc.Distributions[0].UnitMismatch {
		t.Fatalf("delta must surface the intra-side unit drift: %+v", cc.Distributions[0])
	}
}

func TestCompareRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	// Compare validates BOTH inputs before indexing. A report that bypasses the
	// decode boundary (hand-built here) can carry a within-sample duplicate
	// evaluator name or a duplicate (ScenarioID, TrialIndex) identity — either of
	// which would silently merge distinct assessments into one case. Compare must
	// reject it as an *InvalidReportError naming the offending side, whose Cause is
	// the report's own typed validation error.
	dupEvaluatorName := func() eval.Report {
		// One sample, two assessments sharing the evaluator name "e" (a pass and a
		// fail). Without validation these collapse into one case and the pass is
		// hidden behind the fail.
		return report(sample("s1", 0,
			eval.Pass(desc("e", "1")),
			eval.Fail(desc("e", "1"), eval.Finding{Code: eval.FindingCode("x"), Severity: eval.SeverityHigh}),
		))
	}
	dupSampleIdentity := func() eval.Report {
		// Two samples sharing (ScenarioID "s1", TrialIndex 0): an ambiguous sample
		// identity the report boundary forbids.
		return report(
			sample("s1", 0, eval.Pass(desc("e", "1"))),
			sample("s1", 0, eval.Pass(desc("e", "1"))),
		)
	}
	overlongScenarioID := func() eval.Report {
		r := report(sample("s1", 0, eval.Pass(desc("e", "1"))))
		r.Samples[0].ScenarioID = strings.Repeat("x", eval.MaxIDBytes+1)
		return r
	}
	missingTarget := func() eval.Report {
		r := report(sample("s1", 0, eval.Pass(desc("e", "1"))))
		r.Target = ""
		r.Provenance.Target = ""
		return r
	}
	invalidReportID := func() eval.Report {
		r := report(sample("s1", 0, eval.Pass(desc("e", "1"))))
		r.ID = string([]byte{0xff})
		return r
	}
	incompleteSampleEvaluators := func() eval.Report {
		r := report(
			sample("s1", 0, eval.Pass(desc("e", "1"))),
			sample("s2", 0, eval.Pass(desc("e", "1")), eval.Pass(desc("judge", "1"))),
		)
		return r
	}
	valid := report(sample("s1", 0, eval.Pass(desc("e", "1"))))

	tests := []struct {
		name           string
		baseline       eval.Report
		candidate      eval.Report
		wantSide       compare.ComparisonSide
		wantReason     string
		wantValidation bool
	}{
		{
			name:       "invalid baseline duplicate evaluator name",
			baseline:   dupEvaluatorName(),
			candidate:  valid,
			wantSide:   compare.SideBaseline,
			wantReason: "duplicate evaluator name within a sample",
		},
		{
			name:       "invalid baseline duplicate sample identity",
			baseline:   dupSampleIdentity(),
			candidate:  valid,
			wantSide:   compare.SideBaseline,
			wantReason: "duplicate sample identity (scenario id and trial index)",
		},
		{
			name:       "invalid candidate duplicate evaluator name",
			baseline:   valid,
			candidate:  dupEvaluatorName(),
			wantSide:   compare.SideCandidate,
			wantReason: "duplicate evaluator name within a sample",
		},
		{
			name:       "invalid candidate duplicate sample identity",
			baseline:   valid,
			candidate:  dupSampleIdentity(),
			wantSide:   compare.SideCandidate,
			wantReason: "duplicate sample identity (scenario id and trial index)",
		},
		{
			name:           "invalid baseline oversize scenario id",
			baseline:       overlongScenarioID(),
			candidate:      valid,
			wantSide:       compare.SideBaseline,
			wantValidation: true,
		},
		{
			name:       "invalid candidate missing observed target",
			baseline:   valid,
			candidate:  missingTarget(),
			wantSide:   compare.SideCandidate,
			wantReason: "successful sample requires an observed target revision",
		},
		{
			name:       "invalid baseline malformed report id",
			baseline:   invalidReportID(),
			candidate:  valid,
			wantSide:   compare.SideBaseline,
			wantReason: "report id must be valid UTF-8",
		},
		{
			name:       "invalid candidate incomplete sample evaluator set",
			baseline:   valid,
			candidate:  incompleteSampleEvaluators(),
			wantSide:   compare.SideCandidate,
			wantReason: "provenance is inconsistent with the report body",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := compare.Compare(tt.baseline, tt.candidate)
			var ire *compare.InvalidReportError
			if !errors.As(err, &ire) {
				t.Fatalf("got %v, want *InvalidReportError", err)
			}
			// The wrapper lets a caller tell baseline from candidate.
			if ire.Side != tt.wantSide {
				t.Fatalf("side = %q, want %q", ire.Side, tt.wantSide)
			}
			// The underlying typed validation error is classifiable via Unwrap.
			if tt.wantReason != "" {
				var rve *eval.ReportValidationError
				if !errors.As(err, &rve) {
					t.Fatalf("cause not a *eval.ReportValidationError: %v", err)
				}
				if rve.Reason != tt.wantReason {
					t.Fatalf("reason = %q, want %q", rve.Reason, tt.wantReason)
				}
			}
			if tt.wantValidation {
				var ve *eval.ValidationError
				if !errors.As(err, &ve) {
					t.Fatalf("cause not a *eval.ValidationError: %v", err)
				}
			}
		})
	}
}

func TestCompareCrossReportRevisionIsIncompatibleNotDrift(t *testing.T) {
	t.Parallel()

	// Baseline has E@v1 and candidate has E@v2 — each report is INTERNALLY
	// consistent (one revision per name). This is a legitimate cross-report
	// revision change and must surface as an incompatible case, never a drift
	// error: the drift rejection is intra-report only.
	baseline := report(sample("s1", 0, eval.Pass(desc("e", "1"))))
	candidate := report(sample("s1", 0, eval.Pass(desc("e", "2"))))
	cmp, err := compare.Compare(baseline, candidate)
	if err != nil {
		t.Fatalf("cross-report revision change must not error: %v", err)
	}
	cc := findCase(t, cmp, "s1", "e")
	if cc.Class != compare.CaseIncompatible {
		t.Fatalf("class = %q, want %q", cc.Class, compare.CaseIncompatible)
	}
}
