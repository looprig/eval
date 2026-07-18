//go:build integration

package inference

// This live smoke test is excluded from the default `go test` suite: it is
// guarded by the `integration` build tag and runs only under
// `go test -tags=integration`. It never runs, skips, or fails in the default
// unit run.
//
// Client construction is deliberately out of scope for this module. A working
// inference.Client is a connection-bound composition of a dialect-specific
// codec.Codec, a route.Router, an auth.Authenticator, and an endpoint (see
// github.com/looprig/inference/transport.New). The concrete codecs live in the
// llm module, which eval does not depend on, so a real client cannot be wired up
// from eval's dependency set alone. Rather than pull an unapproved dependency
// into eval, this test documents the seam and requires the caller to supply a
// client through a build-time hook.
//
// To exercise it, wire integrationClient below (in a separate, tagged file in
// your own tree, or by editing this stub) to build a real client from your
// llm-module composition root, keyed off the credential env var, then run:
//
//	INFERENCE_INTEGRATION_API_KEY=... go test -tags=integration -run TestObserveLive ./target/inference
//
// The credential is read from the environment and never hardcoded.

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/looprig/core/content"
	"github.com/looprig/eval"
	llm "github.com/looprig/inference"
	"github.com/looprig/inference/model"
)

// integrationCredentialEnv names the environment variable that must carry the
// API credential for the live smoke test. When it is absent the test skips.
const integrationCredentialEnv = "INFERENCE_INTEGRATION_API_KEY"

// integrationClient builds a real inference.Client for the live smoke test from
// the supplied API key and model. It is intentionally a TODO seam: constructing
// a functioning client requires a dialect codec + router from the llm module,
// which eval does not (and must not) depend on. Fill this in from your own
// composition root to run the test.
func integrationClient(t *testing.T, apiKey string, m model.Model) llm.Client {
	t.Helper()
	t.Skipf("integration client construction not wired: supply a real inference.Client "+
		"from an llm-module composition root (see file header). credential present via %s",
		integrationCredentialEnv)
	return nil
}

// TestObserveLive performs one real Observe against a live model. It skips
// cleanly when the credential is absent so it never fails a machine without it.
func TestObserveLive(t *testing.T) {
	apiKey := os.Getenv(integrationCredentialEnv)
	if apiKey == "" {
		t.Skipf("%s not set; skipping live inference smoke test", integrationCredentialEnv)
	}

	// A live-capable model descriptor is the caller's to provide; this is a
	// placeholder that the wired integrationClient is expected to honor.
	m := model.CustomModel(model.ProviderName("openai"), model.APIFormat("openai"), "", "gpt-4o-mini")
	client := integrationClient(t, apiKey, m)

	tgt := NewTarget(client, llm.Request{Model: m})
	sc := eval.Scenario{
		ID:       "live-smoke",
		Name:     "live",
		Revision: eval.Revision(m.Name),
		Input: content.AgenticMessages{&content.UserMessage{Message: content.Message{
			Role:   content.RoleUser,
			Blocks: []content.Block{&content.TextBlock{Text: "Reply with the single word: ok"}},
		}}},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	obs, err := tgt.Observe(ctx, sc)
	if err != nil {
		t.Fatalf("live Observe: %v", err)
	}
	if err := obs.Validate(); err != nil {
		t.Fatalf("live observation invalid: %v", err)
	}
	if len(obs.Conversation) < 2 {
		t.Fatalf("live conversation length = %d, want >= 2", len(obs.Conversation))
	}
}
