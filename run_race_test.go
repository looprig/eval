package eval

import (
	"context"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

// This file concentrates the concurrency, determinism, and input-immutability
// assertions. The whole package test suite runs under -race, so a data race in
// the runner (a goroutine appending to a shared slice, an unsynchronised slot
// write) fails here.

// sampleKey identifies a sample report by its stable derived identity.
type sampleKey struct {
	id    string
	trial int
}

func keysOf(report Report) []sampleKey {
	keys := make([]sampleKey, 0, len(report.Samples))
	for _, s := range report.Samples {
		keys = append(keys, sampleKey{s.ScenarioID, s.TrialIndex})
	}
	return keys
}

// TestRunDeterministicOrdering asserts the report sample order is identical for
// sequential and highly concurrent runs, and matches the canonical
// scenario-major, trial-minor order.
func TestRunDeterministicOrdering(t *testing.T) {
	t.Parallel()
	suite := runSuite("a", "b", "c", "d", "e")
	cfg1 := RunConfig{Trials: 3, Concurrency: 1}
	cfgN := RunConfig{Trials: 3, Concurrency: 8}

	seq, err := Run(context.Background(), cfg1, suite, okTarget(), passEvaluator("q1"), passEvaluator("q2"))
	if err != nil {
		t.Fatalf("sequential Run error: %v", err)
	}
	conc, err := Run(context.Background(), cfgN, suite, okTarget(), passEvaluator("q1"), passEvaluator("q2"))
	if err != nil {
		t.Fatalf("concurrent Run error: %v", err)
	}

	seqKeys := keysOf(seq)
	concKeys := keysOf(conc)
	if !reflect.DeepEqual(seqKeys, concKeys) {
		t.Fatalf("ordering differs:\n seq=%v\nconc=%v", seqKeys, concKeys)
	}

	// Canonical order check.
	var want []sampleKey
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		for tr := 0; tr < 3; tr++ {
			want = append(want, sampleKey{id, tr})
		}
	}
	if !reflect.DeepEqual(seqKeys, want) {
		t.Fatalf("canonical ordering mismatch:\n got=%v\nwant=%v", seqKeys, want)
	}

	// Assessment order within each sample is stable too.
	for _, s := range conc.Samples {
		if len(s.Assessments) != 2 {
			t.Fatalf("sample %q trial %d: got %d assessments, want 2", s.ScenarioID, s.TrialIndex, len(s.Assessments))
		}
		if s.Assessments[0].Evaluator != "q1" || s.Assessments[1].Evaluator != "q2" {
			t.Fatalf("assessment order not stable: %q,%q", s.Assessments[0].Evaluator, s.Assessments[1].Evaluator)
		}
	}
}

// TestRunBoundedConcurrency asserts no more than Concurrency targets run at once.
func TestRunBoundedConcurrency(t *testing.T) {
	t.Parallel()
	const limit = 3
	var inFlight int32
	var maxSeen int32
	target := stubTarget{name: "counting", observe: func(context.Context, Scenario) (Observation, error) {
		cur := atomic.AddInt32(&inFlight, 1)
		for {
			old := atomic.LoadInt32(&maxSeen)
			if cur <= old || atomic.CompareAndSwapInt32(&maxSeen, old, cur) {
				break
			}
		}
		time.Sleep(2 * time.Millisecond)
		atomic.AddInt32(&inFlight, -1)
		return runObservation(), nil
	}}
	suite := runSuite("a", "b", "c", "d", "e", "f", "g", "h", "i", "j")
	cfg := RunConfig{Concurrency: limit, Trials: 2}
	report, err := Run(context.Background(), cfg, suite, target, passEvaluator("q"))
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if len(report.Samples) != 20 {
		t.Fatalf("got %d samples, want 20", len(report.Samples))
	}
	if got := atomic.LoadInt32(&maxSeen); got > limit {
		t.Fatalf("max concurrent targets = %d, want <= %d", got, limit)
	}
}

// TestRunInputImmutable asserts Run does not mutate the caller's suite or
// scenarios. It compares against an independently constructed identical suite.
func TestRunInputImmutable(t *testing.T) {
	t.Parallel()
	suite := runSuite("a", "b", "c")
	want := runSuite("a", "b", "c")
	// A mutating target would only affect caller data if Run passed shared
	// backing; this asserts Run itself leaves the input untouched.
	_, err := Run(context.Background(), RunConfig{Trials: 2, Concurrency: 4}, suite, okTarget(), passEvaluator("q"))
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if !reflect.DeepEqual(suite, want) {
		t.Fatalf("Run mutated its input suite:\n got=%+v\nwant=%+v", suite, want)
	}
}
