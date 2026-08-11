package judgeexample_test

import (
	"context"
	"errors"
	"fmt"

	"github.com/looprig/core/content"
	"github.com/looprig/eval"
	"github.com/looprig/eval/judge"
	"github.com/looprig/eval/rubric"
	"github.com/looprig/inference"
	"github.com/looprig/inference/model"
	"github.com/looprig/inference/stream"
)

type scriptedClient struct {
	strict bool
}

func (c *scriptedClient) Invoke(ctx context.Context, req inference.Request) (*inference.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.strict = req.Output != nil && req.Output.Strict
	return &inference.Response{
		Message: assistantText(`{"score":0.9,"reason":"direct answer","evidence":[{"message_index":1,"quote":"Paris"}]}`),
		Usage: &content.Usage{
			InputTokens:  24,
			OutputTokens: 9,
		},
		Model:        "scripted-judge",
		FinishReason: stream.FinishReasonStop,
	}, nil
}

func (*scriptedClient) Stream(context.Context, inference.Request) (*stream.StreamReader[content.Chunk], error) {
	return nil, errors.New("streaming is not used by the judge")
}

func userText(text string) *content.UserMessage {
	return &content.UserMessage{Message: content.Message{
		Role:   content.RoleUser,
		Blocks: []content.Block{&content.TextBlock{Text: text}},
	}}
}

func assistantText(text string) *content.AIMessage {
	return &content.AIMessage{Message: content.Message{
		Role:   content.RoleAssistant,
		Blocks: []content.Block{&content.TextBlock{Text: text}},
	}}
}

func Example_runScriptedJudge() {
	client := &scriptedClient{}
	judgeModel := model.CustomModel(
		"scripted",
		"offline",
		"",
		"scripted-judge",
		model.WithStructuredOutput(),
	)
	evaluator := judge.New(
		rubric.AnswerRelevanceV1,
		client,
		inference.Request{Model: judgeModel},
	)
	sample := eval.Sample{Observation: eval.Observation{
		Conversation: content.AgenticMessages{
			userText("What is the capital of France?"),
			assistantText("The capital of France is Paris."),
		},
	}}

	assessment, err := evaluator.Evaluate(context.Background(), sample)
	if err != nil {
		panic(err)
	}
	fmt.Printf("status=%s score=%.1f strict=%t\n",
		assessment.Status,
		assessment.Measurements[0].Value,
		client.strict,
	)
	// Output:
	// status=pass score=0.9 strict=true
}
