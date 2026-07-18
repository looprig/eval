package inference

// This file projects an inference.Response into an eval.Observation. The
// conversation is the semantic record: the scenario input followed by the
// returned assistant message. The trace carries only operational facts not
// present in the conversation — the inference operation's timing and the model's
// token usage — as safe, typed evidence. No secret, provider error text, or
// untrusted content is ever placed on the subject, trace, operation, or evidence:
// only model identity, counts, and timings.

import (
	"time"

	"github.com/looprig/core/content"
	"github.com/looprig/eval"
	llm "github.com/looprig/inference"
)

// Fixed, safe identifiers for the operation and evidence the target emits. They
// are constants, never derived from model output.
const (
	operationID      = "inference"
	evidenceIDTiming = eval.EvidenceID("inference_timing")
	evidenceIDUsage  = eval.EvidenceID("inference_usage")
	timingLabel      = eval.Name("inference")
)

// project assembles the observation from the scenario input, the successful
// response, and the pinned operation timing. The caller has already guaranteed a
// non-nil response with a non-nil assistant message.
func (t *target) project(input content.AgenticMessages, resp *llm.Response, start, end time.Time) eval.Observation {
	conv := make(content.AgenticMessages, 0, len(input)+1)
	conv = append(conv, input...)
	conv = append(conv, resp.Message)

	timing := t.timingEvidence(start, end)
	usage := t.usageEvidence(resp)

	op := eval.Operation{
		ID:        operationID,
		Kind:      eval.OperationInference,
		Status:    eval.OperationOK,
		StartedAt: start,
		EndedAt:   end,
		Evidence: []eval.EvidenceRef{
			{Evidence: evidenceIDTiming},
			{Evidence: evidenceIDUsage},
		},
	}

	return eval.Observation{
		Conversation: conv,
		Scope:        eval.ScopeCase,
		Subject: eval.Subject{
			ID:       t.subjectID,
			Kind:     eval.SubjectModel,
			Name:     t.name,
			Revision: t.revision,
		},
		Trace: eval.Trace{
			StartedAt:  start,
			EndedAt:    end,
			Model:      t.revision,
			Operations: []eval.Operation{op},
			Evidence:   []eval.Evidence{timing, usage},
		},
	}
}

// timingEvidence records how long the inference call took as a safe scalar. A
// non-monotonic clock that reports end before start is clamped to zero so the
// evidence never validates as negative.
func (t *target) timingEvidence(start, end time.Time) eval.Evidence {
	d := end.Sub(start)
	if d < 0 {
		d = 0
	}
	return eval.Evidence{
		ID:     evidenceIDTiming,
		Kind:   eval.EvidenceTiming,
		Timing: &eval.TimingEvidence{Label: timingLabel, Duration: d},
	}
}

// usageEvidence records the model's token usage as safe counts. A nil response
// usage becomes a zero usage rather than a failure, and the model revision is
// the target's already-validated identity — never provider-echoed text.
func (t *target) usageEvidence(resp *llm.Response) eval.Evidence {
	var usage content.Usage
	if resp.Usage != nil {
		usage = *resp.Usage
	}
	return eval.Evidence{
		ID:    evidenceIDUsage,
		Kind:  eval.EvidenceUsage,
		Usage: &eval.UsageEvidence{Model: t.revision, Usage: usage},
	}
}
