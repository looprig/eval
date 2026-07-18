package eval

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/looprig/core/content"
)

// TestEvidenceValidateVariants covers each Phase-1 evidence kind: a valid
// instance of every variant plus the union invariants (exactly one payload,
// payload matches kind, valid id, valid kind).
func TestEvidenceValidateVariants(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		evidence Evidence
		wantErr  bool
	}{
		{
			name: "conversation excerpt",
			evidence: Evidence{ID: "e1", Kind: EvidenceConversationExcerpt, ConversationExcerpt: &ConversationExcerpt{
				MessageIndex: 2,
				Role:         content.RoleAssistant,
				Hash:         "sha256:deadbeef",
				Redacted:     "[redacted excerpt]",
			}},
		},
		{
			name:     "message index reference",
			evidence: Evidence{ID: "e2", Kind: EvidenceMessageIndex, MessageIndex: &MessageIndexRef{Index: 0}},
		},
		{
			name: "timing",
			evidence: Evidence{ID: "e3", Kind: EvidenceTiming, Timing: &TimingEvidence{
				Label:    "inference",
				Duration: 250 * time.Millisecond,
			}},
		},
		{
			name: "usage",
			evidence: Evidence{ID: "e4", Kind: EvidenceUsage, Usage: &UsageEvidence{
				Model: "2026-07",
				Usage: content.Usage{InputTokens: 100, OutputTokens: 20},
			}},
		},
		{
			name: "tool operation",
			evidence: Evidence{ID: "e5", Kind: EvidenceToolOperation, ToolOperation: &ToolOperationEvidence{
				ToolName:    "lookup_account",
				ToolUseID:   "call-1",
				ArgsHash:    "sha256:1234",
				ArgsBytes:   64,
				ResultBytes: 128,
				IsError:     true,
			}},
		},
		{
			name: "structured output error",
			evidence: Evidence{ID: "e6", Kind: EvidenceStructuredError, StructuredError: &StructuredOutputError{
				Schema:     "score-v1",
				Reason:     StructuredErrorSchemaMismatch,
				DetailHash: "sha256:5678",
			}},
		},
		{
			name: "diagnostic",
			evidence: Evidence{ID: "e7", Kind: EvidenceDiagnostic, Diagnostic: &DiagnosticEvidence{
				Code:     "judge_unavailable",
				Severity: SeverityHigh,
				Message:  "[redacted judge diagnostic]",
			}},
		},
		{
			name:     "no payload",
			evidence: Evidence{ID: "e8", Kind: EvidenceTiming},
			wantErr:  true,
		},
		{
			name: "multiple payloads",
			evidence: Evidence{ID: "e9", Kind: EvidenceTiming,
				Timing:              &TimingEvidence{Duration: time.Second},
				ConversationExcerpt: &ConversationExcerpt{MessageIndex: 0, Redacted: "x"},
			},
			wantErr: true,
		},
		{
			name: "payload mismatched kind",
			evidence: Evidence{ID: "e10", Kind: EvidenceTiming,
				ConversationExcerpt: &ConversationExcerpt{MessageIndex: 0, Redacted: "x"},
			},
			wantErr: true,
		},
		{
			name:     "empty id",
			evidence: Evidence{ID: "", Kind: EvidenceTiming, Timing: &TimingEvidence{Duration: time.Second}},
			wantErr:  true,
		},
		{
			name:     "unknown kind",
			evidence: Evidence{ID: "e11", Kind: EvidenceKind("teleport"), Timing: &TimingEvidence{Duration: time.Second}},
			wantErr:  true,
		},
		{
			name: "negative excerpt index",
			evidence: Evidence{ID: "e12", Kind: EvidenceConversationExcerpt, ConversationExcerpt: &ConversationExcerpt{
				MessageIndex: -1,
				Redacted:     "x",
			}},
			wantErr: true,
		},
		{
			name: "oversized redacted excerpt",
			evidence: Evidence{ID: "e13", Kind: EvidenceConversationExcerpt, ConversationExcerpt: &ConversationExcerpt{
				MessageIndex: 0,
				Redacted:     RedactedExcerpt(strings.Repeat("x", MaxExcerptBytes+1)),
			}},
			wantErr: true,
		},
		{
			name: "invalid structured error classification",
			evidence: Evidence{ID: "e14", Kind: EvidenceStructuredError, StructuredError: &StructuredOutputError{
				Reason: StructuredErrorReason("gremlins"),
			}},
			wantErr: true,
		},
		{
			name: "negative tool byte count",
			evidence: Evidence{ID: "e15", Kind: EvidenceToolOperation, ToolOperation: &ToolOperationEvidence{
				ToolName:  "lookup_account",
				ArgsBytes: -1,
			}},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.evidence.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Evidence.Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestEvidenceExactlyOnePayload verifies the union constraint directly: for
// every kind, exactly one payload pointer must be set and it must match kind.
func TestEvidenceExactlyOnePayload(t *testing.T) {
	t.Parallel()

	none := Evidence{ID: "x", Kind: EvidenceUsage}
	if err := none.Validate(); err == nil {
		t.Error("evidence with no payload validated")
	} else {
		var pe *EvidencePayloadError
		if !errors.As(err, &pe) {
			t.Errorf("no-payload error = %T, want *EvidencePayloadError", err)
		}
	}

	both := Evidence{ID: "x", Kind: EvidenceUsage,
		Usage:  &UsageEvidence{},
		Timing: &TimingEvidence{Duration: time.Second},
	}
	if err := both.Validate(); err == nil {
		t.Error("evidence with two payloads validated")
	} else {
		var pe *EvidencePayloadError
		if !errors.As(err, &pe) {
			t.Errorf("multi-payload error = %T, want *EvidencePayloadError", err)
		}
	}
}

// TestEvidenceRedactionDiscipline proves sensitive content is only representable
// through hash, classification, byte-count, or bounded redacted-excerpt fields.
// A raw excerpt is length-bounded so untrusted content cannot balloon a report.
func TestEvidenceRedactionDiscipline(t *testing.T) {
	t.Parallel()

	over := ConversationExcerpt{
		MessageIndex: 0,
		Redacted:     RedactedExcerpt(strings.Repeat("s", MaxExcerptBytes+1)),
	}
	if err := over.validate(); err == nil {
		t.Error("oversized redacted excerpt accepted")
	}

	diag := DiagnosticEvidence{
		Code:     "x",
		Severity: SeverityLow,
		Message:  RedactedExcerpt(strings.Repeat("m", MaxExcerptBytes+1)),
	}
	if err := diag.validate(); err == nil {
		t.Error("oversized diagnostic message accepted")
	}
}

func TestEvidenceKindValidate(t *testing.T) {
	t.Parallel()

	valid := []EvidenceKind{
		EvidenceConversationExcerpt, EvidenceMessageIndex, EvidenceTiming,
		EvidenceUsage, EvidenceToolOperation, EvidenceStructuredError, EvidenceDiagnostic,
	}
	for _, k := range valid {
		if err := k.Validate(); err != nil {
			t.Errorf("EvidenceKind(%q).Validate() = %v, want nil", k, err)
		}
	}

	for _, k := range []EvidenceKind{"", "http", "process"} {
		err := EvidenceKind(k).Validate()
		if err == nil {
			t.Errorf("EvidenceKind(%q).Validate() = nil, want error", k)
			continue
		}
		var ee *InvalidEnumError
		if !errors.As(err, &ee) {
			t.Errorf("EvidenceKind(%q) error = %T, want *InvalidEnumError", k, err)
		}
		assertNoUntrustedEcho(t, err, string(k))
	}
}

func TestSubjectKindValidate(t *testing.T) {
	t.Parallel()

	valid := []SubjectKind{SubjectModel, SubjectAgent, SubjectPrompt, SubjectHTTPEndpoint, SubjectProcess}
	for _, k := range valid {
		if err := k.Validate(); err != nil {
			t.Errorf("SubjectKind(%q).Validate() = %v, want nil", k, err)
		}
	}
	for _, k := range []SubjectKind{"", "database", "network"} {
		if err := SubjectKind(k).Validate(); err == nil {
			t.Errorf("SubjectKind(%q).Validate() = nil, want error", k)
		}
	}
}

func TestOperationValidate(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		op      Operation
		wantErr bool
	}{
		{
			name: "valid inference",
			op: Operation{ID: "op-1", Kind: OperationInference, Status: OperationOK,
				StartedAt: start, EndedAt: start.Add(time.Second)},
		},
		{
			name:    "empty id",
			op:      Operation{ID: "", Kind: OperationInference, Status: OperationOK},
			wantErr: true,
		},
		{
			name:    "invalid kind",
			op:      Operation{ID: "op-1", Kind: OperationKind("teleport"), Status: OperationOK},
			wantErr: true,
		},
		{
			name:    "invalid status",
			op:      Operation{ID: "op-1", Kind: OperationInference, Status: OperationStatus("fine")},
			wantErr: true,
		},
		{
			name: "end before start",
			op: Operation{ID: "op-1", Kind: OperationInference, Status: OperationOK,
				StartedAt: start, EndedAt: start.Add(-time.Second)},
			wantErr: true,
		},
		{
			name: "invalid error class",
			op: Operation{ID: "op-1", Kind: OperationInference, Status: OperationFailed,
				ErrorClass: ErrorClass("kaput")},
			wantErr: true,
		},
		{
			name: "evidence ref with nothing set",
			op: Operation{ID: "op-1", Kind: OperationInference, Status: OperationOK,
				Evidence: []EvidenceRef{{}}},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.op.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Operation.Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
