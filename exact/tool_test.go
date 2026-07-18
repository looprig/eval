package exact

import (
	"testing"

	"github.com/looprig/core/content"
	"github.com/looprig/eval"
)

func TestRequiredTool(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		tool       string
		conv       content.AgenticMessages
		wantStatus eval.AssessmentStatus
	}{
		{
			name:       "tool call present",
			tool:       "lookup_account",
			conv:       content.AgenticMessages{userText("look up my account"), aiToolUse("call-1", "lookup_account", `{"id":"acct-9"}`)},
			wantStatus: eval.StatusPass,
		},
		{
			name:       "tool call absent",
			tool:       "lookup_account",
			conv:       content.AgenticMessages{aiText("I cannot help with that")},
			wantStatus: eval.StatusFail,
		},
		{
			name: "tool call present only in a later assistant turn",
			conv: content.AgenticMessages{
				aiText("one moment"),
				aiToolUse("call-7", "lookup_account", `{}`),
			},
			tool:       "lookup_account",
			wantStatus: eval.StatusPass,
		},
		{
			name: "tool use nested inside a tool result is still found",
			tool: "lookup_account",
			conv: content.AgenticMessages{
				toolResult("call-1", false, &content.ToolResultBlock{
					ToolUseID: "call-1",
					Content:   []content.Block{&content.ToolUseBlock{ID: "call-2", Name: "lookup_account", Input: []byte(`{}`)}},
				}),
			},
			wantStatus: eval.StatusPass,
		},
		{
			name: "malformed tool arguments do not prevent a name match",
			tool: "lookup_account",
			conv: content.AgenticMessages{
				aiToolUse("call-1", "lookup_account", `{"id": `), // truncated, invalid JSON
			},
			wantStatus: eval.StatusPass,
		},
		{
			name:       "a different tool was called",
			tool:       "lookup_account",
			conv:       content.AgenticMessages{aiToolUse("call-1", "issue_refund", `{}`)},
			wantStatus: eval.StatusFail,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a := evaluate(t, RequiredTool(tt.tool), sampleOf(obs(tt.conv)))
			if a.Status != tt.wantStatus {
				t.Fatalf("status = %q, want %q", a.Status, tt.wantStatus)
			}
			if a.Status == eval.StatusFail && !findingHasResolvingEvidence(t, a) {
				t.Fatal("fail assessment carries a finding without a resolving evidence reference")
			}
		})
	}
}

func TestForbiddenTool(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		tool       string
		conv       content.AgenticMessages
		wantStatus eval.AssessmentStatus
	}{
		{
			name:       "forbidden tool not called",
			tool:       "issue_refund",
			conv:       content.AgenticMessages{aiToolUse("call-1", "lookup_account", `{}`)},
			wantStatus: eval.StatusPass,
		},
		{
			name:       "forbidden tool called",
			tool:       "issue_refund",
			conv:       content.AgenticMessages{aiToolUse("call-1", "issue_refund", `{"amount":100}`)},
			wantStatus: eval.StatusFail,
		},
		{
			name: "forbidden tool nested inside a tool result",
			tool: "issue_refund",
			conv: content.AgenticMessages{
				toolResult("call-1", false, &content.ToolResultBlock{
					ToolUseID: "call-1",
					Content:   []content.Block{&content.ToolUseBlock{ID: "call-2", Name: "issue_refund", Input: []byte(`bad json`)}},
				}),
			},
			wantStatus: eval.StatusFail,
		},
		{
			name:       "no tool calls at all",
			tool:       "issue_refund",
			conv:       content.AgenticMessages{aiText("here is your answer")},
			wantStatus: eval.StatusPass,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// ForbiddenTool and NoToolCall must be identical.
			for _, ctor := range []struct {
				label string
				ev    eval.Evaluator
			}{
				{"ForbiddenTool", ForbiddenTool(tt.tool)},
				{"NoToolCall", NoToolCall(tt.tool)},
			} {
				a := evaluate(t, ctor.ev, sampleOf(obs(tt.conv)))
				if a.Status != tt.wantStatus {
					t.Fatalf("%s status = %q, want %q", ctor.label, a.Status, tt.wantStatus)
				}
				if a.Status == eval.StatusFail && !findingHasResolvingEvidence(t, a) {
					t.Fatalf("%s: fail assessment carries a finding without a resolving evidence reference", ctor.label)
				}
			}
		})
	}
}

func TestToolVacuousIsNotPass(t *testing.T) {
	t.Parallel()
	conv := content.AgenticMessages{aiToolUse("call-1", "issue_refund", `{}`)}
	for _, tt := range []struct {
		label string
		ev    eval.Evaluator
	}{
		{"RequiredTool empty", RequiredTool("")},
		{"ForbiddenTool empty", ForbiddenTool("")},
		{"NoToolCall empty", NoToolCall("")},
		{"RequiredTool invalid utf8", RequiredTool("bad\xff")},
	} {
		a := evaluate(t, tt.ev, sampleOf(obs(conv)))
		if a.Status == eval.StatusPass {
			t.Fatalf("%s must never pass", tt.label)
		}
		if a.Status != eval.StatusError {
			t.Fatalf("%s status = %q, want %q", tt.label, a.Status, eval.StatusError)
		}
	}
}
