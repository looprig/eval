package exact

import (
	"testing"

	"github.com/looprig/core/content"
	"github.com/looprig/eval"
)

func TestSchemaResult(t *testing.T) {
	t.Parallel()

	conv := content.AgenticMessages{userText("classify this"), aiText(`{"label":"spam"}`)}

	tests := []struct {
		name       string
		evidence   []eval.Evidence
		wantStatus eval.AssessmentStatus
	}{
		{
			name:       "usage present, no structured error, satisfied schema",
			evidence:   []eval.Evidence{usageEv("u-1")},
			wantStatus: eval.StatusPass,
		},
		{
			name:       "structured error present, failed schema",
			evidence:   []eval.Evidence{usageEv("u-1"), structErrEv("se-1", eval.StructuredErrorSchemaMismatch)},
			wantStatus: eval.StatusFail,
		},
		{
			name:       "required usage evidence absent, unverified",
			evidence:   nil,
			wantStatus: eval.StatusUnverified,
		},
		{
			name:       "only a structured error but no required usage evidence, unverified",
			evidence:   []eval.Evidence{structErrEv("se-1", eval.StructuredErrorInvalidJSON)},
			wantStatus: eval.StatusUnverified,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a := evaluate(t, SchemaResult(), sampleOf(obs(conv, tt.evidence...)))
			if a.Status != tt.wantStatus {
				t.Fatalf("status = %q, want %q", a.Status, tt.wantStatus)
			}
			if a.Status == eval.StatusPass {
				return
			}
			// A non-pass must never be a silently inferred pass; a fail must cite
			// the structured-error evidence.
			if a.Status == eval.StatusFail && !findingHasResolvingEvidence(t, a) {
				t.Fatal("fail assessment carries a finding without a resolving evidence reference")
			}
		})
	}
}

func TestSchemaResultUnverifiedNeverPass(t *testing.T) {
	t.Parallel()
	a := evaluate(t, SchemaResult(), sampleOf(obs(content.AgenticMessages{aiText("no output recorded")})))
	if a.Status == eval.StatusPass {
		t.Fatal("missing required evidence must never produce a pass")
	}
	if a.Status != eval.StatusUnverified {
		t.Fatalf("status = %q, want %q", a.Status, eval.StatusUnverified)
	}
}
