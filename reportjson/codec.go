// Package reportjson is the versioned, redacted JSON codec for eval reports and
// a file sink that persists them. It is the report's untrusted deserialization
// boundary: a report is one report/v1 envelope carrying an explicit version
// discriminator and a payload. Decode reads the version first and rejects
// unknown or missing versions before trusting the payload (fail-closed), bounds
// the input size, requires exactly one valid-UTF-8 JSON value, and validates the
// reconstructed assessments against the strict eval domain types.
//
// The wire form is REDACTED BY DEFAULT and CANONICAL. It carries only safe
// fields: sample and evaluator identities, assessment statuses, finite
// measurements, finding CODES and severities (never a finding's free-text
// message), already-redacted evidence payloads (hashes, classifications, counts,
// message indexes, redacted excerpts — never raw text), provenance, timings, and
// a safe classification of any target-stage error (never its raw cause). It never
// marshals the raw Observation (conversation text, tool arguments/results, or the
// raw trace); the design records reports as redacted evidence plus references to
// separately controlled raw traces. Decoding therefore yields the redacted
// projection: Samples[].Observation is the zero value and Finding.Message is
// empty. The JSON form is not a lossless Observation round-trip; it IS a
// byte-stable fixed point over the safe fields.
//
// Encoding is deterministic: samples, assessments, measurements, findings, and
// evidence are emitted in a canonical order independent of input order, and the
// Summary status map is emitted in a fixed status order rather than map-iteration
// order. Non-finite measurement values are rejected fail-closed.
package reportjson

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/looprig/eval"
)

// version1 is the sole wire version this codec implements. It is read FIRST from
// every document; an unknown or missing version is rejected before the payload is
// trusted (fail-closed).
const version1 = "report/v1"

// MaxReportBytes bounds an encoded report at the untrusted decode boundary. It
// rejects an oversized document before it can exhaust memory. Conservative
// starting value; tune to real report sizes later.
const MaxReportBytes = 64 << 20 // 64 MiB

// statusOrder is the fixed, canonical order in which assessment statuses are
// emitted (both the Summary counts and any status-keyed collection). Emitting in
// this order — never map-iteration order — makes the wire form deterministic.
var statusOrder = []eval.AssessmentStatus{
	eval.StatusPass, eval.StatusFail, eval.StatusUnverified, eval.StatusError, eval.StatusSkipped,
}

// TargetErrorClass is the safe, content-free classification of a target-stage
// failure recorded on the wire. The raw TargetError.Cause is never serialized;
// only this closed classification is.
type TargetErrorClass string

const (
	// TargetErrorTimeout: the target stage exceeded its deadline.
	TargetErrorTimeout TargetErrorClass = "timeout"
	// TargetErrorCancelled: the target stage was cancelled.
	TargetErrorCancelled TargetErrorClass = "cancelled"
	// TargetErrorInvalidObservation: the target produced an observation that
	// failed domain validation.
	TargetErrorInvalidObservation TargetErrorClass = "invalid_observation"
	// TargetErrorFailed: the target stage failed for another reason.
	TargetErrorFailed TargetErrorClass = "failed"
)

// DecodedTargetError is the typed, content-free cause reconstructed from a
// decoded report's safe target-error classification. A decoded report's
// SampleReport.TargetErr wraps one of these instead of the original (unrecovered)
// cause, so callers can still classify the failure via errors.As without any raw
// cause text ever having been serialized.
type DecodedTargetError struct {
	Class TargetErrorClass
}

func (e *DecodedTargetError) Error() string {
	return "eval/reportjson: target stage failed (" + string(e.Class) + ")"
}

// --- wire structs -----------------------------------------------------------

type envelopeJSON struct {
	Version string          `json:"version"`
	Report  json.RawMessage `json:"report"`
}

type reportJSON struct {
	ID         string         `json:"id"`
	Suite      string         `json:"suite"`
	Target     string         `json:"target"`
	StartedAt  time.Time      `json:"started_at"`
	EndedAt    time.Time      `json:"ended_at"`
	Samples    []sampleJSON   `json:"samples"`
	Summary    summaryJSON    `json:"summary"`
	Provenance provenanceJSON `json:"provenance"`
}

type sampleJSON struct {
	ScenarioID  string            `json:"scenario_id"`
	TrialIndex  int               `json:"trial_index"`
	TargetError *TargetErrorClass `json:"target_error,omitempty"`
	Assessments []assessmentJSON  `json:"assessments"`
}

type assessmentJSON struct {
	Evaluator     string                `json:"evaluator"`
	Revision      string                `json:"revision"`
	Status        eval.AssessmentStatus `json:"status"`
	Measurements  []measurementJSON     `json:"measurements,omitempty"`
	Findings      []findingJSON         `json:"findings,omitempty"`
	Evidence      []eval.Evidence       `json:"evidence,omitempty"`
	DurationNanos int64                 `json:"duration_nanos"`
}

type measurementJSON struct {
	Name  string    `json:"name"`
	Value float64   `json:"value"`
	Unit  eval.Unit `json:"unit"`
}

// findingJSON is the redacted projection of a Finding: its safe code, severity,
// and evidence references. The evaluator-authored free-text Message is
// deliberately dropped — it is untrusted and must never reach the wire form.
type findingJSON struct {
	Code     string             `json:"code"`
	Severity eval.Severity      `json:"severity"`
	Evidence []eval.EvidenceRef `json:"evidence,omitempty"`
}

type summaryJSON struct {
	Samples      int               `json:"samples"`
	TargetErrors int               `json:"target_errors"`
	Assessments  []statusCountJSON `json:"assessments,omitempty"`
}

type statusCountJSON struct {
	Status eval.AssessmentStatus `json:"status"`
	Count  int                   `json:"count"`
}

type provenanceJSON struct {
	Suite      string             `json:"suite"`
	Target     string             `json:"target"`
	Evaluators []evaluatorRevJSON `json:"evaluators,omitempty"`
}

type evaluatorRevJSON struct {
	Name     string `json:"name"`
	Revision string `json:"revision"`
}

// --- encode -----------------------------------------------------------------

// Encode serializes a valid report to its canonical, redacted report/v1 wire
// form. It rejects a report that fails eval.Report.Validate before JSON
// serialization can normalize a malformed identity, rejects any non-finite
// measurement value fail-closed, drops all raw content (conversation, finding
// messages, target-error causes), and emits every collection in a canonical order
// so the same report always encodes to identical bytes.
func Encode(r eval.Report) ([]byte, error) {
	rj, err := projectReport(r)
	if err != nil {
		return nil, err
	}
	// projectReport runs first so its precise projection errors (notably
	// NonFiniteValueError) remain part of the public encode contract. Validate
	// still runs before either top-level payload/envelope json.Marshal call, so
	// malformed UTF-8 identities cannot be replaced with U+FFFD in emitted bytes.
	if err := r.Validate(); err != nil {
		return nil, &EncodeError{Cause: err}
	}
	payload, err := json.Marshal(rj)
	if err != nil {
		return nil, &EncodeError{Cause: err}
	}
	out, err := json.Marshal(envelopeJSON{Version: version1, Report: payload})
	if err != nil {
		return nil, &EncodeError{Cause: err}
	}
	if len(out) > MaxReportBytes {
		return nil, &ReportTooLargeError{Size: len(out), Max: MaxReportBytes}
	}
	return out, nil
}

// canonicalSort sorts s in place by the supplied primary less function and
// breaks EVERY tie on the element's canonical JSON encoding, so a collision on
// the primary key orders deterministically by content instead of by input
// position. This gives each encoder sort a total order — the byte-stability
// invariant holds even when two elements share a primary key (e.g. two
// assessments with the same Evaluator+Revision). Elements are already internally
// canonicalized before this runs, so their encodings are stable; a marshal
// failure (unreachable for the wire structs) surfaces as an EncodeError.
func canonicalSort[T any](s []T, less func(a, b T) bool) error {
	keys := make([]string, len(s))
	for i := range s {
		b, err := json.Marshal(s[i])
		if err != nil {
			return &EncodeError{Cause: err}
		}
		keys[i] = string(b)
	}
	idx := make([]int, len(s))
	for i := range idx {
		idx[i] = i
	}
	sort.Slice(idx, func(a, b int) bool {
		ia, ib := idx[a], idx[b]
		if less(s[ia], s[ib]) {
			return true
		}
		if less(s[ib], s[ia]) {
			return false
		}
		return keys[ia] < keys[ib]
	})
	ordered := make([]T, len(s))
	for i, j := range idx {
		ordered[i] = s[j]
	}
	copy(s, ordered)
	return nil
}

// projectReport builds the redacted, canonically ordered wire projection of a
// report. It is the single place raw content is dropped.
func projectReport(r eval.Report) (reportJSON, error) {
	samples := make([]sampleJSON, len(r.Samples))
	for i := range r.Samples {
		s, err := projectSample(r.Samples[i])
		if err != nil {
			return reportJSON{}, err
		}
		samples[i] = s
	}
	if err := canonicalSort(samples, func(a, b sampleJSON) bool {
		if a.ScenarioID != b.ScenarioID {
			return a.ScenarioID < b.ScenarioID
		}
		return a.TrialIndex < b.TrialIndex
	}); err != nil {
		return reportJSON{}, err
	}

	return reportJSON{
		ID:         r.ID,
		Suite:      string(r.Suite),
		Target:     string(r.Target),
		StartedAt:  r.StartedAt,
		EndedAt:    r.EndedAt,
		Samples:    samples,
		Summary:    projectSummary(r.Summary),
		Provenance: projectProvenance(r.Provenance),
	}, nil
}

func projectSample(s eval.SampleReport) (sampleJSON, error) {
	out := sampleJSON{ScenarioID: s.ScenarioID, TrialIndex: s.TrialIndex}
	if s.TargetErr != nil {
		class := classifyTargetError(s.TargetErr)
		out.TargetError = &class
	}
	as := make([]assessmentJSON, len(s.Assessments))
	for i := range s.Assessments {
		a, err := projectAssessment(s.Assessments[i])
		if err != nil {
			return sampleJSON{}, err
		}
		as[i] = a
	}
	if err := canonicalSort(as, func(a, b assessmentJSON) bool {
		if a.Evaluator != b.Evaluator {
			return a.Evaluator < b.Evaluator
		}
		return a.Revision < b.Revision
	}); err != nil {
		return sampleJSON{}, err
	}
	out.Assessments = as
	return out, nil
}

func projectAssessment(a eval.Assessment) (assessmentJSON, error) {
	ms := make([]measurementJSON, len(a.Measurements))
	for i, m := range a.Measurements {
		if math.IsNaN(m.Value) || math.IsInf(m.Value, 0) {
			return assessmentJSON{}, &NonFiniteValueError{}
		}
		ms[i] = measurementJSON{Name: string(m.Name), Value: m.Value, Unit: m.Unit}
	}
	if err := canonicalSort(ms, func(a, b measurementJSON) bool { return a.Name < b.Name }); err != nil {
		return assessmentJSON{}, err
	}

	fs := make([]findingJSON, len(a.Findings))
	for i, f := range a.Findings {
		fs[i] = findingJSON{Code: string(f.Code), Severity: f.Severity, Evidence: f.Evidence}
	}
	if err := canonicalSort(fs, func(a, b findingJSON) bool { return a.Code < b.Code }); err != nil {
		return assessmentJSON{}, err
	}

	ev := append([]eval.Evidence(nil), a.Evidence...)
	if err := canonicalSort(ev, func(a, b eval.Evidence) bool { return a.ID < b.ID }); err != nil {
		return assessmentJSON{}, err
	}

	return assessmentJSON{
		Evaluator:     string(a.Evaluator),
		Revision:      string(a.Revision),
		Status:        a.Status,
		Measurements:  ms,
		Findings:      fs,
		Evidence:      ev,
		DurationNanos: a.Duration.Nanoseconds(),
	}, nil
}

func projectSummary(s eval.Summary) summaryJSON {
	counts := make([]statusCountJSON, 0, len(s.Assessments))
	for _, st := range statusOrder {
		if c, ok := s.Assessments[st]; ok {
			counts = append(counts, statusCountJSON{Status: st, Count: c})
		}
	}
	return summaryJSON{Samples: s.Samples, TargetErrors: s.TargetErrors, Assessments: counts}
}

func projectProvenance(p eval.Provenance) provenanceJSON {
	// Evaluator order is semantic (the order they were supplied) and preserved.
	evs := make([]evaluatorRevJSON, len(p.Evaluators))
	for i, e := range p.Evaluators {
		evs[i] = evaluatorRevJSON{Name: string(e.Name), Revision: string(e.Revision)}
	}
	return provenanceJSON{Suite: string(p.Suite), Target: string(p.Target), Evaluators: evs}
}

// classifyTargetError maps a target-stage error to its safe classification. It
// recognises a DecodedTargetError first so a decoded report re-encodes to the
// same class (a byte-stable fixed point), then context cancellation/deadline,
// then an observation validation failure, and otherwise reports a generic
// failure. The raw cause is never inspected for text — only classified.
func classifyTargetError(te *eval.TargetError) TargetErrorClass {
	if te == nil || te.Cause == nil {
		return TargetErrorFailed
	}
	var dec *DecodedTargetError
	if errors.As(te.Cause, &dec) {
		return dec.Class
	}
	switch {
	case errors.Is(te.Cause, context.DeadlineExceeded):
		return TargetErrorTimeout
	case errors.Is(te.Cause, context.Canceled):
		return TargetErrorCancelled
	}
	var ve *eval.ValidationError
	var ie *eval.IndexRangeError
	if errors.As(te.Cause, &ve) || errors.As(te.Cause, &ie) {
		return TargetErrorInvalidObservation
	}
	return TargetErrorFailed
}

// --- decode -----------------------------------------------------------------

// Decode reads a report/v1 document and returns the redacted report view. It is
// the untrusted boundary and the fuzz target: for any input it returns either a
// valid redacted report or a typed error, and never panics. Enforced in order:
// the size bound, valid UTF-8, exactly one JSON value (no trailing data), a known
// version, strict field decoding, finite measurement values, domain validation of
// every reconstructed assessment, and finally the whole-report invariants via
// eval.Report.Validate (identity, timestamp ordering, trial indexes, sample and
// evaluator uniqueness, and summary/provenance consistency).
func Decode(data []byte) (eval.Report, error) {
	var zero eval.Report

	if len(data) > MaxReportBytes {
		return zero, &ReportTooLargeError{Size: len(data), Max: MaxReportBytes}
	}
	if !utf8.Valid(data) {
		return zero, &MalformedReportError{Reason: reasonInvalidUTF8}
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	var env envelopeJSON
	if err := dec.Decode(&env); err != nil {
		return zero, &MalformedReportError{Reason: reasonInvalidJSON}
	}
	if dec.More() {
		return zero, &MalformedReportError{Reason: reasonTrailingData}
	}

	// Version FIRST, before the payload is trusted.
	if env.Version != version1 {
		return zero, &UnknownVersionError{Version: safeVersionToken(env.Version)}
	}
	if len(env.Report) == 0 {
		return zero, &MalformedReportError{Reason: reasonEmptyReport}
	}

	rj, err := decodeReportPayload(env.Report)
	if err != nil {
		return zero, err
	}
	report, err := reconstruct(rj)
	if err != nil {
		return zero, err
	}
	// Whole-report invariants, after every part is individually reconstructed and
	// validated: report identity, timestamp ordering, trial indexes, sample and
	// evaluator uniqueness, and summary/provenance consistency. A report whose
	// parts each validate can still be internally contradictory; reject it here so
	// the decode boundary never yields an inconsistent report.
	if err := report.Validate(); err != nil {
		return zero, &InvalidReportError{Cause: err}
	}
	return report, nil
}

// decodeReportPayload strictly decodes the deferred report payload, rejecting any
// unknown field so a malformed or hostile document cannot smuggle extra data.
func decodeReportPayload(data json.RawMessage) (reportJSON, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var rj reportJSON
	if err := dec.Decode(&rj); err != nil {
		if isUnknownField(err) {
			return reportJSON{}, &MalformedReportError{Reason: reasonUnknownField}
		}
		return reportJSON{}, &MalformedReportError{Reason: reasonInvalidJSON}
	}
	if dec.More() {
		return reportJSON{}, &MalformedReportError{Reason: reasonTrailingData}
	}
	return rj, nil
}

// reconstruct builds the strict eval.Report from its validated wire projection.
// Reconstructed assessments are validated; a target-error class is reconstructed
// into a typed DecodedTargetError. The raw Observation is not reconstructed — the
// wire form never carried it.
func reconstruct(rj reportJSON) (eval.Report, error) {
	samples := make([]eval.SampleReport, len(rj.Samples))
	for i, sj := range rj.Samples {
		s, err := reconstructSample(sj)
		if err != nil {
			return eval.Report{}, err
		}
		samples[i] = s
	}
	return eval.Report{
		ID:         rj.ID,
		Suite:      eval.Revision(rj.Suite),
		Target:     eval.Revision(rj.Target),
		StartedAt:  rj.StartedAt,
		EndedAt:    rj.EndedAt,
		Samples:    samples,
		Summary:    reconstructSummary(rj.Summary),
		Provenance: reconstructProvenance(rj.Provenance),
	}, nil
}

func reconstructSample(sj sampleJSON) (eval.SampleReport, error) {
	s := eval.SampleReport{ScenarioID: sj.ScenarioID, TrialIndex: sj.TrialIndex}
	if sj.TargetError != nil {
		if err := sj.TargetError.Validate(); err != nil {
			return eval.SampleReport{}, &InvalidReportError{Cause: err}
		}
		s.TargetErr = &eval.TargetError{Cause: &DecodedTargetError{Class: *sj.TargetError}}
	}
	if len(sj.Assessments) > 0 {
		s.Assessments = make([]eval.Assessment, len(sj.Assessments))
		for i, aj := range sj.Assessments {
			a, err := reconstructAssessment(aj)
			if err != nil {
				return eval.SampleReport{}, err
			}
			s.Assessments[i] = a
		}
	}
	return s, nil
}

func reconstructAssessment(aj assessmentJSON) (eval.Assessment, error) {
	a := eval.Assessment{
		Evaluator: eval.Name(aj.Evaluator),
		Revision:  eval.Revision(aj.Revision),
		Status:    aj.Status,
		Duration:  time.Duration(aj.DurationNanos),
	}
	if len(aj.Measurements) > 0 {
		a.Measurements = make([]eval.Measurement, len(aj.Measurements))
		for i, m := range aj.Measurements {
			if math.IsNaN(m.Value) || math.IsInf(m.Value, 0) {
				return eval.Assessment{}, &MalformedReportError{Reason: reasonNonFinite}
			}
			a.Measurements[i] = eval.Measurement{Name: eval.Name(m.Name), Value: m.Value, Unit: m.Unit}
		}
	}
	if len(aj.Findings) > 0 {
		a.Findings = make([]eval.Finding, len(aj.Findings))
		for i, f := range aj.Findings {
			a.Findings[i] = eval.Finding{Code: eval.FindingCode(f.Code), Severity: f.Severity, Evidence: f.Evidence}
		}
	}
	if len(aj.Evidence) > 0 {
		a.Evidence = append([]eval.Evidence(nil), aj.Evidence...)
	}
	if err := a.Validate(); err != nil {
		return eval.Assessment{}, &InvalidReportError{Cause: err}
	}
	return a, nil
}

func reconstructSummary(sj summaryJSON) eval.Summary {
	var counts map[eval.AssessmentStatus]int
	if len(sj.Assessments) > 0 {
		counts = make(map[eval.AssessmentStatus]int, len(sj.Assessments))
		for _, sc := range sj.Assessments {
			counts[sc.Status] = sc.Count
		}
	}
	return eval.Summary{Samples: sj.Samples, TargetErrors: sj.TargetErrors, Assessments: counts}
}

func reconstructProvenance(pj provenanceJSON) eval.Provenance {
	evs := make([]eval.EvaluatorRevision, len(pj.Evaluators))
	for i, e := range pj.Evaluators {
		evs[i] = eval.EvaluatorRevision{Name: eval.Name(e.Name), Revision: eval.Revision(e.Revision)}
	}
	return eval.Provenance{Suite: eval.Revision(pj.Suite), Target: eval.Revision(pj.Target), Evaluators: evs}
}

// Validate reports whether c is a known TargetErrorClass. The zero value is not a
// member, so an unset or unknown class is rejected fail-closed.
func (c TargetErrorClass) Validate() error {
	switch c {
	case TargetErrorTimeout, TargetErrorCancelled, TargetErrorInvalidObservation, TargetErrorFailed:
		return nil
	default:
		return &eval.ValidationError{Field: "TargetErrorClass", Reason: "unknown class"}
	}
}

// isUnknownField reports whether a json decode error was an unknown-field
// rejection from DisallowUnknownFields. encoding/json signals this only via the
// error string, so this is the one place a string match is unavoidable; it
// classifies the codec's OWN decoder output, never untrusted content.
func isUnknownField(err error) bool {
	return err != nil && bytes.Contains([]byte(err.Error()), []byte("unknown field"))
}
