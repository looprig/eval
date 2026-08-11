package reportexample_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/looprig/core/content"
	"github.com/looprig/eval"
	"github.com/looprig/eval/exact"
	"github.com/looprig/eval/reportjson"
)

type scriptedTarget struct{}

func (scriptedTarget) Name() string { return "report-target" }

func (scriptedTarget) Observe(_ context.Context, scenario eval.Scenario) (eval.Observation, error) {
	return eval.Observation{
		Conversation: content.AgenticMessages{assistantText("ready")},
		Scope:        eval.ScopeCase,
		Subject: eval.Subject{
			ID:       "report-target",
			Kind:     eval.SubjectAgent,
			Name:     scenario.Name,
			Revision: scenario.Revision,
		},
	}, nil
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

func Example_writeReport() {
	suite := eval.Suite{
		Name:     "report-suite",
		Revision: "v1",
		Scenarios: []eval.Scenario{{
			ID:       "ready-check",
			Name:     "report-target",
			Revision: "v1",
			Input:    content.AgenticMessages{userText("Are you ready?")},
		}},
	}
	report, err := eval.Run(context.Background(), eval.RunConfig{}, suite, scriptedTarget{}, exact.RequiredText("ready"))
	if err != nil {
		panic(err)
	}

	dir, err := os.MkdirTemp("", "looprig-eval-report-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	if err := reportjson.NewFileSink(dir).WriteReport(context.Background(), report); err != nil {
		panic(err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, report.ID+".json"))
	if err != nil {
		panic(err)
	}
	decoded, err := reportjson.Decode(raw)
	if err != nil {
		panic(err)
	}
	fmt.Printf("id=%s samples=%d pass=%d\n",
		decoded.ID,
		decoded.Summary.Samples,
		decoded.Summary.Assessments[eval.StatusPass],
	)
	// Output:
	// id=report-suite@v1 samples=1 pass=1
}
