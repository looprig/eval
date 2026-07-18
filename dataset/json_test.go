package dataset_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/looprig/core/content"
	"github.com/looprig/eval"
	"github.com/looprig/eval/dataset"
)

// newlineByte is the JSONL record separator, used to assemble raw multi-record
// bodies in tests.
const newlineByte = '\n'

// sampleConversation builds a thread exercising every message variant and the
// type-specific fields the codec must preserve: a system prompt, a user turn, an
// assistant turn carrying a tool-use block and AI usage, and a tool-result
// message carrying a tool-result block flagged IsError.
func sampleConversation() content.AgenticMessages {
	return content.AgenticMessages{
		&content.SystemMessage{Message: content.Message{
			Role:   content.RoleSystem,
			Blocks: []content.Block{&content.TextBlock{Text: "be helpful"}},
		}},
		&content.UserMessage{Message: content.Message{
			Role:   content.RoleUser,
			Blocks: []content.Block{&content.TextBlock{Text: "search for x"}},
		}},
		&content.AIMessage{
			Message: content.Message{
				Role: content.RoleAssistant,
				Blocks: []content.Block{&content.ToolUseBlock{
					ID:    "call-1",
					Name:  "search",
					Input: json.RawMessage(`{"q":"x"}`),
				}},
			},
			Usage: &content.Usage{InputTokens: 10, OutputTokens: 5, ReasoningTokens: 2},
		},
		&content.ToolResultMessage{
			Message: content.Message{
				Role: content.RoleTool,
				Blocks: []content.Block{&content.ToolResultBlock{
					ToolUseID: "call-1",
					Content:   []content.Block{&content.TextBlock{Text: "boom"}},
					IsError:   true,
				}},
			},
			ToolUseID: "call-1",
			IsError:   true,
		},
	}
}

// sampleScenario builds a valid scenario with the rich conversation above and a
// populated expectation and label set, so a round-trip proves every field
// survives.
func sampleScenario(id string) eval.Scenario {
	maxCalls := 3
	return eval.Scenario{
		ID:       id,
		Name:     "target",
		Revision: "rev-1",
		Input:    sampleConversation(),
		Expectation: &eval.Expectation{
			RequiredFacts:    []eval.Fact{"x was found"},
			ForbiddenActions: []eval.ActionName{"delete_all"},
			ExpectedToolCalls: []eval.ToolCallExpectation{
				{Tool: "search", MinCount: 1, MaxCount: &maxCalls},
			},
			StructuredOutput: &eval.StructuredOutputExpectation{Schema: "schema-1", Strict: true},
			ReferenceAnswers: []eval.ReferenceAnswer{"the golden answer"},
			PolicyRef:        "policy-1",
		},
		Labels: []eval.Label{
			{Key: "suite", Value: "smoke"},
			{Key: "tier", Value: "1"},
		},
	}
}

func TestRoundTrip(t *testing.T) {
	t.Parallel()

	want := sampleScenario("case-1")

	line, err := dataset.EncodeRecord(want)
	if err != nil {
		t.Fatalf("EncodeRecord: %v", err)
	}

	got, err := dataset.DecodeRecord(line)
	if err != nil {
		t.Fatalf("DecodeRecord: %v", err)
	}

	if !reflect.DeepEqual(want, got) {
		t.Fatalf("round-trip mismatch:\n want %#v\n  got %#v", want, got)
	}

	// Explicitly assert the ordered conversation and the type-specific fields
	// survived, not just that DeepEqual passed.
	if len(got.Input) != 4 {
		t.Fatalf("Input length = %d, want 4", len(got.Input))
	}
	ai, ok := got.Input[2].(*content.AIMessage)
	if !ok {
		t.Fatalf("Input[2] type = %T, want *content.AIMessage", got.Input[2])
	}
	if ai.Usage == nil || ai.Usage.InputTokens != 10 || ai.Usage.OutputTokens != 5 {
		t.Fatalf("AI usage not preserved: %#v", ai.Usage)
	}
	tr, ok := got.Input[3].(*content.ToolResultMessage)
	if !ok {
		t.Fatalf("Input[3] type = %T, want *content.ToolResultMessage", got.Input[3])
	}
	if !tr.IsError || tr.ToolUseID != "call-1" {
		t.Fatalf("tool-result message fields not preserved: %#v", tr)
	}
}

func TestJSONLOrdering(t *testing.T) {
	t.Parallel()

	want := []string{"a", "b", "c", "d"}
	scenarios := make([]eval.Scenario, len(want))
	for i, id := range want {
		scenarios[i] = sampleScenario(id)
	}

	var buf bytes.Buffer
	if err := dataset.Encode(&buf, scenarios); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	// One record per line.
	if lines := strings.Count(strings.TrimRight(buf.String(), "\n"), "\n") + 1; lines != len(want) {
		t.Fatalf("wrote %d lines, want %d", lines, len(want))
	}

	ds, err := dataset.Decode(context.Background(), &buf, "mem")
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if len(ds.Scenarios) != len(want) {
		t.Fatalf("decoded %d scenarios, want %d", len(ds.Scenarios), len(want))
	}
	for i, id := range want {
		if ds.Scenarios[i].ID != id {
			t.Fatalf("order not preserved: position %d id = %q, want %q", i, ds.Scenarios[i].ID, id)
		}
	}
}

// encodeRawEnvelope builds a raw JSONL line with an arbitrary version token,
// reusing the codec's own scenario payload so only the version differs.
func encodeRawEnvelope(t *testing.T, version string, sc eval.Scenario) []byte {
	t.Helper()
	line, err := dataset.EncodeRecord(sc)
	if err != nil {
		t.Fatalf("EncodeRecord: %v", err)
	}
	var env map[string]json.RawMessage
	if err := json.Unmarshal(line, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	v, _ := json.Marshal(version)
	env["version"] = v
	out, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	return out
}

func TestDecodeRecordErrors(t *testing.T) {
	t.Parallel()

	valid := sampleScenario("case-1")
	validLine, err := dataset.EncodeRecord(valid)
	if err != nil {
		t.Fatalf("EncodeRecord: %v", err)
	}

	tests := []struct {
		name   string
		input  []byte
		target error
	}{
		{
			name:   "unknown version",
			input:  encodeRawEnvelope(t, "dataset/v99", valid),
			target: new(dataset.UnknownVersionError),
		},
		{
			name:   "missing version",
			input:  encodeRawEnvelope(t, "", valid),
			target: new(dataset.UnknownVersionError),
		},
		{
			name:   "malformed json",
			input:  []byte(`{"version":"dataset/v1","scenario":`),
			target: new(dataset.MalformedRecordError),
		},
		{
			name:   "invalid utf-8",
			input:  append([]byte(`{"version":"dataset/v1","scenario":{"id":"`), []byte{0xff, 0xfe}...),
			target: new(dataset.MalformedRecordError),
		},
		{
			name:   "trailing data",
			input:  append(append([]byte{}, validLine...), []byte(" trailing")...),
			target: new(dataset.MalformedRecordError),
		},
		{
			name:   "oversize record",
			input:  bytes.Repeat([]byte("a"), dataset.MaxRecordBytes+1),
			target: new(dataset.RecordTooLargeError),
		},
		{
			name:   "invalid scenario (empty id)",
			input:  []byte(`{"version":"dataset/v1","scenario":{"id":"","name":"n","revision":"r","input":[{"role":"user","blocks":[{"type":"text","Text":"hi"}]}]}}`),
			target: new(dataset.InvalidScenarioError),
		},
		{
			name:   "unknown message role",
			input:  []byte(`{"version":"dataset/v1","scenario":{"id":"x","name":"n","revision":"r","input":[{"role":"robot","blocks":[]}]}}`),
			target: new(dataset.MalformedRecordError),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := dataset.DecodeRecord(tt.input)
			if err == nil {
				t.Fatalf("DecodeRecord(%s) = nil error, want %T", tt.name, tt.target)
			}
			if !errors.As(err, targetPtr(tt.target)) {
				t.Fatalf("DecodeRecord(%s) error = %v (%T), want %T", tt.name, err, err, tt.target)
			}
		})
	}
}

// targetPtr returns a **T for errors.As from a *T sentinel.
func targetPtr(target error) any {
	return reflect.New(reflect.TypeOf(target)).Interface()
}

func TestDuplicateScenario(t *testing.T) {
	t.Parallel()

	// Encode rejects duplicates, so build the duplicate JSONL body directly.
	line, err := dataset.EncodeRecord(sampleScenario("dup"))
	if err != nil {
		t.Fatalf("EncodeRecord: %v", err)
	}
	body := append(append(append([]byte{}, line...), newlineByte), append(append([]byte{}, line...), newlineByte)...)

	_, err = dataset.Decode(context.Background(), bytes.NewReader(body), "mem")
	var dup *dataset.DuplicateScenarioError
	if !errors.As(err, &dup) {
		t.Fatalf("Decode error = %v (%T), want *DuplicateScenarioError", err, err)
	}
	if dup.Line != 2 {
		t.Fatalf("duplicate reported at line %d, want 2", dup.Line)
	}
}

func TestDecodeFileTooLarge(t *testing.T) {
	t.Parallel()

	_, err := dataset.Decode(context.Background(), &filler{}, "big")
	var big *dataset.FileTooLargeError
	if !errors.As(err, &big) {
		t.Fatalf("Decode error = %v (%T), want *FileTooLargeError", err, err)
	}
}

// filler is an endless reader of a non-newline byte, used to exceed MaxFileBytes
// without allocating a literal.
type filler struct{}

func (filler) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'a'
	}
	return len(p), nil
}

func TestDecodeBlankLineRejected(t *testing.T) {
	t.Parallel()

	line, err := dataset.EncodeRecord(sampleScenario("a"))
	if err != nil {
		t.Fatalf("EncodeRecord: %v", err)
	}
	// A blank line between two records is malformed.
	body := append(append(append([]byte{}, line...), []byte("\n\n")...), line...)

	_, err = dataset.Decode(context.Background(), bytes.NewReader(body), "mem")
	var mal *dataset.MalformedRecordError
	if !errors.As(err, &mal) {
		t.Fatalf("Decode error = %v (%T), want *MalformedRecordError", err, err)
	}
}

func TestLoadHappyPath(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	line, err := dataset.EncodeRecord(sampleScenario("only"))
	if err != nil {
		t.Fatalf("EncodeRecord: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "data.jsonl"), append(line, '\n'), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	ds, err := dataset.Load(context.Background(), dir, "data.jsonl")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(ds.Scenarios) != 1 || ds.Scenarios[0].ID != "only" {
		t.Fatalf("Load returned %#v", ds.Scenarios)
	}
}

func TestLoadMissingFile(t *testing.T) {
	t.Parallel()

	_, err := dataset.Load(context.Background(), t.TempDir(), "nope.jsonl")
	var open *dataset.OpenError
	if !errors.As(err, &open) {
		t.Fatalf("Load error = %v (%T), want *OpenError", err, err)
	}
}

func TestSymlinkEscape(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	root := filepath.Join(base, "root")
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	secret := filepath.Join(outside, "secret.jsonl")
	if err := os.WriteFile(secret, []byte("secret\n"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	// A symlink inside root that points outside the root.
	if err := os.Symlink(secret, filepath.Join(root, "escape.jsonl")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	_, err := dataset.Load(context.Background(), root, "escape.jsonl")
	var esc *dataset.PathEscapeError
	if !errors.As(err, &esc) {
		t.Fatalf("Load error = %v (%T), want *PathEscapeError", err, err)
	}
}

func TestVersionTokenRedacted(t *testing.T) {
	t.Parallel()

	// An oversized/hostile version token must be withheld from the diagnostic.
	huge := strings.Repeat("Z", 5000)
	line := encodeRawEnvelope(t, huge, sampleScenario("case-1"))
	_, err := dataset.DecodeRecord(line)
	var uv *dataset.UnknownVersionError
	if !errors.As(err, &uv) {
		t.Fatalf("DecodeRecord error = %v (%T), want *UnknownVersionError", err, err)
	}
	if strings.Contains(err.Error(), "Z") {
		t.Fatalf("error text leaked version token: %q", err.Error())
	}
	if uv.Version != "" {
		t.Fatalf("oversized version token not redacted: %q", uv.Version)
	}
}
