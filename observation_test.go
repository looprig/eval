package eval

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/looprig/core/content"
)

// sampleConversation returns an ordered four-message thread exercising the
// message shapes an observation must preserve verbatim: a user turn, an
// assistant text turn, an assistant tool-use turn, and an error tool result.
func sampleConversation() content.AgenticMessages {
	return content.AgenticMessages{
		&content.UserMessage{Message: content.Message{
			Role:   content.RoleUser,
			Blocks: []content.Block{&content.TextBlock{Text: "look up my account"}},
		}},
		&content.AIMessage{
			Message: content.Message{
				Role:   content.RoleAssistant,
				Blocks: []content.Block{&content.TextBlock{Text: "one moment"}},
			},
			Usage: &content.Usage{InputTokens: 12, OutputTokens: 4},
		},
		&content.AIMessage{Message: content.Message{
			Role: content.RoleAssistant,
			Blocks: []content.Block{&content.ToolUseBlock{
				ID:    "call-1",
				Name:  "lookup_account",
				Input: json.RawMessage(`{"id":"acct-9"}`),
			}},
		}},
		&content.ToolResultMessage{
			Message: content.Message{
				Role:   content.RoleTool,
				Blocks: []content.Block{&content.TextBlock{Text: "backend unavailable"}},
			},
			ToolUseID: "call-1",
			IsError:   true,
		},
	}
}

func intPtr(i int) *int { return &i }

// newValidObservation builds a fully valid observation over sampleConversation
// with fresh backing slices so each test case can mutate it in isolation.
func newValidObservation() Observation {
	start := time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC)
	return Observation{
		Conversation: sampleConversation(),
		Scope:        ScopeTurn,
		Subject:      Subject{ID: "gpt-x", Kind: SubjectModel, Name: "gpt-x", Revision: "2026-07"},
		Trace: Trace{
			TraceID:   "trace-1",
			SessionID: "sess-1",
			TurnID:    "turn-1",
			StartedAt: start,
			EndedAt:   start.Add(2 * time.Second),
			Model:     "2026-07",
			Prompt:    "support-v3",
			MessageRanges: []MessageRange{
				{Start: 0, Len: 4},
			},
			Evidence: []Evidence{
				{ID: "ev-excerpt", Kind: EvidenceConversationExcerpt, ConversationExcerpt: &ConversationExcerpt{
					MessageIndex: 3,
					Role:         content.RoleTool,
					Hash:         "sha256:abcd",
					Redacted:     "[tool error: backend unavailable]",
				}},
				{ID: "ev-usage", Kind: EvidenceUsage, Usage: &UsageEvidence{
					Model: "2026-07",
					Usage: content.Usage{InputTokens: 12, OutputTokens: 4},
				}},
			},
			Operations: []Operation{
				{
					ID:         "op-1",
					Kind:       OperationTool,
					Status:     OperationFailed,
					StartedAt:  start.Add(time.Second),
					EndedAt:    start.Add(2 * time.Second),
					ErrorClass: ErrorUnavailable,
					Attributes: []Attribute{{Key: "tool", Value: "lookup_account"}},
					Evidence: []EvidenceRef{
						{Evidence: "ev-usage"},
						{MessageIndex: intPtr(3)},
					},
				},
			},
		},
	}
}

func TestObservationValidateHappyPath(t *testing.T) {
	t.Parallel()
	if err := newValidObservation().Validate(); err != nil {
		t.Fatalf("valid observation rejected: %v", err)
	}
}

func TestObservationValidatePreservesOrder(t *testing.T) {
	t.Parallel()

	obs := newValidObservation()

	wantRoles := []content.Role{content.RoleUser, content.RoleAssistant, content.RoleAssistant, content.RoleTool}
	wantEvidence := []EvidenceID{"ev-excerpt", "ev-usage"}
	wantOps := []string{"op-1"}

	if err := obs.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	if len(obs.Conversation) != len(wantRoles) {
		t.Fatalf("conversation length changed: got %d want %d", len(obs.Conversation), len(wantRoles))
	}
	for i, msg := range obs.Conversation {
		if got := messageRole(msg); got != wantRoles[i] {
			t.Errorf("conversation[%d] role = %q, want %q", i, got, wantRoles[i])
		}
	}
	for i, ev := range obs.Trace.Evidence {
		if ev.ID != wantEvidence[i] {
			t.Errorf("evidence[%d] id = %q, want %q", i, ev.ID, wantEvidence[i])
		}
	}
	for i, op := range obs.Trace.Operations {
		if op.ID != wantOps[i] {
			t.Errorf("operation[%d] id = %q, want %q", i, op.ID, wantOps[i])
		}
	}
}

func messageRole(m content.Conversation) content.Role {
	switch v := m.(type) {
	case *content.UserMessage:
		return v.Role
	case *content.AIMessage:
		return v.Role
	case *content.SystemMessage:
		return v.Role
	case *content.ToolResultMessage:
		return v.Role
	default:
		return ""
	}
}

func TestObservationValidateErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*Observation)
	}{
		{
			name: "invalid subject kind",
			mutate: func(o *Observation) {
				o.Subject.Kind = SubjectKind("wombat")
			},
		},
		{
			name: "empty subject name",
			mutate: func(o *Observation) {
				o.Subject.Name = ""
			},
		},
		{
			name: "invalid scope",
			mutate: func(o *Observation) {
				o.Scope = Scope(42)
			},
		},
		{
			name: "end before start",
			mutate: func(o *Observation) {
				o.Trace.EndedAt = o.Trace.StartedAt.Add(-time.Second)
			},
		},
		{
			name: "message range beyond conversation",
			mutate: func(o *Observation) {
				o.Trace.MessageRanges = []MessageRange{{Start: 0, Len: 5}}
			},
		},
		{
			name: "message range negative start",
			mutate: func(o *Observation) {
				o.Trace.MessageRanges = []MessageRange{{Start: -1, Len: 1}}
			},
		},
		{
			name: "duplicate evidence id",
			mutate: func(o *Observation) {
				o.Trace.Evidence = append(o.Trace.Evidence, Evidence{
					ID:   "ev-usage",
					Kind: EvidenceTiming,
					Timing: &TimingEvidence{
						Label:    "retry",
						Duration: time.Second,
					},
				})
			},
		},
		{
			name: "excerpt index outside conversation",
			mutate: func(o *Observation) {
				o.Trace.Evidence[0].ConversationExcerpt.MessageIndex = 4
			},
		},
		{
			name: "operation references unknown evidence",
			mutate: func(o *Observation) {
				o.Trace.Operations[0].Evidence[0].Evidence = "ev-missing"
			},
		},
		{
			name: "operation evidence ref message index outside conversation",
			mutate: func(o *Observation) {
				o.Trace.Operations[0].Evidence[1].MessageIndex = intPtr(4)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			obs := newValidObservation()
			tt.mutate(&obs)
			err := obs.Validate()
			if err == nil {
				t.Fatalf("Validate() = nil, want error")
			}
			// Every failure is a concrete, classifiable type, not a bare error.
			if isBareError(err) {
				t.Fatalf("Validate() returned bare error %v; want a typed error", err)
			}
			assertNoUntrustedEcho(t, err, "backend unavailable")
			assertNoUntrustedEcho(t, err, "acct-9")
		})
	}
}

// isBareError reports whether err is not one of the package's typed errors.
func isBareError(err error) bool {
	var (
		ve  *ValidationError
		ee  *InvalidEnumError
		ir  *IndexRangeError
		de  *DuplicateEvidenceError
		ue  *UnknownEvidenceError
		pe  *EvidencePayloadError
		uve *content.UsageValidationError
	)
	switch {
	case errors.As(err, &ve),
		errors.As(err, &ee),
		errors.As(err, &ir),
		errors.As(err, &de),
		errors.As(err, &ue),
		errors.As(err, &pe),
		errors.As(err, &uve):
		return false
	default:
		return true
	}
}

func TestObservationValidateEmptyConversation(t *testing.T) {
	t.Parallel()

	obs := Observation{
		Scope:   ScopeSession,
		Subject: Subject{ID: "agent-1", Kind: SubjectAgent, Name: "agent-1", Revision: "v1"},
		Trace:   Trace{},
	}
	if err := obs.Validate(); err != nil {
		t.Fatalf("empty-conversation observation rejected: %v", err)
	}

	// A range or excerpt into an empty conversation must still be rejected.
	obs.Trace.MessageRanges = []MessageRange{{Start: 0, Len: 1}}
	if err := obs.Validate(); err == nil {
		t.Fatal("range into empty conversation accepted, want error")
	}
}

func TestIndexRangeErrorMessageSafe(t *testing.T) {
	t.Parallel()
	e := &IndexRangeError{Field: "MessageRange", Index: 5, Len: 4}
	if !strings.Contains(e.Error(), "MessageRange") {
		t.Errorf("IndexRangeError.Error() = %q, missing field", e.Error())
	}
}
