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
			// Regression for the false positive: an ordinary response emits usage
			// evidence for ANY completion. Usage alone is NOT proof of structured
			// output, so this must be Unverified — never Pass.
			name:       "only generic usage, no structured-output evidence, unverified",
			evidence:   []eval.Evidence{usageEv("u-1")},
			wantStatus: eval.StatusUnverified,
		},
		{
			name:       "structured error present, failed schema",
			evidence:   []eval.Evidence{usageEv("u-1"), structErrEv("se-1", eval.StructuredErrorSchemaMismatch)},
			wantStatus: eval.StatusFail,
		},
		{
			name:       "positive structured-output evidence, passed schema",
			evidence:   []eval.Evidence{usageEv("u-1"), structOutEv("so-1", "score", "v1")},
			wantStatus: eval.StatusPass,
		},
		{
			name:       "no evidence at all, unverified",
			evidence:   nil,
			wantStatus: eval.StatusUnverified,
		},
		{
			// A structured error alone (no usage) is still a schema failure: Fail.
			name:       "structured error alone, failed schema",
			evidence:   []eval.Evidence{structErrEv("se-1", eval.StructuredErrorInvalidJSON)},
			wantStatus: eval.StatusFail,
		},
		{
			// Error takes precedence over a positive signal: a failure is never
			// masked by a success in the same trace.
			name:       "error and positive both present, error wins",
			evidence:   []eval.Evidence{structOutEv("so-1", "score", "v1"), structErrEv("se-1", eval.StructuredErrorMissingField)},
			wantStatus: eval.StatusFail,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a := evaluate(t, SchemaResult(), sampleOf(obs(conv, tt.evidence...)))
			if a.Status != tt.wantStatus {
				t.Fatalf("status = %q, want %q", a.Status, tt.wantStatus)
			}
			// Both a fail and a pass must cite a resolving evidence reference; a
			// verdict is never left bare over its supporting evidence.
			if a.Status == eval.StatusFail || a.Status == eval.StatusPass {
				if !findingHasResolvingEvidence(t, a) {
					t.Fatalf("%s assessment carries a finding without a resolving evidence reference", a.Status)
				}
			}
		})
	}
}

func TestSchemaResultUnverifiedNeverPass(t *testing.T) {
	t.Parallel()
	a := evaluate(t, SchemaResult(), sampleOf(obs(content.AgenticMessages{aiText("no output recorded")})))
	if a.Status == eval.StatusPass {
		t.Fatal("absent structured-output evidence must never produce a pass")
	}
	if a.Status != eval.StatusUnverified {
		t.Fatalf("status = %q, want %q", a.Status, eval.StatusUnverified)
	}
}
