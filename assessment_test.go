package eval

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/looprig/core/content"
)

// newValidAssessment builds a fully valid passing assessment. Its single finding
// references the one evidence entry, exercising the happy path of dangling-ref
// resolution against Assessment.Evidence.
func newValidAssessment() Assessment {
	return Assessment{
		Evaluator: "answer-relevance",
		Revision:  "v1",
		Status:    StatusPass,
		Measurements: []Measurement{
			{Name: "relevance", Value: 0.92, Unit: UnitRatio},
			{Name: "latency", Value: 1.5, Unit: UnitSecond},
		},
		Findings: []Finding{
			{
				Code:     "note",
				Severity: SeverityInfo,
				Message:  "answer is on topic",
				Evidence: []EvidenceRef{{Evidence: "ev-usage"}},
			},
		},
		Evidence: []Evidence{
			{ID: "ev-usage", Kind: EvidenceUsage, Usage: &UsageEvidence{
				Usage: content.Usage{InputTokens: 10, OutputTokens: 3},
			}},
		},
		Duration: 12 * time.Millisecond,
	}
}

func TestAssessmentValidateHappyPath(t *testing.T) {
	t.Parallel()
	if err := newValidAssessment().Validate(); err != nil {
		t.Fatalf("valid assessment rejected: %v", err)
	}
}

func TestAssessmentValidateIdentity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mutate  func(*Assessment)
		wantErr bool
	}{
		{name: "valid", mutate: func(*Assessment) {}, wantErr: false},
		{name: "empty evaluator rejected", mutate: func(a *Assessment) { a.Evaluator = "" }, wantErr: true},
		{name: "empty revision rejected", mutate: func(a *Assessment) { a.Revision = "" }, wantErr: true},
		{name: "unset status rejected", mutate: func(a *Assessment) { a.Status = "" }, wantErr: true},
		{name: "unknown status rejected", mutate: func(a *Assessment) { a.Status = AssessmentStatus("great") }, wantErr: true},
		{name: "negative duration rejected", mutate: func(a *Assessment) { a.Duration = -time.Second }, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a := newValidAssessment()
			tt.mutate(&a)
			err := a.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil && isBareError(err) {
				t.Fatalf("Validate() returned bare error %v; want typed error", err)
			}
		})
	}
}

func TestAssessmentValidateDuplicateMeasurementNames(t *testing.T) {
	t.Parallel()
	a := newValidAssessment()
	a.Measurements = append(a.Measurements, Measurement{Name: "relevance", Value: 0.1, Unit: UnitRatio})
	err := a.Validate()
	if err == nil {
		t.Fatal("Validate() = nil, want error for duplicate measurement name")
	}
	var dm *DuplicateMeasurementError
	if !errors.As(err, &dm) {
		t.Fatalf("Validate() error = %v, want *DuplicateMeasurementError", err)
	}
	// The duplicated measurement name must not leak into the diagnostic.
	assertNoUntrustedEcho(t, err, "relevance")
}

func TestAssessmentValidateNonFiniteMeasurement(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name  string
		value float64
	}{
		{name: "NaN", value: math.NaN()},
		{name: "positive infinity", value: math.Inf(1)},
		{name: "negative infinity", value: math.Inf(-1)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a := newValidAssessment()
			a.Measurements = []Measurement{{Name: "score", Value: tt.value, Unit: UnitRatio}}
			err := a.Validate()
			if err == nil {
				t.Fatalf("Validate() = nil, want error for non-finite value %v", tt.value)
			}
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("Validate() error = %v, want *ValidationError", err)
			}
		})
	}

	// A finite value at the boundary is accepted.
	a := newValidAssessment()
	a.Measurements = []Measurement{{Name: "score", Value: 0, Unit: UnitCount}}
	if err := a.Validate(); err != nil {
		t.Fatalf("finite zero measurement rejected: %v", err)
	}
}

func TestAssessmentValidateInvalidUnit(t *testing.T) {
	t.Parallel()
	a := newValidAssessment()
	a.Measurements = []Measurement{{Name: "score", Value: 1, Unit: Unit("furlong")}}
	err := a.Validate()
	if err == nil {
		t.Fatal("Validate() = nil, want error for invalid unit")
	}
	var ee *InvalidEnumError
	if !errors.As(err, &ee) {
		t.Fatalf("Validate() error = %v, want *InvalidEnumError", err)
	}
	assertNoUntrustedEcho(t, err, "furlong")
}

func TestAssessmentValidateEmptyMeasurementName(t *testing.T) {
	t.Parallel()
	a := newValidAssessment()
	a.Measurements = []Measurement{{Name: "", Value: 1, Unit: UnitCount}}
	if err := a.Validate(); err == nil {
		t.Fatal("Validate() = nil, want error for empty measurement name")
	} else if isBareError(err) {
		t.Fatalf("bare error %v; want typed", err)
	}
}

func TestAssessmentValidateDuplicateFindingCodes(t *testing.T) {
	t.Parallel()
	a := newValidAssessment()
	a.Findings = []Finding{
		{Code: "xyzzy", Severity: SeverityLow, Message: "first"},
		{Code: "xyzzy", Severity: SeverityLow, Message: "second"},
	}
	err := a.Validate()
	if err == nil {
		t.Fatal("Validate() = nil, want error for duplicate finding code")
	}
	var df *DuplicateFindingError
	if !errors.As(err, &df) {
		t.Fatalf("Validate() error = %v, want *DuplicateFindingError", err)
	}
	assertNoUntrustedEcho(t, err, "xyzzy")
}

func TestAssessmentValidateEmptyFindingCode(t *testing.T) {
	t.Parallel()
	a := newValidAssessment()
	a.Findings = []Finding{{Code: "", Severity: SeverityLow, Message: "x"}}
	if err := a.Validate(); err == nil {
		t.Fatal("Validate() = nil, want error for empty finding code")
	} else if isBareError(err) {
		t.Fatalf("bare error %v; want typed", err)
	}
}

func TestAssessmentValidateFindingMessageBound(t *testing.T) {
	t.Parallel()
	a := newValidAssessment()
	a.Findings = []Finding{{Code: "big", Severity: SeverityLow, Message: strings.Repeat("x", MaxFindingMessageBytes+1)}}
	err := a.Validate()
	if err == nil {
		t.Fatal("Validate() = nil, want error for oversized finding message")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("Validate() error = %v, want *ValidationError", err)
	}
}

func TestAssessmentValidateDanglingEvidenceRef(t *testing.T) {
	t.Parallel()

	// A finding referencing an EvidenceID not present in Assessment.Evidence.
	a := newValidAssessment()
	a.Findings = []Finding{{
		Code:     "note",
		Severity: SeverityInfo,
		Message:  "x",
		Evidence: []EvidenceRef{{Evidence: "does-not-exist"}},
	}}
	err := a.Validate()
	if err == nil {
		t.Fatal("Validate() = nil, want error for dangling evidence reference")
	}
	var ue *UnknownEvidenceError
	if !errors.As(err, &ue) {
		t.Fatalf("Validate() error = %v, want *UnknownEvidenceError", err)
	}
	assertNoUntrustedEcho(t, err, "does-not-exist")
}

func TestAssessmentValidateDuplicateEvidenceID(t *testing.T) {
	t.Parallel()
	a := newValidAssessment()
	a.Evidence = append(a.Evidence, Evidence{ID: "ev-usage", Kind: EvidenceUsage, Usage: &UsageEvidence{
		Usage: content.Usage{InputTokens: 1},
	}})
	err := a.Validate()
	if err == nil {
		t.Fatal("Validate() = nil, want error for duplicate evidence id")
	}
	var de *DuplicateEvidenceError
	if !errors.As(err, &de) {
		t.Fatalf("Validate() error = %v, want *DuplicateEvidenceError", err)
	}
}

func TestAssessmentValidateStatusConsistency(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mutate  func(*Assessment)
		wantErr bool
	}{
		{
			name: "pass with high-severity finding rejected",
			mutate: func(a *Assessment) {
				a.Status = StatusPass
				a.Findings = []Finding{{Code: "leak", Severity: SeverityHigh, Message: "canary leaked"}}
			},
			wantErr: true,
		},
		{
			name: "pass with critical-severity finding rejected",
			mutate: func(a *Assessment) {
				a.Status = StatusPass
				a.Findings = []Finding{{Code: "crit", Severity: SeverityCritical, Message: "secret exfiltrated"}}
			},
			wantErr: true,
		},
		{
			name: "pass with info-severity finding allowed",
			mutate: func(a *Assessment) {
				a.Status = StatusPass
				a.Findings = []Finding{{Code: "note", Severity: SeverityInfo, Message: "advisory"}}
			},
			wantErr: false,
		},
		{
			name: "fail with critical-severity finding allowed",
			mutate: func(a *Assessment) {
				a.Status = StatusFail
				a.Findings = []Finding{{Code: "crit", Severity: SeverityCritical, Message: "bad"}}
			},
			wantErr: false,
		},
		{
			name: "unverified carrying a measurement rejected",
			mutate: func(a *Assessment) {
				a.Status = StatusUnverified
				a.Findings = nil
				// keep default measurements
			},
			wantErr: true,
		},
		{
			name: "error carrying a measurement rejected",
			mutate: func(a *Assessment) {
				a.Status = StatusError
				a.Findings = nil
			},
			wantErr: true,
		},
		{
			name: "skipped carrying a measurement rejected",
			mutate: func(a *Assessment) {
				a.Status = StatusSkipped
				a.Findings = nil
			},
			wantErr: true,
		},
		{
			name: "unverified with no measurement allowed",
			mutate: func(a *Assessment) {
				a.Status = StatusUnverified
				a.Measurements = nil
				a.Findings = nil
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a := newValidAssessment()
			tt.mutate(&a)
			err := a.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				var sc *StatusConsistencyError
				if !errors.As(err, &sc) {
					t.Fatalf("Validate() error = %v, want *StatusConsistencyError", err)
				}
			}
		})
	}
}

func TestAssessmentConstructors(t *testing.T) {
	t.Parallel()

	desc := newValidDescriptor()

	tests := []struct {
		name       string
		build      func() Assessment
		wantStatus AssessmentStatus
	}{
		{
			name:       "pass",
			build:      func() Assessment { return Pass(desc, Measurement{Name: "score", Value: 1, Unit: UnitRatio}) },
			wantStatus: StatusPass,
		},
		{
			name:       "fail",
			build:      func() Assessment { return Fail(desc, Finding{Code: "bad", Severity: SeverityHigh, Message: "no"}) },
			wantStatus: StatusFail,
		},
		{
			name:       "unverified",
			build:      func() Assessment { return Unverified(desc) },
			wantStatus: StatusUnverified,
		},
		{
			name: "errored",
			build: func() Assessment {
				return Errored(desc, Finding{Code: "infra", Severity: SeverityHigh, Message: "judge down"})
			},
			wantStatus: StatusError,
		},
		{
			name:       "skipped",
			build:      func() Assessment { return Skipped(desc) },
			wantStatus: StatusSkipped,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a := tt.build()
			if a.Status != tt.wantStatus {
				t.Fatalf("status = %q, want %q", a.Status, tt.wantStatus)
			}
			if a.Evaluator != desc.Name || a.Revision != desc.Revision {
				t.Fatalf("identity = (%q,%q), want (%q,%q)", a.Evaluator, a.Revision, desc.Name, desc.Revision)
			}
			if err := a.Validate(); err != nil {
				t.Fatalf("constructor produced invalid assessment: %v", err)
			}
		})
	}
}

// TestErroredCarriesNoMeasurement asserts the infrastructure/quality boundary:
// Errored produces an error-status assessment, and the consistency rules forbid
// a measurement being smuggled onto it.
func TestErroredCarriesNoMeasurement(t *testing.T) {
	t.Parallel()
	a := Errored(newValidDescriptor())
	a.Measurements = []Measurement{{Name: "score", Value: 0, Unit: UnitRatio}}
	if err := a.Validate(); err == nil {
		t.Fatal("Validate() = nil, want error for measurement on an error-status assessment")
	}
}
