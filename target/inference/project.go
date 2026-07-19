package inference

// This file projects an inference.Response into an eval.Observation. The
// conversation is the semantic record: the scenario input followed by the
// returned assistant message. The trace carries only operational facts not
// present in the conversation — the inference operation's timing and the model's
// token usage — as safe, typed evidence. No secret, provider error text, or
// untrusted content is ever placed on the subject, trace, operation, or evidence:
// only model identity, counts, and timings.

import (
	"bytes"
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
// (zero, false) when the request did not ask for a schema-constrained structured
// output. A schema was requested iff the request template carries an Output with a
// non-empty Schema; without a declared schema there is nothing to validate
// against, so no positive signal is emitted and SchemaResult stays Unverified
// (the fail-secure "unknown is not a pass" behavior).
//
// With a declared schema, the response is first extracted by StructuredResult
// (which only guarantees well-formed JSON) and then validated against the schema
// FOR REAL by conformsToSchema. Only a document that both extracts cleanly AND
// conforms yields a positive EvidenceStructuredOutput; an extraction failure or a
// schema violation yields an EvidenceStructuredError carrying a closed reason
// classification. The raw model output and any provider error text never reach
// the evidence — only the closed reason enum and safe schema identity do.
func (t *target) structuredEvidence(resp *llm.Response) (eval.Evidence, bool) {
	if t.template.Output == nil || len(bytes.TrimSpace(t.template.Output.Schema)) == 0 {
		return eval.Evidence{}, false
	}
	raw, err := llm.StructuredResult(resp)
	if err != nil {
		return structuredErrorEvidence(classifyStructuredError(err)), true
	}
	if ok, reason := conformsToSchema(t.template.Output.Schema, raw); !ok {
		return structuredErrorEvidence(reason), true
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

// structuredErrorEvidence builds the failure evidence for a structured-output
// extraction or schema-conformance failure. It carries only the closed reason
// classification — never a byte of model output.
func structuredErrorEvidence(reason eval.StructuredErrorReason) eval.Evidence {
	return eval.Evidence{
		ID:              evidenceIDStructured,
		Kind:            eval.EvidenceStructuredError,
		StructuredError: &eval.StructuredOutputError{Reason: reason},
	}
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
		case llm.MalformedReasonMalformedJSON, llm.MalformedReasonRootNotObject:
			// A not-JSON or root-not-object body is an invalid-JSON representation.
			// This matches the judge's classifyExtractionError, which maps
			// RootNotObject to InvalidJSON.
			return eval.StructuredErrorInvalidJSON
		case llm.MalformedReasonEmpty, llm.MalformedReasonNilResponse, llm.MalformedReasonNilMessage:
			return eval.StructuredErrorEmptyOutput
		default:
			// Any other malformed representation (ambiguous, invalid block, wrong
			// role, too large, ...) is a shape failure. Fail secure.
			return eval.StructuredErrorSchemaMismatch
		}
	}
	// An unrecognized structured-result error must still map to a failure reason,
	// never absence of error.
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
