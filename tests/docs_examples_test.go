package tests

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type docsManifest struct {
	SchemaVersion int `json:"schemaVersion"`
	Repository    string
	ProofSources  []struct {
		ID     string
		Type   string
		Path   string
		Symbol string
	}
	Examples []struct {
		ID             string
		Ecosystem      string
		Owner          string
		SourcePath     string
		Availability   string
		Versions       map[string]string
		OfflineCommand string
		Assertion      string
		WorkflowPath   string
		JobID          string `json:"jobId"`
		Cleanup        string
		LiveGate       any
		ProofIDs       []string `json:"proofIds"`
	}
}

func TestDocsExamplesArtifacts(t *testing.T) {
	t.Parallel()

	wantSources := map[string]struct {
		typeName string
		path     string
		symbol   string
	}{
		"example-eval-exact-gate-fixture":     {"executable-fixture", "examples/exact/example_test.go", "Example_runExactGate"},
		"example-eval-judge-gate-fixture":     {"executable-fixture", "examples/judge/example_test.go", "Example_runScriptedJudge"},
		"example-eval-report-sink-fixture":    {"executable-fixture", "examples/report/example_test.go", "Example_writeReport"},
		"example-eval-manifest-contract-test": {"test", "tests/docs_examples_test.go", "TestDocsExamplesArtifacts"},
		"example-eval-run-source":             {"source", "run.go", "Run"},
		"example-eval-required-text-source":   {"source", "exact/text.go", "RequiredText"},
		"example-eval-forbidden-text-source":  {"source", "exact/text.go", "ForbiddenText"},
		"example-eval-judge-new-source":       {"source", "judge/judge.go", "New"},
		"example-eval-file-sink-source":       {"source", "reportjson/sink.go", "NewFileSink"},
		"example-eval-report-decode-source":   {"source", "reportjson/codec.go", "Decode"},
	}

	for _, source := range wantSources {
		if _, err := os.Stat(filepath.Join("..", source.path)); err != nil {
			t.Fatalf("required docs source %q: %v", source.path, err)
		}
	}

	raw, err := os.ReadFile(filepath.Join("..", "testdata", "docs", "examples.json"))
	if err != nil {
		t.Fatalf("read docs examples manifest: %v", err)
	}
	var manifest docsManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("decode docs examples manifest: %v", err)
	}
	if manifest.SchemaVersion != 1 || manifest.Repository != "eval" {
		t.Fatalf("manifest identity = (%d, %q), want (1, eval)", manifest.SchemaVersion, manifest.Repository)
	}
	if len(manifest.ProofSources) != len(wantSources) {
		t.Fatalf("proof source count = %d, want %d", len(manifest.ProofSources), len(wantSources))
	}
	known := make(map[string]bool, len(manifest.ProofSources))
	for _, source := range manifest.ProofSources {
		want, ok := wantSources[source.ID]
		if !ok {
			t.Fatalf("unexpected proof source %q", source.ID)
		}
		if source.Type != want.typeName || source.Path != want.path || source.Symbol != want.symbol {
			t.Fatalf("proof source %q = (%q, %q, %q), want (%q, %q, %q)", source.ID, source.Type, source.Path, source.Symbol, want.typeName, want.path, want.symbol)
		}
		known[source.ID] = true
	}
	if len(manifest.Examples) != 3 {
		t.Fatalf("example count = %d, want 3", len(manifest.Examples))
	}
	for _, example := range manifest.Examples {
		if example.Ecosystem != "go" || example.Owner != "eval" || example.Availability != "source-workspace" {
			t.Fatalf("example %q has invalid ownership metadata", example.ID)
		}
		if example.Versions["github.com/looprig/eval"] != "source-workspace" {
			t.Fatalf("example %q does not pin eval to source-workspace", example.ID)
		}
		if example.OfflineCommand != "GOWORK=off GOCACHE=/tmp/looprig-eval-docs-gocache go test ./examples/..." {
			t.Fatalf("example %q has unexpected offline command %q", example.ID, example.OfflineCommand)
		}
		if example.WorkflowPath != ".github/workflows/docs-examples.yml" || example.JobID != "docs-examples" {
			t.Fatalf("example %q has invalid workflow binding", example.ID)
		}
		if example.Assertion == "" || example.Cleanup == "" || example.LiveGate != nil {
			t.Fatalf("example %q must document assertion and cleanup with no live gate", example.ID)
		}
		if len(example.ProofIDs) < 2 {
			t.Fatalf("example %q proof ID count = %d, want at least 2", example.ID, len(example.ProofIDs))
		}
		for _, id := range example.ProofIDs {
			if !known[id] {
				t.Fatalf("example %q refers to unknown proof %q", example.ID, id)
			}
		}
	}

	workflow, err := os.ReadFile(filepath.Join("..", ".github", "workflows", "docs-examples.yml"))
	if err != nil {
		t.Fatalf("read docs examples workflow: %v", err)
	}
	text := string(workflow)
	for _, command := range []string{
		"GOWORK=off GOCACHE=/tmp/looprig-eval-docs-gocache go test ./examples/...",
		"GOWORK=off GOCACHE=/tmp/looprig-eval-docs-gocache make test",
	} {
		if !strings.Contains(text, "run: "+command) {
			t.Fatalf("workflow does not literally run %q", command)
		}
	}
	if strings.Contains(text, "run: GOWORK=off GOCACHE=/tmp/looprig-eval-docs-gocache go test -race ./...") {
		t.Fatal("workflow duplicates the race-enabled repository tests already owned by make test")
	}
}
