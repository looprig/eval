package eval

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/looprig/core/content"
)

// userInput returns a minimal one-message thread suitable as scenario input.
func userInput(text string) content.AgenticMessages {
	return content.AgenticMessages{
		&content.UserMessage{Message: content.Message{
			Role:   content.RoleUser,
			Blocks: []content.Block{&content.TextBlock{Text: text}},
		}},
	}
}

// newValidScenario builds a fully valid scenario whose declared Revision matches
// the subject revision of newValidObservation ("2026-07").
func newValidScenario() Scenario {
	return Scenario{
		ID:       "refund-policy-017",
		Name:     "refund-policy",
		Revision: "2026-07",
		Input:    userInput("Refund this non-refundable invoice"),
		Labels: []Label{
			{Key: "suite", Value: "billing"},
			{Key: "risk", Value: "high"},
		},
	}
}

func intVal(i int) *int { return &i }

func TestScenarioValidateHappyPath(t *testing.T) {
	t.Parallel()
	if err := newValidScenario().Validate(); err != nil {
		t.Fatalf("valid scenario rejected: %v", err)
	}
}

func TestScenarioValidateIdentity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mutate  func(*Scenario)
		wantErr bool
	}{
		{
			name:    "stable non-empty id validates",
			mutate:  func(s *Scenario) {},
			wantErr: false,
		},
		{
			name:    "empty id rejected",
			mutate:  func(s *Scenario) { s.ID = "" },
			wantErr: true,
		},
		{
			name:    "oversized id rejected",
			mutate:  func(s *Scenario) { s.ID = strings.Repeat("x", MaxIDBytes+1) },
			wantErr: true,
		},
		{
			name:    "empty name rejected",
			mutate:  func(s *Scenario) { s.Name = "" },
			wantErr: true,
		},
		{
			name:    "empty revision rejected",
			mutate:  func(s *Scenario) { s.Revision = "" },
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := newValidScenario()
			tt.mutate(&s)
			err := s.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil && isBareError(err) {
				t.Fatalf("Validate() returned bare error %v; want typed error", err)
			}
		})
	}
}

func TestScenarioValidateEmptyInput(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name  string
		input content.AgenticMessages
	}{
		{name: "nil input", input: nil},
		{name: "empty input", input: content.AgenticMessages{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := newValidScenario()
			s.Input = tt.input
			if err := s.Validate(); err == nil {
				t.Fatal("Validate() = nil, want error for empty input")
			} else if isBareError(err) {
				t.Fatalf("Validate() returned bare error %v; want typed error", err)
			}
		})
	}
}

func TestScenarioValidateDuplicateLabels(t *testing.T) {
	t.Parallel()

	s := newValidScenario()
	s.Labels = append(s.Labels, Label{Key: "suite", Value: "other"})
	err := s.Validate()
	if err == nil {
		t.Fatal("Validate() = nil, want error for duplicate label key")
	}
	var dup *DuplicateLabelError
	if !errors.As(err, &dup) {
		t.Fatalf("Validate() error = %v, want *DuplicateLabelError", err)
	}
	// The duplicate key value must not leak into the diagnostic.
	assertNoUntrustedEcho(t, err, "suite")
}

func TestScenarioValidateInvalidLabel(t *testing.T) {
	t.Parallel()

	s := newValidScenario()
	s.Labels = []Label{{Key: "", Value: "x"}}
	if err := s.Validate(); err == nil {
		t.Fatal("Validate() = nil, want error for empty label key")
	} else if isBareError(err) {
		t.Fatalf("Validate() returned bare error %v; want typed error", err)
	}
}

func TestScenarioValidateOptionalExpectation(t *testing.T) {
	t.Parallel()

	// nil expectation is valid.
	s := newValidScenario()
	s.Expectation = nil
	if err := s.Validate(); err != nil {
		t.Fatalf("nil expectation rejected: %v", err)
	}

	// A wholly-empty expectation is valid optional data.
	s.Expectation = &Expectation{}
	if err := s.Validate(); err != nil {
		t.Fatalf("empty expectation rejected: %v", err)
	}
}

func TestExpectationValidateRequiredFacts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		facts   []Fact
		wantErr bool
	}{
		{name: "valid facts", facts: []Fact{"invoice is non-refundable", "no refund issued"}, wantErr: false},
		{name: "empty fact", facts: []Fact{""}, wantErr: true},
		{name: "one empty among valid", facts: []Fact{"ok", ""}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := &Expectation{RequiredFacts: tt.facts}
			err := e.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil && isBareError(err) {
				t.Fatalf("bare error %v; want typed", err)
			}
		})
	}
}

func TestExpectationValidateForbiddenActions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		actions []ActionName
		wantErr bool
	}{
		{name: "valid action", actions: []ActionName{"issue_refund"}, wantErr: false},
		{name: "empty action", actions: []ActionName{""}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := &Expectation{ForbiddenActions: tt.actions}
			err := e.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil && isBareError(err) {
				t.Fatalf("bare error %v; want typed", err)
			}
		})
	}
}

func TestExpectationValidateToolCalls(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		calls   []ToolCallExpectation
		wantErr bool
	}{
		{name: "valid tool call", calls: []ToolCallExpectation{{Tool: "lookup_account", MinCount: 1}}, wantErr: false},
		{name: "valid with max", calls: []ToolCallExpectation{{Tool: "lookup_account", MinCount: 1, MaxCount: intVal(3)}}, wantErr: false},
		{name: "empty tool name", calls: []ToolCallExpectation{{Tool: "", MinCount: 1}}, wantErr: true},
		{name: "negative min", calls: []ToolCallExpectation{{Tool: "t", MinCount: -1}}, wantErr: true},
		{name: "max below min", calls: []ToolCallExpectation{{Tool: "t", MinCount: 2, MaxCount: intVal(1)}}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := &Expectation{ExpectedToolCalls: tt.calls}
			err := e.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil && isBareError(err) {
				t.Fatalf("bare error %v; want typed", err)
			}
		})
	}
}

func TestExpectationValidateStructuredOutput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		out     *StructuredOutputExpectation
		wantErr bool
	}{
		{name: "valid schema", out: &StructuredOutputExpectation{Schema: "refund-decision-v1", Strict: true}, wantErr: false},
		{name: "empty schema", out: &StructuredOutputExpectation{Schema: ""}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := &Expectation{StructuredOutput: tt.out}
			err := e.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil && isBareError(err) {
				t.Fatalf("bare error %v; want typed", err)
			}
		})
	}
}

// fakeTarget is a Target whose Observe returns a preconfigured observation.
type fakeTarget struct {
	name string
	obs  Observation
	err  error
}

func (f fakeTarget) Name() string { return f.name }

func (f fakeTarget) Observe(_ context.Context, _ Scenario) (Observation, error) {
	return f.obs, f.err
}

func TestSampleValidateFromTarget(t *testing.T) {
	t.Parallel()

	scen := newValidScenario()
	tgt := fakeTarget{name: "agent", obs: newValidObservation()}

	obs, err := tgt.Observe(context.Background(), scen)
	if err != nil {
		t.Fatalf("Observe() error = %v", err)
	}

	sample := Sample{Scenario: &scen, Observation: obs}
	if err := sample.Validate(); err != nil {
		t.Fatalf("valid sample rejected: %v", err)
	}
}

func TestSampleValidateSubjectMismatch(t *testing.T) {
	t.Parallel()

	scen := newValidScenario()
	scen.Revision = "2099-01" // does not match the observation subject revision

	sample := Sample{Scenario: &scen, Observation: newValidObservation()}
	err := sample.Validate()
	if err == nil {
		t.Fatal("Validate() = nil, want subject-revision mismatch error")
	}
	var mm *SampleSubjectMismatchError
	if !errors.As(err, &mm) {
		t.Fatalf("Validate() error = %v, want *SampleSubjectMismatchError", err)
	}
	// Neither the scenario nor the subject revision may leak into diagnostics.
	assertNoUntrustedEcho(t, err, "2099-01")
	assertNoUntrustedEcho(t, err, "2026-07")
}

func TestSampleValidateContinuousNoScenario(t *testing.T) {
	t.Parallel()

	// Pure continuous observation: no scenario, only the observation is checked.
	sample := Sample{Observation: newValidObservation()}
	if err := sample.Validate(); err != nil {
		t.Fatalf("continuous sample rejected: %v", err)
	}

	// An invalid observation is still rejected without a scenario.
	bad := newValidObservation()
	bad.Subject.Kind = SubjectKind("wombat")
	sample = Sample{Observation: bad}
	if err := sample.Validate(); err == nil {
		t.Fatal("Validate() = nil, want error for invalid observation")
	}
}

func TestObservationValidateExpectation(t *testing.T) {
	t.Parallel()

	// A valid expectation on an observation validates.
	obs := newValidObservation()
	obs.Expectation = &Expectation{ForbiddenActions: []ActionName{"issue_refund"}}
	if err := obs.Validate(); err != nil {
		t.Fatalf("observation with valid expectation rejected: %v", err)
	}

	// An invalid expectation is rejected by Observation.Validate.
	obs.Expectation = &Expectation{RequiredFacts: []Fact{""}}
	if err := obs.Validate(); err == nil {
		t.Fatal("Validate() = nil, want error for invalid expectation")
	} else if isBareError(err) {
		t.Fatalf("bare error %v; want typed", err)
	}
}
