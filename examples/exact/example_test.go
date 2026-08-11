package exactexample_test

import (
	"context"
	"fmt"

	"github.com/looprig/core/content"
	"github.com/looprig/eval"
	"github.com/looprig/eval/exact"
)

type scriptedTarget struct{}

func (scriptedTarget) Name() string { return "capital-answer" }

func (scriptedTarget) Observe(_ context.Context, scenario eval.Scenario) (eval.Observation, error) {
	conversation := append(content.AgenticMessages(nil), scenario.Input...)
	conversation = append(conversation, assistantText("Paris is the capital of France."))
	return eval.Observation{
		Conversation: conversation,
		Scope:        eval.ScopeCase,
		Subject: eval.Subject{
			ID:       "scripted-target",
			Kind:     eval.SubjectAgent,
			Name:     scenario.Name,
			Revision: scenario.Revision,
		},
	}, nil
}

func userText(text string) *content.UserMessage {
	return &content.UserMessage{Message: content.Message{
		Role: content.RoleUser,
		Blocks: []content.Block{
			&content.TextBlock{Text: text},
		},
	}}
}

func assistantText(text string) *content.AIMessage {
	return &content.AIMessage{Message: content.Message{
		Role: content.RoleAssistant,
		Blocks: []content.Block{
			&content.TextBlock{Text: text},
		},
	}}
}

func Example_runExactGate() {
	suite := eval.Suite{
		Name:     "capital-smoke",
		Revision: "v1",
		Scenarios: []eval.Scenario{{
			ID:       "france-capital",
			Name:     "capital-answer",
			Revision: "v1",
			Input:    content.AgenticMessages{userText("What is the capital of France?")},
		}},
	}

	report, err := eval.Run(
		context.Background(),
		eval.RunConfig{},
		suite,
		scriptedTarget{},
		exact.RequiredText("Paris"),
		exact.ForbiddenText("London"),
	)
	if err != nil {
		panic(err)
	}

	fmt.Printf("samples=%d pass=%d fail=%d\n",
		report.Summary.Samples,
		report.Summary.Assessments[eval.StatusPass],
		report.Summary.Assessments[eval.StatusFail],
	)
	// Output:
	// samples=1 pass=2 fail=0
}
