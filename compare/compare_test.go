package compare_test

import (
	"errors"
	"math"
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

func report(samples ...eval.SampleReport) eval.Report {
	return eval.Report{ID: "r", Suite: eval.Revision("s@1"), Target: eval.Revision("t@1"), Samples: samples}
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

func TestCompareRejectsNonFinite(t *testing.T) {
	t.Parallel()

	bad := eval.Report{Samples: []eval.SampleReport{
		{ScenarioID: "s1", Assessments: []eval.Assessment{
			{Evaluator: eval.Name("e"), Revision: eval.Revision("1"), Status: eval.StatusPass,
				Measurements: []eval.Measurement{{Name: eval.Name("m"), Value: math.Inf(1), Unit: eval.UnitRatio}}},
		}},
	}}
	_, err := compare.Compare(bad, report())
	var nfe *compare.NonFiniteMeasurementError
	if !errors.As(err, &nfe) {
		t.Fatalf("got %v, want *NonFiniteMeasurementError", err)
	}
}
