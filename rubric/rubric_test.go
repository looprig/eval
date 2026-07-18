package rubric_test

import (
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/looprig/eval"
	"github.com/looprig/eval/rubric"
)

// validRubric returns a minimal well-formed rubric the tests mutate to exercise
// each validation rule in isolation.
func validRubric() rubric.Rubric {
	return rubric.Rubric{
		Name:       "answer_relevance",
		Revision:   "v1",
		Scope:      eval.ScopeCase,
		Definition: "Judges whether the response addresses the question.",
		Criteria: []rubric.Criterion{
			{ID: "directness", Description: "engages the question", MinScore: 0, MaxScore: 1},
			{ID: "completeness", Description: "covers the request", MinScore: 0, MaxScore: 1},
		},
		Anchors: []rubric.Anchor{
			{Score: 0, Label: "poor", Description: "ignores the question"},
			{Score: 1, Label: "excellent", Description: "fully addresses it"},
		},
	}
}

func TestRubricValidateHappyPath(t *testing.T) {
	t.Parallel()
	if err := validRubric().Validate(); err != nil {
		t.Fatalf("valid rubric failed Validate(): %v", err)
	}
}

func TestRubricValidateRejects(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mutate  func(*rubric.Rubric)
		wantDup bool // expect a *DuplicateCriterionError rather than a *ValidationError
	}{
		{
			name:   "empty name",
			mutate: func(r *rubric.Rubric) { r.Name = "" },
		},
		{
			name:   "empty revision",
			mutate: func(r *rubric.Rubric) { r.Revision = "" },
		},
		{
			name:   "unknown scope",
			mutate: func(r *rubric.Rubric) { r.Scope = eval.Scope(200) },
		},
		{
			name:   "empty definition",
			mutate: func(r *rubric.Rubric) { r.Definition = "" },
		},
		{
			name:   "oversized definition",
			mutate: func(r *rubric.Rubric) { r.Definition = strings.Repeat("x", rubric.MaxDefinitionBytes+1) },
		},
		{
			name:   "no criteria",
			mutate: func(r *rubric.Rubric) { r.Criteria = nil },
		},
		{
			name: "criterion invalid score range (min == max)",
			mutate: func(r *rubric.Rubric) {
				r.Criteria[0].MinScore = 1
				r.Criteria[0].MaxScore = 1
			},
		},
		{
			name: "criterion invalid score range (min > max)",
			mutate: func(r *rubric.Rubric) {
				r.Criteria[0].MinScore = 1
				r.Criteria[0].MaxScore = 0
			},
		},
		{
			name: "criterion non-finite score",
			mutate: func(r *rubric.Rubric) {
				r.Criteria[0].MaxScore = math.Inf(1)
			},
		},
		{
			name: "duplicate criterion id",
			mutate: func(r *rubric.Rubric) {
				r.Criteria[1].ID = r.Criteria[0].ID
			},
			wantDup: true,
		},
		{
			name: "anchor score above range",
			mutate: func(r *rubric.Rubric) {
				r.Anchors = append(r.Anchors, rubric.Anchor{Score: 2, Label: "over", Description: "too high"})
			},
		},
		{
			name: "anchor score below range",
			mutate: func(r *rubric.Rubric) {
				r.Anchors = append(r.Anchors, rubric.Anchor{Score: -1, Label: "under", Description: "too low"})
			},
		},
		{
			name: "anchor non-finite score",
			mutate: func(r *rubric.Rubric) {
				r.Anchors = append(r.Anchors, rubric.Anchor{Score: math.NaN(), Label: "nan", Description: "not a number"})
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := validRubric()
			tt.mutate(&r)
			err := r.Validate()
			if err == nil {
				t.Fatalf("expected Validate() to reject %s", tt.name)
			}
			if tt.wantDup {
				var dup *rubric.DuplicateCriterionError
				if !errors.As(err, &dup) {
					t.Fatalf("error = %v, want *DuplicateCriterionError", err)
				}
				return
			}
			var ve *rubric.ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("error = %v, want *ValidationError", err)
			}
		})
	}
}

func TestRubricScoreRangeAndThreshold(t *testing.T) {
	t.Parallel()
	r := validRubric()
	minScore, maxScore := r.ScoreRange()
	if minScore != 0 || maxScore != 1 {
		t.Fatalf("ScoreRange() = (%v, %v), want (0, 1)", minScore, maxScore)
	}
	if got := r.PassThreshold(); got != 0.5 {
		t.Fatalf("PassThreshold() = %v, want 0.5", got)
	}
}

func TestCatalogValid(t *testing.T) {
	t.Parallel()

	entries := map[string]rubric.Rubric{
		"AnswerRelevanceV1":            rubric.AnswerRelevanceV1,
		"GroundednessV1":               rubric.GroundednessV1,
		"InstructionAdherenceV1":       rubric.InstructionAdherenceV1,
		"GoalAdherenceV1":              rubric.GoalAdherenceV1,
		"ToxicityV1":                   rubric.ToxicityV1,
		"VulgarityV1":                  rubric.VulgarityV1,
		"InternetUseAppropriatenessV1": rubric.InternetUseAppropriatenessV1,
	}

	if len(rubric.Catalog()) != len(entries) {
		t.Fatalf("Catalog() has %d entries, want %d", len(rubric.Catalog()), len(entries))
	}

	for name, r := range entries {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := r.Validate(); err != nil {
				t.Fatalf("%s failed Validate(): %v", name, err)
			}
			if r.Revision != "v1" {
				t.Fatalf("%s revision = %q, want stable %q", name, r.Revision, "v1")
			}
			// Every built-in scores on the uniform [0,1] scale.
			if minScore, maxScore := r.ScoreRange(); minScore != 0 || maxScore != 1 {
				t.Fatalf("%s ScoreRange() = (%v, %v), want (0, 1)", name, minScore, maxScore)
			}
		})
	}
}

func TestCatalogNamesUnique(t *testing.T) {
	t.Parallel()
	seen := make(map[eval.Name]struct{})
	for _, r := range rubric.Catalog() {
		if _, dup := seen[r.Name]; dup {
			t.Fatalf("duplicate rubric name in catalog: %q", r.Name)
		}
		seen[r.Name] = struct{}{}
	}
}
