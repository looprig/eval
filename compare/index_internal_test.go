package compare

import (
	"errors"
	"math"
	"testing"

	"github.com/looprig/eval"
)

// These tests exercise index directly. Compare now validates both inputs via
// Report.Validate before indexing, so a non-finite measurement or intra-report
// evaluator revision drift is caught at the report boundary and index's own
// checks are never reached through the public Compare path. Those checks remain
// as defense-in-depth for the per-report indexing, so they are tested here at
// the unexported seam rather than through Compare.

func TestIndexRejectsNonFinite(t *testing.T) {
	t.Parallel()

	for _, v := range []float64{math.Inf(1), math.Inf(-1), math.NaN()} {
		r := eval.Report{Samples: []eval.SampleReport{
			{ScenarioID: "s1", Assessments: []eval.Assessment{
				{Evaluator: eval.Name("e"), Revision: eval.Revision("1"), Status: eval.StatusPass,
					Measurements: []eval.Measurement{{Name: eval.Name("m"), Value: v, Unit: eval.UnitRatio}}},
			}},
		}}
		_, err := index(r)
		var nfe *NonFiniteMeasurementError
		if !errors.As(err, &nfe) {
			t.Fatalf("value %v: got %v, want *NonFiniteMeasurementError", v, err)
		}
	}
}

func TestIndexRejectsIntraReportRevisionDrift(t *testing.T) {
	t.Parallel()

	// A single report whose two samples carry the same (scenario, evaluator name)
	// under different revisions. Gathering them into one case would silently absorb
	// the second revision as a trial of the first; index rejects it fail-closed.
	r := eval.Report{Samples: []eval.SampleReport{
		{ScenarioID: "s1", TrialIndex: 0, Assessments: []eval.Assessment{eval.Pass(eval.Descriptor{Name: "e", Revision: "1", Method: eval.MethodProgrammatic})}},
		{ScenarioID: "s1", TrialIndex: 1, Assessments: []eval.Assessment{eval.Pass(eval.Descriptor{Name: "e", Revision: "2", Method: eval.MethodProgrammatic})}},
	}}
	_, err := index(r)
	var de *EvaluatorRevisionDriftError
	if !errors.As(err, &de) {
		t.Fatalf("got %v, want *EvaluatorRevisionDriftError", err)
	}
}
