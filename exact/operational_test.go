package exact

import (
	"math"
	"testing"
	"time"

	"github.com/looprig/core/content"
	"github.com/looprig/eval"
)

// singleMeasurement returns the sole measurement of a, failing otherwise.
func singleMeasurement(t *testing.T, a eval.Assessment) eval.Measurement {
	t.Helper()
	if len(a.Measurements) != 1 {
		t.Fatalf("expected exactly one measurement, got %d", len(a.Measurements))
	}
	return a.Measurements[0]
}

func TestToolErrorRate(t *testing.T) {
	t.Parallel()

	conv := content.AgenticMessages{aiText("working")}

	tests := []struct {
		name       string
		opts       []RateOption
		evidence   []eval.Evidence
		wantStatus eval.AssessmentStatus
		wantRate   float64 // checked only when a measurement is present
		wantMeas   bool
	}{
		{
			name:       "no tool-operation evidence is unverified",
			evidence:   nil,
			wantStatus: eval.StatusUnverified,
		},
		{
			name: "half errored, no threshold, passes with measurement",
			evidence: []eval.Evidence{
				toolOpEv("to-1", "lookup_account", false),
				toolOpEv("to-2", "issue_refund", true),
			},
			wantStatus: eval.StatusPass,
			wantRate:   0.5,
			wantMeas:   true,
		},
		{
			name: "rate exceeds threshold, fails with measurement and evidence",
			opts: []RateOption{MaxErrorRate(0.4)},
			evidence: []eval.Evidence{
				toolOpEv("to-1", "lookup_account", false),
				toolOpEv("to-2", "issue_refund", true),
			},
			wantStatus: eval.StatusFail,
			wantRate:   0.5,
			wantMeas:   true,
		},
		{
			name: "rate equal to threshold does not fail",
			opts: []RateOption{MaxErrorRate(0.5)},
			evidence: []eval.Evidence{
				toolOpEv("to-1", "lookup_account", false),
				toolOpEv("to-2", "issue_refund", true),
			},
			wantStatus: eval.StatusPass,
			wantRate:   0.5,
			wantMeas:   true,
		},
		{
			name: "all successful, zero rate, passes",
			evidence: []eval.Evidence{
				toolOpEv("to-1", "lookup_account", false),
			},
			wantStatus: eval.StatusPass,
			wantRate:   0,
			wantMeas:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a := evaluate(t, ToolErrorRate(tt.opts...), sampleOf(obs(conv, tt.evidence...)))
			if a.Status != tt.wantStatus {
				t.Fatalf("status = %q, want %q", a.Status, tt.wantStatus)
			}
			if tt.wantMeas {
				m := singleMeasurement(t, a)
				if m.Unit != eval.UnitRatio {
					t.Fatalf("unit = %q, want %q", m.Unit, eval.UnitRatio)
				}
				if m.Value != tt.wantRate {
					t.Fatalf("rate = %v, want %v", m.Value, tt.wantRate)
				}
			}
			if a.Status == eval.StatusFail && !findingHasResolvingEvidence(t, a) {
				t.Fatal("fail assessment carries a finding without a resolving evidence reference")
			}
			if a.Status == eval.StatusUnverified && len(a.Measurements) != 0 {
				t.Fatal("unverified assessment must not carry a measurement")
			}
		})
	}
}

func TestToolErrorRateVacuousThresholdIsNotPass(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		label string
		r     float64
	}{
		{"negative", -0.1},
		{"above one", 1.5},
		{"nan", math.NaN()},
	} {
		ev := ToolErrorRate(MaxErrorRate(tt.r))
		a := evaluate(t, ev, sampleOf(obs(content.AgenticMessages{aiText("x")}, toolOpEv("to-1", "t", true))))
		if a.Status == eval.StatusPass {
			t.Fatalf("threshold %s must never pass", tt.label)
		}
		if a.Status != eval.StatusError {
			t.Fatalf("threshold %s status = %q, want %q", tt.label, a.Status, eval.StatusError)
		}
	}
}

func TestMaxDuration(t *testing.T) {
	t.Parallel()

	conv := content.AgenticMessages{aiText("done")}

	tests := []struct {
		name       string
		limit      time.Duration
		evidence   []eval.Evidence
		wantStatus eval.AssessmentStatus
		wantSecs   float64
		wantMeas   bool
	}{
		{
			name:       "no timing evidence is unverified",
			limit:      5 * time.Second,
			evidence:   nil,
			wantStatus: eval.StatusUnverified,
		},
		{
			name:       "under the limit passes with measurement",
			limit:      5 * time.Second,
			evidence:   []eval.Evidence{timingEv("t-1", 3*time.Second)},
			wantStatus: eval.StatusPass,
			wantSecs:   3,
			wantMeas:   true,
		},
		{
			name:       "over the limit fails with measurement and evidence",
			limit:      5 * time.Second,
			evidence:   []eval.Evidence{timingEv("t-1", 6*time.Second)},
			wantStatus: eval.StatusFail,
			wantSecs:   6,
			wantMeas:   true,
		},
		{
			name:       "equal to the limit does not fail",
			limit:      5 * time.Second,
			evidence:   []eval.Evidence{timingEv("t-1", 5*time.Second)},
			wantStatus: eval.StatusPass,
			wantSecs:   5,
			wantMeas:   true,
		},
		{
			name:  "largest of several timing spans is measured",
			limit: 5 * time.Second,
			evidence: []eval.Evidence{
				timingEv("t-1", 2*time.Second),
				timingEv("t-2", 7*time.Second),
			},
			wantStatus: eval.StatusFail,
			wantSecs:   7,
			wantMeas:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a := evaluate(t, MaxDuration(tt.limit), sampleOf(obs(conv, tt.evidence...)))
			if a.Status != tt.wantStatus {
				t.Fatalf("status = %q, want %q", a.Status, tt.wantStatus)
			}
			if tt.wantMeas {
				m := singleMeasurement(t, a)
				if m.Unit != eval.UnitSecond {
					t.Fatalf("unit = %q, want %q", m.Unit, eval.UnitSecond)
				}
				if m.Value != tt.wantSecs {
					t.Fatalf("seconds = %v, want %v", m.Value, tt.wantSecs)
				}
			}
			if a.Status == eval.StatusFail && !findingHasResolvingEvidence(t, a) {
				t.Fatal("fail assessment carries a finding without a resolving evidence reference")
			}
			if a.Status == eval.StatusUnverified && len(a.Measurements) != 0 {
				t.Fatal("unverified assessment must not carry a measurement")
			}
		})
	}
}

func TestMaxDurationVacuousLimitIsNotPass(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		label string
		d     time.Duration
	}{
		{"zero", 0},
		{"negative", -time.Second},
	} {
		a := evaluate(t, MaxDuration(tt.d), sampleOf(obs(content.AgenticMessages{aiText("x")}, timingEv("t-1", time.Second))))
		if a.Status == eval.StatusPass {
			t.Fatalf("limit %s must never pass", tt.label)
		}
		if a.Status != eval.StatusError {
			t.Fatalf("limit %s status = %q, want %q", tt.label, a.Status, eval.StatusError)
		}
	}
}

// TestDescriptorsValidate asserts every evaluator's descriptor is well-formed.
func TestDescriptorsValidate(t *testing.T) {
	t.Parallel()
	evs := []eval.Evaluator{
		RequiredText("x"),
		ForbiddenText("x"),
		RequiredTool("t"),
		ForbiddenTool("t"),
		NoToolCall("t"),
		SchemaResult(),
		ToolErrorRate(),
		ToolErrorRate(MaxErrorRate(0.5)),
		MaxDuration(time.Second),
	}
	for _, ev := range evs {
		d := ev.Descriptor()
		if err := d.Validate(); err != nil {
			t.Fatalf("descriptor %q invalid: %v", d.Name, err)
		}
		if d.Method != eval.MethodProgrammatic {
			t.Fatalf("descriptor %q method = %v, want programmatic", d.Name, d.Method)
		}
	}
}
