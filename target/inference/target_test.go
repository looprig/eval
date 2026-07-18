package inference

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/looprig/core/content"
	"github.com/looprig/eval"
	llm "github.com/looprig/inference"
	"github.com/looprig/inference/model"
	"github.com/looprig/inference/stream"
)

// errStreamUnsupported is returned by the fake client's Stream method: the
// target never streams, so the fake refuses rather than returning a nil reader.
var errStreamUnsupported = errors.New("fake: stream unsupported")

// fakeClient is a canned inference.Client. It records the requests it receives so
// tests can assert on request assembly, and returns a pre-seeded response/error.
// Capture is mutex-guarded so concurrent Observe calls exercise -race cleanly.
type fakeClient struct {
	mu       sync.Mutex
	resp     *llm.Response
	err      error
	honorCtx bool
	gotReqs  []llm.Request
}

func (f *fakeClient) Invoke(ctx context.Context, req llm.Request) (*llm.Response, error) {
	if f.honorCtx {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	f.mu.Lock()
	f.gotReqs = append(f.gotReqs, req)
	f.mu.Unlock()
	return f.resp, f.err
}

func (f *fakeClient) Stream(context.Context, llm.Request) (*stream.StreamReader[content.Chunk], error) {
	return nil, errStreamUnsupported
}

func (f *fakeClient) requests() []llm.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]llm.Request, len(f.gotReqs))
	copy(out, f.gotReqs)
	return out
}

// testModel builds a minimal secret-free model descriptor with the given wire id.
func testModel(name string) model.Model {
	return model.CustomModel(model.ProviderName("test"), model.APIFormat("test"), "", name)
}

// userText builds a user message carrying a single text block.
func userText(text string) *content.UserMessage {
	return &content.UserMessage{Message: content.Message{
		Role:   content.RoleUser,
		Blocks: []content.Block{&content.TextBlock{Text: text}},
	}}
}

// aiText builds an assistant message carrying a single text block.
func aiText(text string, usage *content.Usage) *content.AIMessage {
	return &content.AIMessage{
		Message: content.Message{
			Role:   content.RoleAssistant,
			Blocks: []content.Block{&content.TextBlock{Text: text}},
		},
		Usage: usage,
	}
}

// okResponse builds a well-formed response with an assistant message and usage.
func okResponse(text string, u *content.Usage) *llm.Response {
	return &llm.Response{Message: aiText(text, u), Usage: u, Model: "test-model"}
}

// pinnedClock returns a clock that yields the supplied instants in order, then
// repeats the last one. It lets a test pin StartedAt/EndedAt deterministically.
func pinnedClock(times ...time.Time) func() time.Time {
	var i int
	return func() time.Time {
		t := times[i]
		if i < len(times)-1 {
			i++
		}
		return t
	}
}

func TestObserve_AppendsScenarioMessagesRespectingTemplate(t *testing.T) {
	t.Parallel()

	fake := &fakeClient{resp: okResponse("hello", &content.Usage{InputTokens: 3, OutputTokens: 2})}
	template := llm.Request{
		Model:    testModel("m1"),
		System:   "you are a fixture",
		Messages: content.AgenticMessages{userText("preamble")},
	}
	tgt := NewTarget(fake, template)

	sc := eval.Scenario{
		ID:       "s1",
		Name:     "case",
		Revision: "r1",
		Input:    content.AgenticMessages{userText("first"), userText("second")},
	}
	if _, err := tgt.Observe(context.Background(), sc); err != nil {
		t.Fatalf("Observe: %v", err)
	}

	reqs := fake.requests()
	if len(reqs) != 1 {
		t.Fatalf("Invoke called %d times, want 1", len(reqs))
	}
	got := reqs[0]
	if got.System != "you are a fixture" {
		t.Errorf("System = %q, want template System preserved", got.System)
	}
	if got.Model.Name != "m1" {
		t.Errorf("Model.Name = %q, want template model preserved", got.Model.Name)
	}
	if len(got.Messages) != 3 {
		t.Fatalf("request messages = %d, want 3 (1 template + 2 input)", len(got.Messages))
	}
	if got.Messages[0] != template.Messages[0] {
		t.Errorf("first request message is not the template message")
	}
	if got.Messages[1] != sc.Input[0] || got.Messages[2] != sc.Input[1] {
		t.Errorf("scenario input not appended in order")
	}
}

func TestObserve_ProjectsResponseSubjectAndUsage(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	end := start.Add(1500 * time.Millisecond)
	usage := &content.Usage{InputTokens: 11, OutputTokens: 7, CacheReadTokens: 2}

	fake := &fakeClient{resp: okResponse("the answer", usage)}
	template := llm.Request{Model: testModel("gpt-eval")}
	tgt := NewTarget(fake, template, WithClock(pinnedClock(start, end)))

	sc := eval.Scenario{ID: "s1", Name: "case", Revision: "r1", Input: content.AgenticMessages{userText("q")}}
	obs, err := tgt.Observe(context.Background(), sc)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}

	if err := obs.Validate(); err != nil {
		t.Fatalf("Observation.Validate: %v", err)
	}

	// Conversation = scenario input + returned assistant message, in order.
	if len(obs.Conversation) != 2 {
		t.Fatalf("conversation length = %d, want 2", len(obs.Conversation))
	}
	if obs.Conversation[0] != sc.Input[0] {
		t.Errorf("conversation[0] is not the scenario input message")
	}
	if obs.Conversation[1] != fake.resp.Message {
		t.Errorf("conversation[1] is not the returned assistant message")
	}

	// Subject: model kind, identity from the model.
	if obs.Subject.Kind != eval.SubjectModel {
		t.Errorf("Subject.Kind = %q, want model", obs.Subject.Kind)
	}
	if obs.Subject.Name != "gpt-eval" {
		t.Errorf("Subject.Name = %q, want gpt-eval", obs.Subject.Name)
	}
	if obs.Subject.Revision != "gpt-eval" {
		t.Errorf("Subject.Revision = %q, want gpt-eval", obs.Subject.Revision)
	}
	if obs.Subject.ID == "" {
		t.Errorf("Subject.ID must be non-empty")
	}

	// Trace timing pinned by the injected clock.
	if !obs.Trace.StartedAt.Equal(start) || !obs.Trace.EndedAt.Equal(end) {
		t.Errorf("trace time = [%v,%v], want [%v,%v]", obs.Trace.StartedAt, obs.Trace.EndedAt, start, end)
	}
	if obs.Trace.Model != "gpt-eval" {
		t.Errorf("Trace.Model = %q, want gpt-eval", obs.Trace.Model)
	}

	// Exactly one inference operation, OK, pinned times.
	if len(obs.Trace.Operations) != 1 {
		t.Fatalf("operations = %d, want 1", len(obs.Trace.Operations))
	}
	op := obs.Trace.Operations[0]
	if op.Kind != eval.OperationInference || op.Status != eval.OperationOK {
		t.Errorf("operation = {%q,%q}, want {inference,ok}", op.Kind, op.Status)
	}
	if !op.StartedAt.Equal(start) || !op.EndedAt.Equal(end) {
		t.Errorf("operation time = [%v,%v], want [%v,%v]", op.StartedAt, op.EndedAt, start, end)
	}
	if len(op.Attributes) != 0 {
		t.Errorf("operation carries %d attributes, want 0 (no metadata surface for leaks)", len(op.Attributes))
	}

	// Timing + usage evidence, projected from the response.
	var timing *eval.TimingEvidence
	var usageEv *eval.UsageEvidence
	for _, ev := range obs.Trace.Evidence {
		switch ev.Kind {
		case eval.EvidenceTiming:
			timing = ev.Timing
		case eval.EvidenceUsage:
			usageEv = ev.Usage
		}
	}
	if timing == nil {
		t.Fatal("no timing evidence")
	}
	if timing.Duration != 1500*time.Millisecond {
		t.Errorf("timing.Duration = %v, want 1.5s", timing.Duration)
	}
	if usageEv == nil {
		t.Fatal("no usage evidence")
	}
	if usageEv.Usage != *usage {
		t.Errorf("usage evidence = %+v, want %+v", usageEv.Usage, *usage)
	}
	if usageEv.Model != "gpt-eval" {
		t.Errorf("usage evidence model = %q, want gpt-eval", usageEv.Model)
	}
}

func TestObserve_NilUsageProjectsZeroCounts(t *testing.T) {
	t.Parallel()

	fake := &fakeClient{resp: &llm.Response{Message: aiText("hi", nil), Usage: nil}}
	tgt := NewTarget(fake, llm.Request{Model: testModel("m")})
	sc := eval.Scenario{ID: "s", Name: "c", Revision: "r", Input: content.AgenticMessages{userText("q")}}

	obs, err := tgt.Observe(context.Background(), sc)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if err := obs.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	var usageEv *eval.UsageEvidence
	for _, ev := range obs.Trace.Evidence {
		if ev.Kind == eval.EvidenceUsage {
			usageEv = ev.Usage
		}
	}
	if usageEv == nil {
		t.Fatal("no usage evidence")
	}
	if (usageEv.Usage != content.Usage{}) {
		t.Errorf("usage = %+v, want zero for nil response usage", usageEv.Usage)
	}
}

func TestObserve_InferenceErrorIsTypedAndContentFree(t *testing.T) {
	t.Parallel()

	secret := "provider-secret-leak-abc123"
	cause := errors.New(secret)
	fake := &fakeClient{err: cause}
	tgt := NewTarget(fake, llm.Request{Model: testModel("m")})
	sc := eval.Scenario{ID: "s", Name: "c", Revision: "r", Input: content.AgenticMessages{userText("q")}}

	_, err := tgt.Observe(context.Background(), sc)
	if err == nil {
		t.Fatal("expected error")
	}
	var infErr *InferenceError
	if !errors.As(err, &infErr) {
		t.Fatalf("error = %T, want *InferenceError", err)
	}
	if !errors.Is(err, cause) {
		t.Errorf("errors.Is could not reach the wrapped cause")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("error string leaked provider text: %q", err.Error())
	}
}

func TestObserve_NilResponseAndNilMessage(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		resp *llm.Response
	}{
		{"nil response", nil},
		{"nil message", &llm.Response{Message: nil}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fake := &fakeClient{resp: tc.resp}
			tgt := NewTarget(fake, llm.Request{Model: testModel("m")})
			sc := eval.Scenario{ID: "s", Name: "c", Revision: "r", Input: content.AgenticMessages{userText("q")}}

			_, err := tgt.Observe(context.Background(), sc)
			if err == nil {
				t.Fatal("expected error, got nil (and no panic)")
			}
			var empty *EmptyResponseError
			if !errors.As(err, &empty) {
				t.Fatalf("error = %T, want *EmptyResponseError", err)
			}
		})
	}
}

func TestObserve_ContextCancellation(t *testing.T) {
	t.Parallel()

	fake := &fakeClient{honorCtx: true, resp: okResponse("unused", nil)}
	tgt := NewTarget(fake, llm.Request{Model: testModel("m")})
	sc := eval.Scenario{ID: "s", Name: "c", Revision: "r", Input: content.AgenticMessages{userText("q")}}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := tgt.Observe(ctx, sc)
	if err == nil {
		t.Fatal("expected error on cancelled context")
	}
	var infErr *InferenceError
	if !errors.As(err, &infErr) {
		t.Fatalf("error = %T, want *InferenceError", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("errors.Is(err, context.Canceled) = false; cancellation not propagated")
	}
}

func TestObserve_InvalidModelIdentityFailsBeforeInvoke(t *testing.T) {
	t.Parallel()

	fake := &fakeClient{resp: okResponse("x", nil)}
	// A wire model id longer than the identity bound cannot be a valid eval Name.
	tgt := NewTarget(fake, llm.Request{Model: testModel(strings.Repeat("a", eval.MaxNameBytes+1))})
	sc := eval.Scenario{ID: "s", Name: "c", Revision: "r", Input: content.AgenticMessages{userText("q")}}

	_, err := tgt.Observe(context.Background(), sc)
	if err == nil {
		t.Fatal("expected identity error")
	}
	var idErr *IdentityError
	if !errors.As(err, &idErr) {
		t.Fatalf("error = %T, want *IdentityError", err)
	}
	if n := len(fake.requests()); n != 0 {
		t.Errorf("Invoke called %d times, want 0 (fail before calling the model)", n)
	}
}

func TestObserve_NoSecretLeakIntoObservation(t *testing.T) {
	t.Parallel()

	secret := "SYSTEM-PROMPT-SECRET-9f8e7d"
	fake := &fakeClient{resp: okResponse("answer", &content.Usage{InputTokens: 1, OutputTokens: 1})}
	template := llm.Request{
		Model:  testModel("m"),
		System: secret, // a caller placing sensitive instruction text on the template
	}
	tgt := NewTarget(fake, template)
	sc := eval.Scenario{ID: "s", Name: "c", Revision: "r", Input: content.AgenticMessages{userText("q")}}

	obs, err := tgt.Observe(context.Background(), sc)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}

	// The trace/subject/evidence must not carry the system prompt text.
	blob := mustJSON(t, obs.Subject) + mustJSON(t, obs.Trace)
	if strings.Contains(blob, secret) {
		t.Errorf("secret leaked into trace/subject metadata")
	}

	// Positive: the projected SAFE content MUST be present in the SAME marshaled
	// bytes, so this leak test cannot pass on an empty/blank Observation. The
	// usage token counts and model revision are safe and expected.
	for _, want := range []string{`"InputTokens":1`, `"OutputTokens":1`} {
		if !strings.Contains(blob, want) {
			t.Errorf("expected safe usage content %q missing from observation:\n%s", want, blob)
		}
	}
	if obs.Trace.Model != "m" {
		t.Errorf("Trace.Model = %q, want m (projected model revision)", obs.Trace.Model)
	}
	if obs.Subject.Revision != "m" {
		t.Errorf("Subject.Revision = %q, want m (projected model revision)", obs.Subject.Revision)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

func TestObserve_TemplateNotMutated(t *testing.T) {
	t.Parallel()

	// Give the template slices spare capacity so a naive append would scribble
	// into their backing arrays.
	msgs := make(content.AgenticMessages, 1, 8)
	msgs[0] = userText("template")
	tools := make([]llm.Tool, 1, 8)
	tools[0] = llm.Tool{Name: "t", Description: "d"}
	template := llm.Request{Model: testModel("m"), Messages: msgs, Tools: tools}

	before := struct {
		msgs  content.AgenticMessages
		tools []llm.Tool
	}{
		msgs:  append(content.AgenticMessages(nil), template.Messages...),
		tools: append([]llm.Tool(nil), template.Tools...),
	}

	fake := &fakeClient{resp: okResponse("x", nil)}
	tgt := NewTarget(fake, template)
	sc := eval.Scenario{ID: "s", Name: "c", Revision: "r", Input: content.AgenticMessages{userText("a"), userText("b")}}
	if _, err := tgt.Observe(context.Background(), sc); err != nil {
		t.Fatalf("Observe: %v", err)
	}

	if len(template.Messages) != len(before.msgs) {
		t.Fatalf("template.Messages length changed: %d -> %d", len(before.msgs), len(template.Messages))
	}
	for i := range before.msgs {
		if template.Messages[i] != before.msgs[i] {
			t.Errorf("template.Messages[%d] mutated", i)
		}
	}
	if !reflect.DeepEqual(template.Tools, before.tools) {
		t.Errorf("template.Tools mutated")
	}
}

func TestObserve_ConcurrentNoInterference(t *testing.T) {
	t.Parallel()

	template := llm.Request{
		Model:    testModel("m"),
		Messages: content.AgenticMessages{userText("preamble")},
	}
	fake := &fakeClient{resp: okResponse("x", nil)}
	tgt := NewTarget(fake, template)

	const n = 32
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			unique := "input-" + strconv.Itoa(i)
			sc := eval.Scenario{ID: "s", Name: "c", Revision: "r", Input: content.AgenticMessages{userText(unique)}}
			if _, err := tgt.Observe(context.Background(), sc); err != nil {
				t.Errorf("Observe: %v", err)
			}
		}(i)
	}
	wg.Wait()

	// Every captured request must have exactly its own input appended after the
	// shared template preamble — no cross-contamination through a shared backing.
	reqs := fake.requests()
	if len(reqs) != n {
		t.Fatalf("captured %d requests, want %d", len(reqs), n)
	}
	seen := make(map[string]struct{}, n)
	for _, r := range reqs {
		if len(r.Messages) != 2 {
			t.Fatalf("request has %d messages, want 2", len(r.Messages))
		}
		um, ok := r.Messages[1].(*content.UserMessage)
		if !ok || len(um.Blocks) != 1 {
			t.Fatalf("unexpected appended message shape")
		}
		tb, ok := um.Blocks[0].(*content.TextBlock)
		if !ok {
			t.Fatalf("unexpected block type")
		}
		seen[tb.Text] = struct{}{}
	}
	if len(seen) != n {
		t.Errorf("distinct appended inputs = %d, want %d (interference detected)", len(seen), n)
	}
}

func TestName(t *testing.T) {
	t.Parallel()

	fake := &fakeClient{resp: okResponse("x", nil)}

	if got := NewTarget(fake, llm.Request{Model: testModel("model-x")}).Name(); got != "model-x" {
		t.Errorf("Name() = %q, want model-x (from template model)", got)
	}
	if got := NewTarget(fake, llm.Request{Model: testModel("model-x")}, WithName("override")).Name(); got != "override" {
		t.Errorf("Name() = %q, want override", got)
	}
}
