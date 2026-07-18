package exact

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/looprig/core/content"
	"github.com/looprig/eval"
)

// --- shared test helpers (used across the exact evaluator test files) ---

// userText builds a user message carrying a single text block.
func userText(s string) *content.UserMessage {
	return &content.UserMessage{Message: content.Message{
		Role:   content.RoleUser,
		Blocks: []content.Block{&content.TextBlock{Text: s}},
	}}
}

// aiText builds one assistant message whose blocks are the given text parts, in
// order. Passing several parts exercises a single message with multiple text
// blocks; passing several aiText messages exercises multiple assistant turns.
func aiText(parts ...string) *content.AIMessage {
	blocks := make([]content.Block, 0, len(parts))
	for _, p := range parts {
		blocks = append(blocks, &content.TextBlock{Text: p})
	}
	return &content.AIMessage{Message: content.Message{
		Role:   content.RoleAssistant,
		Blocks: blocks,
	}}
}

// aiToolUse builds an assistant message that requests a tool call. input is the
// raw tool argument JSON, passed verbatim so a malformed value can be exercised.
func aiToolUse(id, name, input string) *content.AIMessage {
	return &content.AIMessage{Message: content.Message{
		Role: content.RoleAssistant,
		Blocks: []content.Block{&content.ToolUseBlock{
			ID:    id,
			Name:  name,
			Input: json.RawMessage(input),
		}},
	}}
}

// toolResult builds a tool-result message whose blocks may themselves nest
// further blocks, so tests can place text or a tool-use inside a tool result.
func toolResult(toolUseID string, isErr bool, blocks ...content.Block) *content.ToolResultMessage {
	return &content.ToolResultMessage{
		Message: content.Message{
			Role:   content.RoleTool,
			Blocks: blocks,
		},
		ToolUseID: toolUseID,
		IsError:   isErr,
	}
}

// nestedTextResult builds a tool-result message that nests a text block inside a
// ToolResultBlock, so the tool text is one level deeper than a top-level block.
func nestedTextResult(toolUseID, text string) *content.ToolResultMessage {
	return toolResult(toolUseID, false, &content.ToolResultBlock{
		ToolUseID: toolUseID,
		Content:   []content.Block{&content.TextBlock{Text: text}},
	})
}

// obs builds an observation over conv carrying the given trace evidence. The
// subject and scope are left at their zero values: the exact evaluators read the
// conversation and the trace only, never the subject, so a minimal observation
// is sufficient and keeps the tables focused.
func obs(conv content.AgenticMessages, evid ...eval.Evidence) eval.Observation {
	return eval.Observation{
		Conversation: conv,
		Trace:        eval.Trace{Evidence: evid},
	}
}

// sampleOf pairs an observation with no scenario (continuous-observation shape).
func sampleOf(o eval.Observation) eval.Sample {
	return eval.Sample{Observation: o}
}

// mustValid fails the test unless a satisfies the assessment boundary contract.
func mustValid(t *testing.T, a eval.Assessment) {
	t.Helper()
	if err := a.Validate(); err != nil {
		t.Fatalf("assessment failed Validate(): %v (status=%q)", err, a.Status)
	}
}

// evaluate runs ev against the sample and fails on an unexpected infra error,
// then asserts the returned assessment is itself well-formed.
func evaluate(t *testing.T, ev eval.Evaluator, s eval.Sample) eval.Assessment {
	t.Helper()
	a, err := ev.Evaluate(context.Background(), s)
	if err != nil {
		t.Fatalf("Evaluate returned an unexpected error: %v", err)
	}
	mustValid(t, a)
	return a
}

// toolOpEv builds a tool-operation evidence entry with the given error flag.
func toolOpEv(id, name string, isErr bool) eval.Evidence {
	return eval.Evidence{
		ID:   eval.EvidenceID(id),
		Kind: eval.EvidenceToolOperation,
		ToolOperation: &eval.ToolOperationEvidence{
			ToolName: eval.Name(name),
			IsError:  isErr,
		},
	}
}

// timingEv builds a timing evidence entry of the given duration.
func timingEv(id string, d time.Duration) eval.Evidence {
	return eval.Evidence{
		ID:     eval.EvidenceID(id),
		Kind:   eval.EvidenceTiming,
		Timing: &eval.TimingEvidence{Duration: d},
	}
}

// usageEv builds a model-usage evidence entry.
func usageEv(id string) eval.Evidence {
	return eval.Evidence{
		ID:    eval.EvidenceID(id),
		Kind:  eval.EvidenceUsage,
		Usage: &eval.UsageEvidence{Usage: content.Usage{InputTokens: 8, OutputTokens: 4}},
	}
}

// structErrEv builds a structured-output-error evidence entry.
func structErrEv(id string, reason eval.StructuredErrorReason) eval.Evidence {
	return eval.Evidence{
		ID:              eval.EvidenceID(id),
		Kind:            eval.EvidenceStructuredError,
		StructuredError: &eval.StructuredOutputError{Reason: reason},
	}
}

// structOutEv builds a positive structured-output evidence entry: the subject
// produced output that validated against its declared schema.
func structOutEv(id, schemaName, schemaRev string) eval.Evidence {
	return eval.Evidence{
		ID:   eval.EvidenceID(id),
		Kind: eval.EvidenceStructuredOutput,
		StructuredOutput: &eval.StructuredOutput{
			SchemaName:     eval.Name(schemaName),
			SchemaRevision: eval.Revision(schemaRev),
		},
	}
}

// --- RequiredText ---

func TestRequiredText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		required   []string
		conv       content.AgenticMessages
		wantStatus eval.AssessmentStatus
	}{
		{
			name:       "all substrings present",
			required:   []string{"refund", "processed"},
			conv:       content.AgenticMessages{userText("refund?"), aiText("your refund was processed")},
			wantStatus: eval.StatusPass,
		},
		{
			name:       "one substring missing",
			required:   []string{"refund", "declined"},
			conv:       content.AgenticMessages{aiText("your refund was processed")},
			wantStatus: eval.StatusFail,
		},
		{
			name:     "substring split across two assistant messages does not match",
			required: []string{"refundprocessed"},
			// Two separate assistant turns; the flatten joins them with a newline
			// so the concatenated token never appears.
			conv:       content.AgenticMessages{aiText("refund"), aiText("processed")},
			wantStatus: eval.StatusFail,
		},
		{
			name:       "both parts present across two assistant messages",
			required:   []string{"refund", "processed"},
			conv:       content.AgenticMessages{aiText("refund"), aiText("processed")},
			wantStatus: eval.StatusPass,
		},
		{
			name:       "unicode substring present",
			required:   []string{"世界"},
			conv:       content.AgenticMessages{aiText("你好世界")},
			wantStatus: eval.StatusPass,
		},
		{
			name:     "required text only inside nested tool result is not assistant output",
			required: []string{"secret-value"},
			conv: content.AgenticMessages{
				aiText("here is the answer"),
				nestedTextResult("call-1", "secret-value"),
			},
			wantStatus: eval.StatusFail,
		},
		{
			name:       "no assistant messages at all",
			required:   []string{"anything"},
			conv:       content.AgenticMessages{userText("hello")},
			wantStatus: eval.StatusFail,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a := evaluate(t, RequiredText(tt.required...), sampleOf(obs(tt.conv)))
			if a.Status != tt.wantStatus {
				t.Fatalf("status = %q, want %q", a.Status, tt.wantStatus)
			}
			if a.Status == eval.StatusFail && !findingHasResolvingEvidence(t, a) {
				t.Fatal("fail assessment carries a finding without a resolving evidence reference")
			}
		})
	}
}

func TestRequiredTextVacuousIsNotPass(t *testing.T) {
	t.Parallel()
	a := evaluate(t, RequiredText(), sampleOf(obs(content.AgenticMessages{aiText("hi")})))
	if a.Status == eval.StatusPass {
		t.Fatal("vacuous RequiredText() must never pass")
	}
	if a.Status != eval.StatusError {
		t.Fatalf("vacuous RequiredText() status = %q, want %q", a.Status, eval.StatusError)
	}
}

// --- ForbiddenText ---

func TestForbiddenText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		forbidden  []string
		conv       content.AgenticMessages
		wantStatus eval.AssessmentStatus
	}{
		{
			name:       "none present",
			forbidden:  []string{"guaranteed", "risk-free"},
			conv:       content.AgenticMessages{aiText("this investment carries risk")},
			wantStatus: eval.StatusPass,
		},
		{
			name:       "one forbidden phrase present",
			forbidden:  []string{"guaranteed"},
			conv:       content.AgenticMessages{aiText("returns are guaranteed")},
			wantStatus: eval.StatusFail,
		},
		{
			name:       "forbidden phrase present only in second assistant message",
			forbidden:  []string{"guaranteed"},
			conv:       content.AgenticMessages{aiText("hello"), aiText("it is guaranteed")},
			wantStatus: eval.StatusFail,
		},
		{
			name:      "forbidden phrase only inside nested tool result is not assistant output",
			forbidden: []string{"guaranteed"},
			conv: content.AgenticMessages{
				aiText("no promises here"),
				nestedTextResult("call-1", "guaranteed"),
			},
			wantStatus: eval.StatusPass,
		},
		{
			name:       "unicode forbidden phrase present",
			forbidden:  []string{"naïve"},
			conv:       content.AgenticMessages{aiText("that is a naïve assumption")},
			wantStatus: eval.StatusFail,
		},
		{
			name:       "empty conversation",
			forbidden:  []string{"guaranteed"},
			conv:       content.AgenticMessages{},
			wantStatus: eval.StatusPass,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a := evaluate(t, ForbiddenText(tt.forbidden...), sampleOf(obs(tt.conv)))
			if a.Status != tt.wantStatus {
				t.Fatalf("status = %q, want %q", a.Status, tt.wantStatus)
			}
			if a.Status == eval.StatusFail && !findingHasResolvingEvidence(t, a) {
				t.Fatal("fail assessment carries a finding without a resolving evidence reference")
			}
		})
	}
}

func TestForbiddenTextVacuousIsNotPass(t *testing.T) {
	t.Parallel()
	a := evaluate(t, ForbiddenText(), sampleOf(obs(content.AgenticMessages{aiText("hi")})))
	if a.Status == eval.StatusPass {
		t.Fatal("vacuous ForbiddenText() must never pass")
	}
	if a.Status != eval.StatusError {
		t.Fatalf("vacuous ForbiddenText() status = %q, want %q", a.Status, eval.StatusError)
	}
}

// findingHasResolvingEvidence reports whether every finding that names an
// EvidenceID resolves to an entry in the assessment's own evidence, and that at
// least one such resolving reference exists. Assessment.Validate already rejects
// dangling references; this additionally insists a failure actually cites
// evidence rather than leaving the finding bare.
func findingHasResolvingEvidence(t *testing.T, a eval.Assessment) bool {
	t.Helper()
	ids := make(map[eval.EvidenceID]struct{}, len(a.Evidence))
	for _, ev := range a.Evidence {
		ids[ev.ID] = struct{}{}
	}
	cited := false
	for _, f := range a.Findings {
		for _, ref := range f.Evidence {
			if ref.Evidence == "" {
				continue
			}
			if _, ok := ids[ref.Evidence]; !ok {
				return false
			}
			cited = true
		}
	}
	return cited
}
