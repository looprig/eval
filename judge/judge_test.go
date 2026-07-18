package judge_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/looprig/core/content"
	"github.com/looprig/eval"
	"github.com/looprig/eval/judge"
	"github.com/looprig/eval/rubric"
	"github.com/looprig/inference"
	"github.com/looprig/inference/model"
	"github.com/looprig/inference/stream"
)

// --- fakes and fixtures -----------------------------------------------------

// errBoom is a canned inference transport failure.
var errBoom = errors.New("provider unreachable")

// fakeClient is an inference.Client that returns a canned response or error and
// records the request it was handed, so tests can assert what the judge built. It
// honors context cancellation before returning, so a deadline-exceeded context
// surfaces as a real inference failure.
type fakeClient struct {
	resp   *inference.Response
	err    error
	gotReq inference.Request
	calls  int
}

func (f *fakeClient) Invoke(ctx context.Context, req inference.Request) (*inference.Response, error) {
	f.gotReq = req
	f.calls++
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return f.resp, f.err
}

func (f *fakeClient) Stream(context.Context, inference.Request) (*stream.StreamReader[content.Chunk], error) {
	return nil, errors.New("stream not used by the judge")
}

func userText(s string) *content.UserMessage {
	return &content.UserMessage{Message: content.Message{
		Role:   content.RoleUser,
		Blocks: []content.Block{&content.TextBlock{Text: s}},
	}}
}

func aiText(s string) *content.AIMessage {
	return &content.AIMessage{Message: content.Message{
		Role:   content.RoleAssistant,
		Blocks: []content.Block{&content.TextBlock{Text: s}},
	}}
}

// conv is the fixture conversation. Message 1 (the assistant) contains "Paris",
// so a valid quote provenance test can cite it.
func conv() content.AgenticMessages {
	return content.AgenticMessages{
		userText("What is the capital of France?"),
		aiText("The capital of France is Paris."),
	}
}

func sampleOf(c content.AgenticMessages) eval.Sample {
	return eval.Sample{Observation: eval.Observation{Conversation: c}}
}

// capableModel advertises native structured output; incapableModel does not.
func capableModel() model.Model {
	return model.CustomModel("test", "test", "", "judge-model", model.WithStructuredOutput())
}

func incapableModel() model.Model {
	return model.CustomModel("test", "test", "", "judge-model")
}

func template(m model.Model) inference.Request {
	return inference.Request{Model: m}
}

// respText returns a structured-output response carrying s as terminal assistant
// text with a clean stop finish reason and some usage.
func respText(s string) *inference.Response {
	return &inference.Response{
		Message:      aiText(s),
		Usage:        &content.Usage{InputTokens: 42, OutputTokens: 7},
		Model:        "judge-model",
		FinishReason: stream.FinishReasonStop,
	}
}

// allMessageText concatenates the text of every message in a request, so a test
// can assert the conversation was carried into the request.
func allMessageText(req inference.Request) string {
	var b strings.Builder
	for _, m := range req.Messages {
		if u, ok := m.(*content.UserMessage); ok {
			for _, blk := range u.Blocks {
				if t, ok := blk.(*content.TextBlock); ok {
					b.WriteString(t.Text)
				}
			}
		}
	}
	return b.String()
}

// --- request assembly (behavior of the built request) -----------------------

func TestJudgeBuildsStrictStructuredRequest(t *testing.T) {
	t.Parallel()

	fc := &fakeClient{resp: respText(`{"score":0.9,"reason":"correct","evidence":[{"message_index":1,"quote":"Paris"}]}`)}
	j := judge.New(rubric.AnswerRelevanceV1, fc, template(capableModel()))

	a, err := j.Evaluate(context.Background(), sampleOf(conv()))
	if err != nil {
		t.Fatalf("Evaluate returned error: %v", err)
	}
	if err := a.Validate(); err != nil {
		t.Fatalf("returned assessment failed Validate(): %v", err)
	}

	req := fc.gotReq
	if req.Output == nil {
		t.Fatal("request Output is nil, want a structured output schema")
	}
	if !req.Output.Strict {
		t.Fatal("request Output.Strict = false, want true")
	}
	// The rubric revision is carried in the trusted instruction.
	if want := "revision " + string(rubric.AnswerRelevanceV1.Revision); !strings.Contains(req.System, want) {
		t.Fatalf("request System missing rubric revision %q", want)
	}
	// Bounded evidence instruction, in lockstep with the validation cap.
	if want := "at most " + strconv.Itoa(judge.MaxEvidenceQuotes); !strings.Contains(req.System, want) {
		t.Fatalf("request System missing bounded evidence instruction %q", want)
	}
	// A prompt-injection warning frames the data as untrusted.
	if !strings.Contains(req.System, "untrusted") {
		t.Fatal("request System missing untrusted-data framing")
	}
	// The observation conversation is carried as data.
	data := allMessageText(req)
	if !strings.Contains(data, "capital of France") || !strings.Contains(data, "Paris") {
		t.Fatal("request messages do not carry the observation conversation")
	}
}

// --- valid verdicts ---------------------------------------------------------

func TestJudgeValidVerdict(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		json       string
		wantStatus eval.AssessmentStatus
	}{
		{
			name:       "high score passes",
			json:       `{"score":0.9,"reason":"correct and complete","evidence":[{"message_index":1,"quote":"Paris"}]}`,
			wantStatus: eval.StatusPass,
		},
		{
			name:       "low score fails",
			json:       `{"score":0.1,"reason":"off topic","evidence":[{"message_index":1,"quote":"capital"}]}`,
			wantStatus: eval.StatusFail,
		},
		{
			name:       "no evidence still valid",
			json:       `{"score":0.8,"reason":"clearly relevant","evidence":[]}`,
			wantStatus: eval.StatusPass,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fc := &fakeClient{resp: respText(tt.json)}
			j := judge.New(rubric.AnswerRelevanceV1, fc, template(capableModel()))

			a, err := j.Evaluate(context.Background(), sampleOf(conv()))
			if err != nil {
				t.Fatalf("Evaluate returned error: %v", err)
			}
			if err := a.Validate(); err != nil {
				t.Fatalf("assessment failed Validate(): %v", err)
			}
			if a.Status != tt.wantStatus {
				t.Fatalf("status = %q, want %q", a.Status, tt.wantStatus)
			}
			if len(a.Measurements) != 1 || a.Measurements[0].Unit != eval.UnitRatio {
				t.Fatalf("expected one ratio measurement, got %+v", a.Measurements)
			}
		})
	}
}

func TestJudgeDescriptorIsMethodModel(t *testing.T) {
	t.Parallel()
	j := judge.New(rubric.GroundednessV1, &fakeClient{}, template(capableModel()))
	d := j.Descriptor()
	if d.Method != eval.MethodModel {
		t.Fatalf("Descriptor().Method = %v, want MethodModel", d.Method)
	}
	if err := d.Validate(); err != nil {
		t.Fatalf("Descriptor().Validate() = %v", err)
	}
	if d.Name != rubric.GroundednessV1.Name || d.Revision != rubric.GroundednessV1.Revision {
		t.Fatalf("descriptor identity = (%q,%q), want rubric identity", d.Name, d.Revision)
	}
}

// --- failure paths: never a pass -------------------------------------------

func TestJudgeFailurePaths(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		client *fakeClient
		model  model.Model
		assert func(t *testing.T, err error)
	}{
		{
			name:   "malformed json",
			client: &fakeClient{resp: respText(`{"score": `)},
			model:  capableModel(),
			assert: func(t *testing.T, err error) {
				var target *judge.MalformedOutputError
				if !errors.As(err, &target) {
					t.Fatalf("err = %v, want *MalformedOutputError", err)
				}
			},
		},
		{
			name:   "schema shape mismatch (unknown field)",
			client: &fakeClient{resp: respText(`{"score":0.5,"reason":"x","evidence":[],"extra":true}`)},
			model:  capableModel(),
			assert: func(t *testing.T, err error) {
				var target *judge.MalformedOutputError
				if !errors.As(err, &target) {
					t.Fatalf("err = %v, want *MalformedOutputError", err)
				}
			},
		},
		{
			name:   "out of range score",
			client: &fakeClient{resp: respText(`{"score":5,"reason":"x","evidence":[]}`)},
			model:  capableModel(),
			assert: func(t *testing.T, err error) {
				var target *judge.ScoreRangeError
				if !errors.As(err, &target) {
					t.Fatalf("err = %v, want *ScoreRangeError", err)
				}
			},
		},
		{
			name:   "invalid message index",
			client: &fakeClient{resp: respText(`{"score":0.9,"reason":"x","evidence":[{"message_index":99,"quote":"Paris"}]}`)},
			model:  capableModel(),
			assert: func(t *testing.T, err error) {
				var target *judge.MessageIndexError
				if !errors.As(err, &target) {
					t.Fatalf("err = %v, want *MessageIndexError", err)
				}
			},
		},
		{
			name:   "quote not present in message",
			client: &fakeClient{resp: respText(`{"score":0.9,"reason":"x","evidence":[{"message_index":1,"quote":"Berlin"}]}`)},
			model:  capableModel(),
			assert: func(t *testing.T, err error) {
				var target *judge.QuoteNotFoundError
				if !errors.As(err, &target) {
					t.Fatalf("err = %v, want *QuoteNotFoundError", err)
				}
			},
		},
		{
			name:   "inference error",
			client: &fakeClient{err: errBoom},
			model:  capableModel(),
			assert: func(t *testing.T, err error) {
				var target *judge.InferenceError
				if !errors.As(err, &target) {
					t.Fatalf("err = %v, want *InferenceError", err)
				}
				if !errors.Is(err, errBoom) {
					t.Fatalf("InferenceError does not unwrap to the transport error")
				}
			},
		},
		{
			name:   "unsupported structured output",
			client: &fakeClient{resp: respText(`{"score":0.9,"reason":"x","evidence":[]}`)},
			model:  incapableModel(),
			assert: func(t *testing.T, err error) {
				var target *judge.UnsupportedStructuredOutputError
				if !errors.As(err, &target) {
					t.Fatalf("err = %v, want *UnsupportedStructuredOutputError", err)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			j := judge.New(rubric.AnswerRelevanceV1, tt.client, template(tt.model))
			a, err := j.Evaluate(context.Background(), sampleOf(conv()))
			if err == nil {
				t.Fatalf("expected an error, got assessment %+v", a)
			}
			// Fail secure: a failed judge NEVER yields a pass.
			if a.Status == eval.StatusPass {
				t.Fatal("failed judge produced a pass assessment")
			}
			tt.assert(t, err)
		})
	}
}

// TestJudgeUnsupportedNeverCallsModel confirms the judge fails before invoking a
// model that cannot satisfy the schema, rather than calling it and guessing.
func TestJudgeUnsupportedNeverCallsModel(t *testing.T) {
	t.Parallel()
	fc := &fakeClient{resp: respText(`{"score":0.9,"reason":"x","evidence":[]}`)}
	j := judge.New(rubric.AnswerRelevanceV1, fc, template(incapableModel()))
	if _, err := j.Evaluate(context.Background(), sampleOf(conv())); err == nil {
		t.Fatal("expected an unsupported-structured-output error")
	}
	if fc.calls != 0 {
		t.Fatalf("client was invoked %d times, want 0 for an unsupported model", fc.calls)
	}
}

func TestJudgeTimeout(t *testing.T) {
	t.Parallel()

	fc := &fakeClient{resp: respText(`{"score":0.9,"reason":"x","evidence":[]}`)}
	j := judge.New(rubric.AnswerRelevanceV1, fc, template(capableModel()))

	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	a, err := j.Evaluate(ctx, sampleOf(conv()))
	if err == nil {
		t.Fatalf("expected a timeout error, got assessment %+v", a)
	}
	if a.Status == eval.StatusPass {
		t.Fatal("timed-out judge produced a pass assessment")
	}
	var target *judge.InferenceError
	if !errors.As(err, &target) {
		t.Fatalf("err = %v, want *InferenceError", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout error does not unwrap to context.DeadlineExceeded: %v", err)
	}
}

func TestJudgeRejectsInvalidRubric(t *testing.T) {
	t.Parallel()
	bad := rubric.AnswerRelevanceV1
	bad.Definition = "" // invalidate
	fc := &fakeClient{resp: respText(`{"score":0.9,"reason":"x","evidence":[]}`)}
	j := judge.New(bad, fc, template(capableModel()))

	_, err := j.Evaluate(context.Background(), sampleOf(conv()))
	var target *judge.RubricInvalidError
	if !errors.As(err, &target) {
		t.Fatalf("err = %v, want *RubricInvalidError", err)
	}
	if fc.calls != 0 {
		t.Fatalf("client invoked for an invalid rubric (%d calls)", fc.calls)
	}
}
