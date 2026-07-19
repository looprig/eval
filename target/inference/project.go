package inference

// This file projects an inference.Response into an eval.Observation. The
// conversation is the semantic record: the scenario input followed by the
// returned assistant message. The trace carries only operational facts not
// present in the conversation — the inference operation's timing and the model's
// token usage — as safe, typed evidence. No secret, provider error text, or
// untrusted content is ever placed on the subject, trace, operation, or evidence:
// only model identity, counts, and timings.

import (
	"errors"
	"time"

	"github.com/looprig/core/content"
	"github.com/looprig/eval"
	llm "github.com/looprig/inference"
)

// Fixed, safe identifiers for the operation and evidence the target emits. They
// are constants, never derived from model output.
const (
	operationID          = "inference"
	evidenceIDTiming     = eval.EvidenceID("inference_timing")
	evidenceIDUsage      = eval.EvidenceID("inference_usage")
	evidenceIDStructured = eval.EvidenceID("inference_structured_output")
	timingLabel          = eval.Name("inference")
)

// project assembles the observation from the scenario input, the successful
// response, and the pinned operation timing. The caller has already guaranteed a
// non-nil response with a non-nil assistant message.
func (t *target) project(input content.AgenticMessages, resp *llm.Response, start, end time.Time) eval.Observation {
	conv := make(content.AgenticMessages, 0, len(input)+1)
	conv = append(conv, input...)
	conv = append(conv, resp.Message)

	evidence := []eval.Evidence{t.timingEvidence(start, end), t.usageEvidence(resp)}
	refs := []eval.EvidenceRef{
		{Evidence: evidenceIDTiming},
		{Evidence: evidenceIDUsage},
	}
	// When the request asked for structured output, close the loop: emit a
	// positive structured-output signal on success or a classified structured
	// error on failure, so exact.SchemaResult can move off unverified. Only the
	// closed reason enum and safe schema identity ever reach the evidence.
	if structured, ok := t.structuredEvidence(resp); ok {
		evidence = append(evidence, structured)
		refs = append(refs, eval.EvidenceRef{Evidence: structured.ID})
	}

	op := eval.Operation{
		ID:        operationID,
		Kind:      eval.OperationInference,
		Status:    eval.OperationOK,
		StartedAt: start,
		EndedAt:   end,
		Evidence:  refs,
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
			Evidence:   evidence,
		},
	}
}

// structuredEvidence produces the target's structured-output evidence, or
// (zero, false) when the request did not ask for structured output. Structured
// output was requested iff the request template carries an Output schema. On a
// valid structured response it emits a positive EvidenceStructuredOutput
// carrying only safe schema identity; on any structured-result failure it emits
// an EvidenceStructuredError carrying only a closed reason classification. The
// raw model output and any provider error text never reach the evidence.
func (t *target) structuredEvidence(resp *llm.Response) (eval.Evidence, bool) {
	if t.template.Output == nil {
		return eval.Evidence{}, false
	}
	if _, err := llm.StructuredResult(resp); err != nil {
		return eval.Evidence{
			ID:              evidenceIDStructured,
			Kind:            eval.EvidenceStructuredError,
			StructuredError: &eval.StructuredOutputError{Reason: classifyStructuredError(err)},
		}, true
	}
	return eval.Evidence{
		ID:   evidenceIDStructured,
		Kind: eval.EvidenceStructuredOutput,
		StructuredOutput: &eval.StructuredOutput{
			SchemaName:     eval.Name(t.template.Output.Name),
			SchemaRevision: eval.Revision(llm.StructuredOutputRevision),
		},
	}, true
}

// classifyStructuredError maps a typed inference structured-result failure onto
// eval's closed StructuredErrorReason vocabulary. It is a closed switch that
// fails secure: an unrecognized inference error or malformed reason becomes a
// schema mismatch (a failure), never a benign or passing classification. It
// carries across no bytes of model output or provider text.
func classifyStructuredError(err error) eval.StructuredErrorReason {
	var finish *llm.StructuredOutputFinishError
	if errors.As(err, &finish) {
		return eval.StructuredErrorEmptyOutput
	}
	var malformed *llm.MalformedStructuredOutputError
	if errors.As(err, &malformed) {
		switch malformed.ReasonCode {
		case llm.MalformedReasonMalformedJSON:
			return eval.StructuredErrorInvalidJSON
		case llm.MalformedReasonEmpty, llm.MalformedReasonNilResponse, llm.MalformedReasonNilMessage:
			return eval.StructuredErrorEmptyOutput
		default:
			return eval.StructuredErrorSchemaMismatch
		}
	}
	return eval.StructuredErrorSchemaMismatch
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
