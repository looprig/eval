// Package compare diffs a candidate eval report against a baseline. It operates
// only over the safe report fields (identities, statuses, finite measurements)
// and never touches raw conversation content, finding messages, or target-error
// causes, so its output carries no untrusted content.
//
// A case is one (scenario, evaluator-name) identity. Because a report retains
// every per-(scenario, trial) sample, a case gathers all of an evaluator's
// assessments across trials on both sides, so repeated-trial variance stays
// visible: the individual trial results are retained, not just an aggregate mean.
//
// Two matched cases are COMPATIBLE only when their evaluator revisions agree.
// Score distributions are compared only for compatible cases; an incompatible
// case (the evaluator changed revision between runs) is surfaced as such and
// never silently averaged across the revision boundary. Comparison classifies
// each case as added, removed, incompatible, errored, unverified, failed,
// changed, or unchanged — it does not reduce a run to a single mean.
package compare

import (
	"math"
	"sort"

	"github.com/looprig/eval"
)

// CaseClass is the classification of one compared case. It is a closed set.
type CaseClass string

const (
	// CaseAdded: the case exists in the candidate but not the baseline.
	CaseAdded CaseClass = "added"
	// CaseRemoved: the case exists in the baseline but not the candidate.
	CaseRemoved CaseClass = "removed"
	// CaseIncompatible: the case exists in both but the evaluator revision
	// changed, so the two are not comparable and distributions are not compared.
	CaseIncompatible CaseClass = "incompatible"
	// CaseErrored: the candidate case has an error-status assessment — the
	// evaluator failed to reach a verdict. Surfaced distinctly from a regression.
	CaseErrored CaseClass = "errored"
	// CaseUnverified: the candidate case has an unverified assessment (and no
	// error). Unknown is never a pass.
	CaseUnverified CaseClass = "unverified"
	// CaseFailed: the candidate case has a failing verdict (and no error or
	// unverified). A standing quality failure.
	CaseFailed CaseClass = "failed"
	// CaseChanged: the case passes in the candidate but its outcome or score
	// distribution differs from the baseline.
	CaseChanged CaseClass = "changed"
	// CaseUnchanged: the case passes in both with an equivalent distribution.
	CaseUnchanged CaseClass = "unchanged"
)

// CaseKey identifies a compared case: a scenario and an evaluator name. The
// evaluator revision is a compatibility attribute, not part of the key, so a
// revision bump is reported as an incompatible case rather than as an unrelated
// add/remove pair.
type CaseKey struct {
	ScenarioID string
	Evaluator  eval.Name
}

// TrialResult is one evaluator assessment on one trial, retained so per-trial
// variance is visible. Measurements are the safe (name/value/unit) triples.
type TrialResult struct {
	TrialIndex   int
	Status       eval.AssessmentStatus
	Measurements []eval.Measurement
}

// Distribution summarises a measurement across a case's trials.
type Distribution struct {
	Count int
	Mean  float64
	Min   float64
	Max   float64
}

// MeasurementDelta pairs a measurement's baseline and candidate distributions.
// It is populated only for compatible cases.
//
// A measurement name may appear on both sides carrying DIFFERENT units (for
// example latency in seconds on the baseline and a bare count on the candidate).
// Numbers measured in different units are not comparable, so the two units are
// tracked separately and never collapsed: BaselineUnit and CandidateUnit expose
// each side's unit, and UnitMismatch flags the incompatibility. Unit is the
// agreed unit when both sides match (and the baseline's when they do not),
// retained for the common compatible case.
type MeasurementDelta struct {
	Name          eval.Name
	Unit          eval.Unit
	BaselineUnit  eval.Unit
	CandidateUnit eval.Unit
	UnitMismatch  bool
	Baseline      Distribution
	Candidate     Distribution
}

// CaseComparison is the diff of one case. Baseline and Candidate hold the
// retained per-trial results; Distributions is non-empty only for a compatible
// case that carries measurements.
type CaseComparison struct {
	Key               CaseKey
	Class             CaseClass
	Compatible        bool
	BaselineRevision  eval.Revision
	CandidateRevision eval.Revision
	Baseline          []TrialResult
	Candidate         []TrialResult
	Distributions     []MeasurementDelta
}

// Comparison is the full baseline-vs-candidate diff: one entry per case, in a
// canonical order.
type Comparison struct {
	Cases []CaseComparison
}

// Compare diffs candidate against baseline. It fails closed on a non-finite
// measurement value (a report should never contain one; comparison rejects it
// rather than propagate a poisoned aggregate) and otherwise returns a per-case
// diff that retains individual trial results and compares distributions only for
// compatible cases.
func Compare(baseline, candidate eval.Report) (Comparison, error) {
	base, err := index(baseline)
	if err != nil {
		return Comparison{}, err
	}
	cand, err := index(candidate)
	if err != nil {
		return Comparison{}, err
	}

	keys := unionKeys(base, cand)
	cases := make([]CaseComparison, 0, len(keys))
	for _, k := range keys {
		cases = append(cases, compareCase(k, base[k], cand[k]))
	}
	sort.Slice(cases, func(i, j int) bool {
		if cases[i].Key.ScenarioID != cases[j].Key.ScenarioID {
			return cases[i].Key.ScenarioID < cases[j].Key.ScenarioID
		}
		return cases[i].Key.Evaluator < cases[j].Key.Evaluator
	})
	return Comparison{Cases: cases}, nil
}

// caseData is a case's gathered assessments on one side of the comparison.
type caseData struct {
	revision eval.Revision
	trials   []TrialResult
}

// index gathers a report's assessments into per-case data, validating that every
// measurement value is finite.
func index(r eval.Report) (map[CaseKey]*caseData, error) {
	out := make(map[CaseKey]*caseData)
	for si := range r.Samples {
		s := r.Samples[si]
		for ai := range s.Assessments {
			a := s.Assessments[ai]
			for _, m := range a.Measurements {
				if math.IsNaN(m.Value) || math.IsInf(m.Value, 0) {
					return nil, &NonFiniteMeasurementError{}
				}
			}
			key := CaseKey{ScenarioID: s.ScenarioID, Evaluator: a.Evaluator}
			cd := out[key]
			if cd == nil {
				cd = &caseData{revision: a.Revision}
				out[key] = cd
			}
			cd.trials = append(cd.trials, TrialResult{
				TrialIndex:   s.TrialIndex,
				Status:       a.Status,
				Measurements: append([]eval.Measurement(nil), a.Measurements...),
			})
		}
	}
	return out, nil
}

func unionKeys(a, b map[CaseKey]*caseData) []CaseKey {
	seen := make(map[CaseKey]struct{}, len(a)+len(b))
	keys := make([]CaseKey, 0, len(a)+len(b))
	for k := range a {
		if _, ok := seen[k]; !ok {
			seen[k] = struct{}{}
			keys = append(keys, k)
		}
	}
	for k := range b {
		if _, ok := seen[k]; !ok {
			seen[k] = struct{}{}
			keys = append(keys, k)
		}
	}
	return keys
}

// compareCase classifies a single case from its baseline and candidate data
// (either may be nil when the case is present on only one side).
func compareCase(key CaseKey, base, cand *caseData) CaseComparison {
	cc := CaseComparison{Key: key}
	if base != nil {
		cc.BaselineRevision = base.revision
		cc.Baseline = base.trials
	}
	if cand != nil {
		cc.CandidateRevision = cand.revision
		cc.Candidate = cand.trials
	}

	switch {
	case base == nil:
		cc.Class = CaseAdded
		return cc
	case cand == nil:
		cc.Class = CaseRemoved
		return cc
	}

	// Present on both sides. Distributions are only comparable across the same
	// evaluator revision.
	if base.revision != cand.revision {
		cc.Compatible = false
		cc.Class = CaseIncompatible
		return cc
	}

	cc.Compatible = true
	cc.Distributions = distributions(base.trials, cand.trials)
	cc.Class = classify(base.trials, cand.trials, cc.Distributions)
	return cc
}

// outcome ranks a side's assessments into a single representative status using
// the precedence error > unverified > fail > pass > skipped. The most serious
// signal wins so an error or unverified in any trial is never hidden by a
// passing sibling.
func outcome(trials []TrialResult) eval.AssessmentStatus {
	rank := map[eval.AssessmentStatus]int{
		eval.StatusError: 5, eval.StatusUnverified: 4, eval.StatusFail: 3,
		eval.StatusPass: 2, eval.StatusSkipped: 1,
	}
	best := eval.AssessmentStatus("")
	bestRank := 0
	for _, tr := range trials {
		if r := rank[tr.Status]; r > bestRank {
			bestRank = r
			best = tr.Status
		}
	}
	return best
}

// classify determines the class of a compatible, both-present case. Candidate
// error/unverified/fail states are surfaced first (a failure is not a quality
// improvement); otherwise a passing candidate is Changed when its outcome or a
// score distribution differs from the baseline, and Unchanged when it matches.
func classify(base, cand []TrialResult, deltas []MeasurementDelta) CaseClass {
	switch outcome(cand) {
	case eval.StatusError:
		return CaseErrored
	case eval.StatusUnverified:
		return CaseUnverified
	case eval.StatusFail:
		return CaseFailed
	}
	if outcome(base) != outcome(cand) {
		return CaseChanged
	}
	for _, d := range deltas {
		// A unit mismatch makes the two sides incomparable: equal raw numbers in
		// different units are NOT unchanged. Never let a mismatch fall through to
		// CaseUnchanged.
		if d.UnitMismatch || d.Baseline != d.Candidate {
			return CaseChanged
		}
	}
	return CaseUnchanged
}

// distributions computes per-measurement baseline/candidate distributions over a
// case's trials, one entry per measurement name that appears on either side, in
// canonical name order.
func distributions(base, cand []TrialResult) []MeasurementDelta {
	baseAgg := aggregate(base)
	candAgg := aggregate(cand)

	names := make(map[eval.Name]struct{}, len(baseAgg)+len(candAgg))
	for n := range baseAgg {
		names[n] = struct{}{}
	}
	for n := range candAgg {
		names[n] = struct{}{}
	}
	if len(names) == 0 {
		return nil
	}

	ordered := make([]eval.Name, 0, len(names))
	for n := range names {
		ordered = append(ordered, n)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })

	out := make([]MeasurementDelta, 0, len(ordered))
	for _, n := range ordered {
		bAcc, onBase := baseAgg[n]
		cAcc, onCand := candAgg[n]

		var baseUnit, candUnit eval.Unit
		if onBase {
			baseUnit = bAcc.unit
		}
		if onCand {
			candUnit = cAcc.unit
		}
		// UnitMismatch flags any incomparability of the two distributions:
		//   - a cross-side disagreement (present on both sides with differing units), or
		//   - intra-side drift on either side (a side that switched a measurement's
		//     unit across its own trials, whose values are not internally comparable).
		// Either makes the raw numbers meaningless to compare, so classify treats the
		// case as Changed rather than letting equal means read as Unchanged.
		crossMismatch := onBase && onCand && baseUnit != candUnit
		baseDrift := onBase && !bAcc.unitConsistent
		candDrift := onCand && !cAcc.unitConsistent
		mismatch := crossMismatch || baseDrift || candDrift

		// Unit records the agreed unit for the common compatible case; when the
		// units differ it falls back to the baseline's (never silently the
		// candidate's, which was the last-writer-wins bug), with the divergence
		// made explicit through BaselineUnit/CandidateUnit/UnitMismatch.
		agreed := baseUnit
		if !onBase {
			agreed = candUnit
		}

		var baseDist, candDist Distribution
		if onBase {
			baseDist = bAcc.distribution()
		}
		if onCand {
			candDist = cAcc.distribution()
		}

		out = append(out, MeasurementDelta{
			Name:          n,
			Unit:          agreed,
			BaselineUnit:  baseUnit,
			CandidateUnit: candUnit,
			UnitMismatch:  mismatch,
			Baseline:      baseDist,
			Candidate:     candDist,
		})
	}
	return out
}

// accumulator collects a single measurement's values across trials. unit is the
// representative (first-seen) unit; unitConsistent reports whether every trial's
// measurement for this name carried that same unit. A side that switches a
// measurement's unit between trials (for example latency reported in seconds on
// one trial and as a bare count on another) is not internally comparable, so its
// values must never be silently summed or averaged across the drift.
type accumulator struct {
	unit           eval.Unit
	unitConsistent bool
	count          int
	sum            float64
	min            float64
	max            float64
}

func (a accumulator) distribution() Distribution {
	if a.count == 0 {
		return Distribution{}
	}
	return Distribution{Count: a.count, Mean: a.sum / float64(a.count), Min: a.min, Max: a.max}
}

// aggregate folds a side's trials into per-measurement accumulators.
func aggregate(trials []TrialResult) map[eval.Name]*accumulator {
	out := make(map[eval.Name]*accumulator)
	for _, tr := range trials {
		for _, m := range tr.Measurements {
			acc := out[m.Name]
			if acc == nil {
				acc = &accumulator{unit: m.Unit, unitConsistent: true, min: m.Value, max: m.Value}
				out[m.Name] = acc
			}
			// Intra-side unit drift: a later trial reports this measurement in a
			// different unit than the first. Keep the first unit as representative but
			// record the drift so the distribution is flagged, never silently merged.
			if m.Unit != acc.unit {
				acc.unitConsistent = false
			}
			acc.count++
			acc.sum += m.Value
			if m.Value < acc.min {
				acc.min = m.Value
			}
			if m.Value > acc.max {
				acc.max = m.Value
			}
		}
	}
	return out
}
