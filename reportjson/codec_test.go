package reportjson_test

import (
	"bytes"
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/looprig/core/content"
	"github.com/looprig/eval"
	"github.com/looprig/eval/reportjson"
)

// --- fixture builders -------------------------------------------------------

func desc(name, rev string) eval.Descriptor {
	return eval.Descriptor{Name: eval.Name(name), Revision: eval.Revision(rev), Method: eval.MethodProgrammatic}
}

func measure(name string, v float64, unit eval.Unit) eval.Measurement {
	return eval.Measurement{Name: eval.Name(name), Value: v, Unit: unit}
}

func diagEvidence(id, redacted string) eval.Evidence {
	return eval.Evidence{
		ID:   eval.EvidenceID(id),
		Kind: eval.EvidenceDiagnostic,
		Diagnostic: &eval.DiagnosticEvidence{
			Code:     eval.Name("diag"),
			Severity: eval.SeverityLow,
			Message:  eval.RedactedExcerpt(redacted),
		},
	}
}

func passWith(name, rev string, ms ...eval.Measurement) eval.Assessment {
	return eval.Pass(desc(name, rev), ms...)
}

// convObservation builds a minimal valid observation carrying the given raw
// conversation text (used by the redaction canary test).
func convObservation(text string) eval.Observation {
	return eval.Observation{
		Conversation: content.AgenticMessages{
			&content.UserMessage{Message: content.Message{
				Role:   content.RoleUser,
				Blocks: []content.Block{&content.TextBlock{Text: text}},
			}},
		},
		Scope: eval.ScopeCase,
		Subject: eval.Subject{
			ID:       "subj-1",
			Kind:     eval.SubjectAgent,
			Name:     eval.Name("agent"),
			Revision: eval.Revision("r1"),
		},
	}
}

func baseReport() eval.Report {
	return eval.Report{
		ID:        "report-1",
		Suite:     eval.Revision("suite@1"),
		Target:    eval.Revision("target@1"),
		StartedAt: time.Unix(1000, 0).UTC(),
		EndedAt:   time.Unix(2000, 0).UTC(),
		Samples: []eval.SampleReport{
			{
				ScenarioID:  "s1",
				TrialIndex:  0,
				Observation: convObservation("hello world"),
				Assessments: []eval.Assessment{
					passWith("exact", "1", measure("score", 1, eval.UnitRatio)),
				},
			},
		},
		Summary: eval.Summary{
			Samples:     1,
			Assessments: map[eval.AssessmentStatus]int{eval.StatusPass: 1},
		},
		Provenance: eval.Provenance{
			Suite:  eval.Revision("suite@1"),
			Target: eval.Revision("target@1"),
			Evaluators: []eval.EvaluatorRevision{
				{Name: eval.Name("exact"), Revision: eval.Revision("1")},
			},
		},
	}
}

// --- round trip -------------------------------------------------------------

func TestRoundTrip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		report eval.Report
	}{
		{name: "minimal", report: baseReport()},
		{
			name: "all statuses",
			report: func() eval.Report {
				r := baseReport()
				r.Samples[0].Assessments = []eval.Assessment{
					eval.Pass(desc("p", "1"), measure("m", 0.5, eval.UnitRatio)),
					eval.Fail(desc("f", "1"), eval.Finding{Code: eval.FindingCode("bad"), Severity: eval.SeverityHigh, Message: "why"}),
					eval.Unverified(desc("u", "1")),
					eval.Errored(desc("er", "1")),
					eval.Skipped(desc("sk", "1")),
				}
				r.Summary.Assessments = map[eval.AssessmentStatus]int{
					eval.StatusPass: 1, eval.StatusFail: 1, eval.StatusUnverified: 1,
					eval.StatusError: 1, eval.StatusSkipped: 1,
				}
				r.Provenance.Evaluators = []eval.EvaluatorRevision{
					{Name: eval.Name("p"), Revision: eval.Revision("1")},
					{Name: eval.Name("f"), Revision: eval.Revision("1")},
					{Name: eval.Name("u"), Revision: eval.Revision("1")},
					{Name: eval.Name("er"), Revision: eval.Revision("1")},
					{Name: eval.Name("sk"), Revision: eval.Revision("1")},
				}
				return r
			}(),
		},
		{
			name: "partial run with nil observation and stage error",
			report: func() eval.Report {
				r := baseReport()
				r.Samples = append(r.Samples, eval.SampleReport{
					ScenarioID: "s2",
					TrialIndex: 0,
					TargetErr:  &eval.TargetError{Cause: context.DeadlineExceeded},
				})
				r.Summary.Samples = 2
				r.Summary.TargetErrors = 1
				return r
			}(),
		},
		{
			name: "assessment with evidence",
			report: func() eval.Report {
				r := baseReport()
				r.Samples[0].Assessments = []eval.Assessment{
					{
						Evaluator: eval.Name("ev"), Revision: eval.Revision("1"), Status: eval.StatusFail,
						Findings: []eval.Finding{{
							Code: eval.FindingCode("leak"), Severity: eval.SeverityCritical,
							Evidence: []eval.EvidenceRef{{Evidence: eval.EvidenceID("d1")}},
						}},
						Evidence: []eval.Evidence{diagEvidence("d1", "redacted-excerpt")},
					},
				}
				r.Summary.Assessments = map[eval.AssessmentStatus]int{eval.StatusFail: 1}
				r.Provenance.Evaluators = []eval.EvaluatorRevision{{Name: eval.Name("ev"), Revision: eval.Revision("1")}}
				return r
			}(),
		},
		{
			name: "assessment with positive structured-output evidence",
			report: func() eval.Report {
				r := baseReport()
				r.Samples[0].Assessments = []eval.Assessment{
					{
						Evaluator: eval.Name("ev"), Revision: eval.Revision("1"), Status: eval.StatusPass,
						Findings: []eval.Finding{{
							Code: eval.FindingCode("schema_result_satisfied"), Severity: eval.SeverityInfo,
							Evidence: []eval.EvidenceRef{{Evidence: eval.EvidenceID("so1")}},
						}},
						Evidence: []eval.Evidence{{
							ID:   eval.EvidenceID("so1"),
							Kind: eval.EvidenceStructuredOutput,
							StructuredOutput: &eval.StructuredOutput{
								SchemaName:     eval.Name("score"),
								SchemaRevision: eval.Revision("v1"),
							},
						}},
					},
				}
				r.Summary.Assessments = map[eval.AssessmentStatus]int{eval.StatusPass: 1}
				r.Provenance.Evaluators = []eval.EvaluatorRevision{{Name: eval.Name("ev"), Revision: eval.Revision("1")}}
				return r
			}(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			enc, err := reportjson.Encode(tt.report)
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			dec, err := reportjson.Decode(enc)
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			// Re-encoding the decoded (redacted) view must be byte-stable.
			enc2, err := reportjson.Encode(dec)
			if err != nil {
				t.Fatalf("re-Encode: %v", err)
			}
			if !bytes.Equal(enc, enc2) {
				t.Fatalf("codec not byte-stable:\n first=%s\n  second=%s", enc, enc2)
			}
			// Safe scalar identities survive.
			if dec.ID != tt.report.ID || dec.Suite != tt.report.Suite || dec.Target != tt.report.Target {
				t.Fatalf("identity not preserved: got %+v", dec)
			}
			if len(dec.Samples) != len(tt.report.Samples) {
				t.Fatalf("sample count = %d, want %d", len(dec.Samples), len(tt.report.Samples))
			}
			// The JSON form is the redacted projection: raw conversation is dropped.
			for i := range dec.Samples {
				if dec.Samples[i].Observation.Conversation != nil {
					t.Fatalf("sample %d retained raw conversation", i)
				}
			}
		})
	}
}

func TestDeterministicOrdering(t *testing.T) {
	t.Parallel()

	sorted := baseReport()
	sorted.Samples = []eval.SampleReport{
		{ScenarioID: "s1", TrialIndex: 0, Observation: convObservation("a"), Assessments: []eval.Assessment{
			passWith("aa", "1", measure("alpha", 1, eval.UnitRatio), measure("beta", 2, eval.UnitCount)),
		}},
		{ScenarioID: "s2", TrialIndex: 0, Observation: convObservation("b"), Assessments: []eval.Assessment{
			passWith("bb", "1"),
		}},
	}
	sorted.Summary.Samples = 2
	sorted.Summary.Assessments = map[eval.AssessmentStatus]int{eval.StatusPass: 2}

	scrambled := baseReport()
	scrambled.Samples = []eval.SampleReport{
		{ScenarioID: "s2", TrialIndex: 0, Observation: convObservation("b"), Assessments: []eval.Assessment{
			passWith("bb", "1"),
		}},
		{ScenarioID: "s1", TrialIndex: 0, Observation: convObservation("a"), Assessments: []eval.Assessment{
			passWith("aa", "1", measure("beta", 2, eval.UnitCount), measure("alpha", 1, eval.UnitRatio)),
		}},
	}
	scrambled.Summary.Samples = 2
	scrambled.Summary.Assessments = map[eval.AssessmentStatus]int{eval.StatusPass: 2}

	a, err := reportjson.Encode(sorted)
	if err != nil {
		t.Fatalf("Encode sorted: %v", err)
	}
	b, err := reportjson.Encode(scrambled)
	if err != nil {
		t.Fatalf("Encode scrambled: %v", err)
	}
	if !bytes.Equal(a, b) {
		t.Fatalf("encoding not canonical:\n sorted=%s\n scrambled=%s", a, b)
	}
}

func TestEncodeTotalOrderTiebreak(t *testing.T) {
	t.Parallel()

	// Two assessments in ONE sample share the same (Evaluator, Revision) but
	// differ in status and measurements. Without a total-order tiebreaker their
	// relative order would depend on input order, breaking byte-stability.
	build := func(swap bool) eval.Report {
		a1 := eval.Assessment{
			Evaluator: eval.Name("dup"), Revision: eval.Revision("1"),
			Status:       eval.StatusPass,
			Measurements: []eval.Measurement{measure("score", 1, eval.UnitRatio)},
		}
		a2 := eval.Assessment{
			Evaluator: eval.Name("dup"), Revision: eval.Revision("1"),
			Status:       eval.StatusFail,
			Measurements: []eval.Measurement{measure("score", 0, eval.UnitRatio)},
		}
		as := []eval.Assessment{a1, a2}
		if swap {
			as = []eval.Assessment{a2, a1}
		}
		r := baseReport()
		r.Samples[0].Assessments = as
		r.Summary.Assessments = map[eval.AssessmentStatus]int{eval.StatusPass: 1, eval.StatusFail: 1}
		return r
	}

	forward, err := reportjson.Encode(build(false))
	if err != nil {
		t.Fatalf("Encode forward: %v", err)
	}
	// Repeated encode of the same input is byte-identical.
	again, err := reportjson.Encode(build(false))
	if err != nil {
		t.Fatalf("Encode again: %v", err)
	}
	if !bytes.Equal(forward, again) {
		t.Fatalf("repeated encode not byte-identical:\n first=%s\n second=%s", forward, again)
	}
	// A shuffled input order encodes to identical bytes: the collision on
	// (Evaluator, Revision) is broken deterministically by content.
	shuffled, err := reportjson.Encode(build(true))
	if err != nil {
		t.Fatalf("Encode shuffled: %v", err)
	}
	if !bytes.Equal(forward, shuffled) {
		t.Fatalf("encoding depends on input order:\n forward=%s\n shuffled=%s", forward, shuffled)
	}
}

func TestRedaction(t *testing.T) {
	t.Parallel()

	const (
		convCanary    = "CANARY_CONVERSATION_a1b2c3"
		findingCanary = "CANARY_FINDING_d4e5f6"
		causeCanary   = "CANARY_TARGETERR_g7h8i9"
	)

	r := baseReport()
	r.Samples[0].Observation = convObservation(convCanary)
	r.Samples[0].Assessments = []eval.Assessment{
		eval.Fail(desc("judge", "1"), eval.Finding{
			Code: eval.FindingCode("relevance"), Severity: eval.SeverityHigh, Message: findingCanary,
		}),
	}
	r.Samples = append(r.Samples, eval.SampleReport{
		ScenarioID: "s2", TrialIndex: 0,
		TargetErr: &eval.TargetError{Cause: errors.New(causeCanary)},
	})
	r.Summary.Samples = 2
	r.Summary.TargetErrors = 1
	r.Summary.Assessments = map[eval.AssessmentStatus]int{eval.StatusFail: 1}

	enc, err := reportjson.Encode(r)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	for _, canary := range []string{convCanary, findingCanary, causeCanary} {
		if bytes.Contains(enc, []byte(canary)) {
			t.Fatalf("canary %q leaked into wire form:\n%s", canary, enc)
		}
	}
	// The SAFE fields MUST survive, so this canary test cannot pass on a blank
	// or near-empty encoding. Scenario identities, the finding code, and the
	// assessment status are all safe and expected on the wire.
	for _, want := range []string{`"s1"`, `"s2"`, `"relevance"`, `"fail"`} {
		if !bytes.Contains(enc, []byte(want)) {
			t.Fatalf("expected safe field %q missing from wire form:\n%s", want, enc)
		}
	}
}

func TestEncodeRejectsNonFinite(t *testing.T) {
	t.Parallel()

	for _, v := range []float64{math.Inf(1), math.Inf(-1), math.NaN()} {
		r := baseReport()
		r.Samples[0].Assessments = []eval.Assessment{
			{Evaluator: eval.Name("e"), Revision: eval.Revision("1"), Status: eval.StatusPass,
				Measurements: []eval.Measurement{{Name: eval.Name("m"), Value: v, Unit: eval.UnitRatio}}},
		}
		_, err := reportjson.Encode(r)
		var nfe *reportjson.NonFiniteValueError
		if !errors.As(err, &nfe) {
			t.Fatalf("value %v: got err %v, want *NonFiniteValueError", v, err)
		}
	}
}

func TestDecodeRejects(t *testing.T) {
	t.Parallel()

	valid, err := reportjson.Encode(baseReport())
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	tests := []struct {
		name string
		data []byte
		want func(error) bool
	}{
		{
			name: "unknown version",
			data: []byte(`{"version":"report/v99","report":{}}`),
			want: func(e error) bool { var x *reportjson.UnknownVersionError; return errors.As(e, &x) },
		},
		{
			name: "missing version",
			data: []byte(`{"report":{}}`),
			want: func(e error) bool { var x *reportjson.UnknownVersionError; return errors.As(e, &x) },
		},
		{
			name: "invalid json",
			data: []byte(`{not json`),
			want: func(e error) bool { var x *reportjson.MalformedReportError; return errors.As(e, &x) },
		},
		{
			name: "trailing data",
			data: append(append([]byte{}, valid...), []byte(`{}`)...),
			want: func(e error) bool { var x *reportjson.MalformedReportError; return errors.As(e, &x) },
		},
		{
			name: "invalid utf8",
			data: append([]byte(`{"version":"report/v1","report":{"id":"`), []byte{0xff, 0xfe}...),
			want: func(e error) bool { var x *reportjson.MalformedReportError; return errors.As(e, &x) },
		},
		{
			name: "oversize",
			data: bytes.Repeat([]byte("a"), reportjson.MaxReportBytes+1),
			want: func(e error) bool { var x *reportjson.ReportTooLargeError; return errors.As(e, &x) },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := reportjson.Decode(tt.data)
			if err == nil || !tt.want(err) {
				t.Fatalf("Decode(%s): got %v", tt.name, err)
			}
		})
	}
}

// TestDecodeRejectsInvalidReport confirms Decode enforces the whole-report
// invariants, not just per-assessment validity: a report whose parts each decode
// but which is internally contradictory is rejected as an InvalidReportError.
func TestDecodeRejectsInvalidReport(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(r *eval.Report)
	}{
		{
			name:   "ended before started",
			mutate: func(r *eval.Report) { r.EndedAt = r.StartedAt.Add(-time.Hour) },
		},
		{
			name:   "negative trial index",
			mutate: func(r *eval.Report) { r.Samples[0].TrialIndex = -1 },
		},
		{
			name:   "empty scenario id",
			mutate: func(r *eval.Report) { r.Samples[0].ScenarioID = "" },
		},
		{
			name:   "oversize scenario id",
			mutate: func(r *eval.Report) { r.Samples[0].ScenarioID = strings.Repeat("x", eval.MaxIDBytes+1) },
		},
		{
			name: "empty suite revision",
			mutate: func(r *eval.Report) {
				r.Suite = ""
				r.Provenance.Suite = ""
			},
		},
		{
			name: "successful sample with empty target revision",
			mutate: func(r *eval.Report) {
				r.Target = ""
				r.Provenance.Target = ""
			},
		},
		{
			name: "target revision present when every target failed",
			mutate: func(r *eval.Report) {
				r.Samples[0].TargetErr = &eval.TargetError{Cause: errors.New("down")}
				r.Samples[0].Assessments = nil
				r.Summary.TargetErrors = 1
				r.Summary.Assessments = nil
			},
		},
		{
			name: "duplicate sample identity",
			mutate: func(r *eval.Report) {
				r.Samples = append(r.Samples, r.Samples[0])
				r.Summary.Samples = 2
			},
		},
		{
			name: "duplicate evaluator within sample",
			mutate: func(r *eval.Report) {
				r.Samples[0].Assessments = append(r.Samples[0].Assessments, passWith("exact", "1", measure("score", 1, eval.UnitRatio)))
				r.Summary.Assessments = map[eval.AssessmentStatus]int{eval.StatusPass: 2}
			},
		},
		{
			name:   "summary count disagrees with assessments",
			mutate: func(r *eval.Report) { r.Summary.Assessments = map[eval.AssessmentStatus]int{eval.StatusFail: 1} },
		},
		{
			// The same evaluator name appears with revision 1 in one sample and
			// revision 2 in another: report-wide revision drift the decode boundary
			// must reject. Summary and provenance are kept consistent so the drift is
			// the sole failure under test.
			name: "evaluator revision drift across samples",
			mutate: func(r *eval.Report) {
				r.Samples = append(r.Samples, eval.SampleReport{
					ScenarioID:  "s2",
					TrialIndex:  0,
					Assessments: []eval.Assessment{passWith("exact", "2", measure("score", 1, eval.UnitRatio))},
				})
				r.Summary.Samples = 2
				r.Summary.Assessments = map[eval.AssessmentStatus]int{eval.StatusPass: 2}
				r.Provenance.Evaluators = []eval.EvaluatorRevision{
					{Name: eval.Name("exact"), Revision: eval.Revision("1")},
					{Name: eval.Name("exact"), Revision: eval.Revision("2")},
				}
			},
		},
		{
			// A forged baseline that presents a passing assessment for a sample whose
			// target errored: the runner never emits this, and the decode boundary must
			// reject it (a target error skips assessment). Summary.TargetErrors is kept
			// consistent so the contradiction is the sole failure under test.
			name: "target error with assessments",
			mutate: func(r *eval.Report) {
				r.Samples[0].TargetErr = &eval.TargetError{Cause: errors.New("boom")}
				r.Summary.TargetErrors = 1
			},
		},
		{
			name:   "provenance suite contradicts report suite",
			mutate: func(r *eval.Report) { r.Provenance.Suite = eval.Revision("different-suite@9") },
		},
		{
			name: "provenance evaluator set contradicts assessed identities",
			mutate: func(r *eval.Report) {
				r.Provenance.Evaluators = []eval.EvaluatorRevision{{Name: eval.Name("phantom"), Revision: eval.Revision("9")}}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := baseReport()
			tt.mutate(&r)
			enc, err := reportjson.Encode(r)
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			_, err = reportjson.Decode(enc)
			var ire *reportjson.InvalidReportError
			if !errors.As(err, &ire) {
				t.Fatalf("Decode: got %v, want *InvalidReportError", err)
			}
			var rve *eval.ReportValidationError
			var ve *eval.ValidationError
			if !errors.As(err, &rve) && !errors.As(err, &ve) {
				t.Fatalf("wrapped cause not a typed eval validation error: %v", err)
			}
		})
	}
}

// TestDecodeGenuineRunRoundTrips confirms a real Run report survives
// Encode→Decode: it decodes without error (which means it passed the report-level
// validation the decoder now enforces), proving Report.Validate is aligned with
// the runner's real output.
func TestDecodeGenuineRunRoundTrips(t *testing.T) {
	t.Parallel()
	suite := eval.Suite{
		Name:     eval.Name("smoke"),
		Revision: eval.Revision("suite-v1"),
		Scenarios: []eval.Scenario{
			{ID: "a", Name: eval.Name("agent"), Revision: eval.Revision("r1"), Input: userInput("prompt a")},
			{ID: "b", Name: eval.Name("agent"), Revision: eval.Revision("r1"), Input: userInput("prompt b")},
		},
	}
	report, err := eval.Run(context.Background(), eval.RunConfig{}, suite, roundTripTarget{}, roundTripEvaluator{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if verr := report.Validate(); verr != nil {
		t.Fatalf("runner report failed Validate before encode: %v", verr)
	}
	enc, err := reportjson.Encode(report)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	dec, err := reportjson.Decode(enc)
	if err != nil {
		t.Fatalf("Decode of genuine run report failed validation: %v", err)
	}
	if dec.ID != report.ID {
		t.Fatalf("decoded id = %q, want %q", dec.ID, report.ID)
	}
}

// userInput builds a minimal valid scenario input.
func userInput(text string) content.AgenticMessages {
	return content.AgenticMessages{
		&content.UserMessage{Message: content.Message{
			Role:   content.RoleUser,
			Blocks: []content.Block{&content.TextBlock{Text: text}},
		}},
	}
}

// roundTripTarget is a minimal Target returning a fixed valid observation.
type roundTripTarget struct{}

func (roundTripTarget) Name() string { return "rt" }

func (roundTripTarget) Observe(context.Context, eval.Scenario) (eval.Observation, error) {
	return eval.Observation{
		Scope:   eval.ScopeCase,
		Subject: eval.Subject{ID: "subj", Kind: eval.SubjectModel, Name: eval.Name("agent"), Revision: eval.Revision("r1")},
	}, nil
}

// roundTripEvaluator is a minimal Evaluator returning a passing assessment.
type roundTripEvaluator struct{}

func (roundTripEvaluator) Descriptor() eval.Descriptor {
	return eval.Descriptor{Name: eval.Name("q"), Revision: eval.Revision("v1"), Method: eval.MethodProgrammatic}
}

func (e roundTripEvaluator) Evaluate(context.Context, eval.Sample) (eval.Assessment, error) {
	return eval.Pass(e.Descriptor(), measure("score", 1, eval.UnitRatio)), nil
}

func TestDecodePreservesTargetErrorClass(t *testing.T) {
	t.Parallel()

	r := baseReport()
	r.Samples = append(r.Samples, eval.SampleReport{
		ScenarioID: "s2", TrialIndex: 0,
		TargetErr: &eval.TargetError{Cause: context.DeadlineExceeded},
	})
	r.Summary.Samples = 2
	r.Summary.TargetErrors = 1

	enc, err := reportjson.Encode(r)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	dec, err := reportjson.Decode(enc)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	var found bool
	for _, s := range dec.Samples {
		if s.TargetErr == nil {
			continue
		}
		found = true
		var dte *reportjson.DecodedTargetError
		if !errors.As(s.TargetErr.Cause, &dte) {
			t.Fatalf("decoded target error cause = %v, want *DecodedTargetError", s.TargetErr.Cause)
		}
		if dte.Class != reportjson.TargetErrorTimeout {
			t.Fatalf("class = %q, want %q", dte.Class, reportjson.TargetErrorTimeout)
		}
	}
	if !found {
		t.Fatal("no decoded target error found")
	}
}

// --- sink -------------------------------------------------------------------

func TestFileSinkAtomicWrite(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	sink := reportjson.NewFileSink(dir)
	r := baseReport()
	if err := sink.WriteReport(context.Background(), r); err != nil {
		t.Fatalf("WriteReport: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	var final string
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".json" {
			final = e.Name()
		}
	}
	if final == "" {
		t.Fatalf("no .json report written; entries=%v", entries)
	}
	data, err := os.ReadFile(filepath.Join(dir, final))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if _, err := reportjson.Decode(data); err != nil {
		t.Fatalf("written file does not decode: %v", err)
	}
	// No temp file left behind.
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".json" {
			t.Fatalf("stray non-report file left behind: %s", e.Name())
		}
	}
}

func TestFileSinkRejectsPathEscape(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	sink := reportjson.NewFileSink(dir)
	r := baseReport()
	r.ID = "../escape"

	err := sink.WriteReport(context.Background(), r)
	var pe *reportjson.InvalidReportIDError
	var esc *reportjson.PathEscapeError
	if !errors.As(err, &pe) && !errors.As(err, &esc) {
		t.Fatalf("WriteReport(escaping id): got %v, want path rejection", err)
	}
	// Nothing must have been written outside the root.
	if _, statErr := os.Stat(filepath.Join(filepath.Dir(dir), "escape.json")); statErr == nil {
		t.Fatal("escaping write landed outside the root")
	}
}
