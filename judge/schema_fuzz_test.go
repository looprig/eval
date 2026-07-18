package judge_test

import (
	"testing"

	"github.com/looprig/core/content"
	"github.com/looprig/eval/judge"
)

// FuzzScoreSchemaDecode drives the score-schema parser with arbitrary bytes: the
// model output it decodes is external, untrusted input, so it must never panic
// and a successful decode must survive local re-validation without panicking
// either. The fuzzer asserts the boundary is total, not that any given input is
// accepted.
func FuzzScoreSchemaDecode(f *testing.F) {
	seeds := []string{
		`{"score":0.5,"reason":"ok","evidence":[]}`,
		`{"score":0.9,"reason":"good","evidence":[{"message_index":0,"quote":"hi"}]}`,
		`{"score":5,"reason":"x","evidence":[]}`,
		`{"score": `,
		`{}`,
		``,
		`[]`,
		`{"score":0.1,"reason":"x","evidence":[{"message_index":-1,"quote":""}]}`,
		`{"score":0.1,"reason":"x","evidence":[{"message_index":0,"quote":"nope"}],"extra":1}`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	conv := content.AgenticMessages{
		&content.UserMessage{Message: content.Message{
			Role:   content.RoleUser,
			Blocks: []content.Block{&content.TextBlock{Text: "hi there"}},
		}},
	}

	f.Fuzz(func(t *testing.T, raw []byte) {
		out, err := judge.ScoreSchemaV1.Decode(raw)
		if err != nil {
			return
		}
		// A clean decode must be validatable without panicking; the result
		// (accept or a typed reject) is not asserted here.
		_ = judge.ScoreSchemaV1.Validate(out, 0, 1, conv)
	})
}
