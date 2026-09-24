# eval

`github.com/looprig/eval` is an application-neutral evaluation framework for
agentic systems. It runs as ordinary Go code under `go test`, reusing the
standard `testing` package for execution, comparison, failure reporting,
parallelism, and CI, and adds the domain vocabulary `testing` does not provide:
conversations, expectations, operational evidence, evaluators, rubrics,
findings, measurements, reports, and sinks.

An agent interaction is a `content.AgenticMessages` thread from
`github.com/looprig/core` — text, multimodal blocks, tool requests, tool
results, errors, and token usage — so input, output, steps, and tool calls are
never duplicated into eval-specific string fields.

The root package depends only on `core`. `judge/` and `target/inference/` add
the `github.com/looprig/inference` dependency; nothing else does (no Harness,
sandbox, OpenTelemetry, or provider SDK reaches a consumer's build graph through
eval).

## Status

Released. The root package (suites, scenarios, targets, evaluators,
assessments, the `eval.Run` engine and `eval.Report`), deterministic
evaluators (`exact`), the structured model judge (`judge`) with versioned
rubrics (`rubric`), the active-inference target (`target/inference`), the
JSONL scenario codec (`dataset`), the redacted report codec and file sink
(`reportjson`), baseline comparison (`compare`) and the `go test` integration
(`evaltest`) are implemented. No Harness adapter ships in this module.

Install:

```sh
go get github.com/looprig/eval@latest
```

| Package | Purpose |
|---|---|
| `github.com/looprig/eval` | Core vocabulary and engine: `Suite`, `Scenario`, `Target`, `Evaluator`, `Assessment`, `Report`, `Sink`, `Run`. |
| `evaltest` | Runs a suite under `go test` as subtests and gates the report. |
| `exact` | Deterministic evaluators over text, tool calls, structured output, tool-error rate and latency. |
| `judge` | Structured-output model judge over an injected `inference.Client`. |
| `rubric` | Versioned rubric definitions and `rubric.Catalog()`. |
| `target/inference` | `eval.Target` that drives a scenario through an `inference.Client`. |
| `dataset` | Versioned, bounded JSONL codec for scenarios. |
| `reportjson` | Versioned, redacted `report/v1` JSON codec and `FileSink`. |
| `compare` | Per-case baseline-versus-candidate comparison. |
| `examples/*` | Runnable `Example` tests for `exact`, `judge` and `report`. |

## What eval does not do (non-goals)

These are contracts, not omissions:

- **Evals only observe, score, and report.** An evaluator reports evidence and
  assessments; it never authorizes, blocks, rewrites, retries, or otherwise
  acts on a live session. Deciding what to do with a finding is a separate
  component's job.
- **Missing evidence is `unverified`, never a pass.** Absent context, an
  unavailable judge, an unsupported sandbox guarantee, or a model that cannot
  satisfy the declared schema yields `unverified` or `error` — never an inferred
  passing score.
- **Eval output never touches the Harness journal.** Eval does not depend on
  Harness; evaluating a live session means snapshotting its public conversation
  at a turn/session boundary and evaluating it *outside* the active loop, never
  writing the journal or changing the active conversation.
- Eval does not own alert thresholds or delivery, and does not select an
  OpenTelemetry SDK, exporter, or backend. Reports are data; a downstream system
  decides whether a measurement warrants an alert.

## Deterministic qualification

Write a suite of scenarios, run it through a target with one or more `exact.*`
programmatic evaluators, and gate the report — all inside a normal Go test.
`exact` checks observable facts: required/forbidden output text, required or
forbidden tool calls, structured-output conformance, tool-error rate, and
latency.

```go
func TestSupportAgentQualification(t *testing.T) {
	t.Parallel()

	suite := eval.Suite{
		Name:     "support-agent",
		Revision: "2026-07-18",
		Scenarios: []eval.Scenario{{
			ID:       "lookup-001",
			Name:     "looks up the account before answering",
			Revision: "1",
			Input:    content.AgenticMessages{ /* user turn(s) */ },
		}},
	}

	report := evaltest.Run(t, suite, target,
		exact.RequiredTool("lookup_account"),
		exact.ForbiddenText("as an AI language model"),
	)

	evaltest.RequirePass(t, report)
}
```

`evaltest.Run` executes the suite through `eval.Run`, presents every scenario
and evaluator as a Go subtest, and returns the complete `eval.Report` for
custom assertions. Presentation is informational — gate the report explicitly
with `evaltest.RequirePass` (fails on any non-pass) or `evaltest.RequireVerified`
(fails on `error`/`unverified`, tolerates a recorded `fail`).

For a single case, `evaltest.RunScenario(t, scenario, target, evaluators...)`
wraps one `eval.Scenario` in a one-scenario suite:

```go
func TestInvoiceAgentDoesNotInventRefund(t *testing.T) {
	t.Parallel()

	scenario := eval.Scenario{
		ID:       "refund-policy-017",
		Name:     "refuses a refund on a non-refundable invoice",
		Revision: "1",
		Input:    content.AgenticMessages{ /* "Refund this non-refundable invoice" */ },
	}

	report := evaltest.RunScenario(t, scenario, agentTarget,
		exact.NoToolCall("issue_refund"),
	)
	evaltest.RequirePass(t, report)
}
```

The engine runs stages independently: a target error is not reported as a failed
quality score, and one evaluator error does not discard a sibling's assessment.
`eval.RunConfig{}` is the deterministic default (one trial, sequential); set
`Trials`, `Concurrency`, and per-stage timeouts to run a matrix or repeated
trials.

## The structured judge

For genuinely ambiguous quality — relevance, groundedness, instruction
adherence, goal adherence, toxicity — use `judge.New`. It builds an
`inference.Request` with strict structured output, calls an injected
`inference.Client`, and re-validates the decoded score locally. A model that
cannot satisfy the schema produces a typed `error`/`unverified`, never a guessed
verdict.

```go
// template carries the (structured-output-capable) judge Model and any System
// or sampling defaults; the judge fills Messages (the untrusted conversation)
// and Output (the strict score schema) on each call.
template := inference.Request{Model: model.Model{ /* judge model */ }}

report := evaltest.Run(t, suite, target,
	exact.RequiredTool("lookup_account"),                         // deterministic evidence
	judge.New(rubric.AnswerRelevanceV1, judgeClient, template),   // model judgment
)
evaltest.RequirePass(t, report)
```

Rubrics (`rubric.*`) define *what good means* and are versioned
(`AnswerRelevanceV1`, `GroundednessV1`, `InstructionAdherenceV1`,
`GoalAdherenceV1`, `ToxicityV1`, `VulgarityV1`,
`InternetUseAppropriatenessV1`; `rubric.Catalog()` lists them). The judge schema
defines the *shape of the answer* and is separate. Combining an `exact.*` check
with a `judge.*` score in one suite is the composite pattern: a judge score can
never conceal a deterministic failure, because both assessments are retained in
the report.

## Targets

A `target` turns a `Scenario` into an `Observation`. `target/inference`'s
`NewTarget(client, template, opts...)` drives a scenario's input through an
`inference.Client` and projects the reply into an observation, deriving safe
subject/model provenance from the template model. Continuous evaluation already
has an observation and constructs a `Sample` without running a target.

## Go test build tags and the test cache

Expensive or networked cases use ordinary Go build tags so they are excluded
from the default `go test ./...`:

- Tag live/model-backed cases `//go:build integration` (or `qualification`) and
  run them explicitly: `go test -tags integration -race ./...`. Integration
  tests live in `*_integration_test.go` (this module's own is
  `target/inference/target_integration_test.go`).
- Unit tests never touch the network. Datasets can be embedded with `//go:embed`.

Because a live judge or target is nondeterministic, do **not** let Go's test
cache serve a stale pass. Run live/nondeterministic tests with `-count=1`:

```sh
go test -tags integration -race -count=1 ./...
```

Deterministic golden tests remain cacheable and need no `-count=1`. Fuzz targets
exercise the parsers and codecs (`go test -fuzz=FuzzXxx -fuzztime=30s ./path`),
never a paid judge.

## Reports, sinks, and baselines

`eval.Run` returns a typed `eval.Report` — per-sample results, assessments,
measurements, findings, and provenance. A `Sink` is the destination contract:

```go
type Sink interface {
	WriteReport(context.Context, Report) error
}
```

`reportjson` implements the redacted `report/v1` wire form:

- `reportjson.Encode(report) ([]byte, error)` / `reportjson.Decode(data) (eval.Report, error)`
  — the versioned codec; both directions enforce `Report.Validate`, and raw
  conversation text, judge explanations, and secrets are redacted on the wire.
- `reportjson.NewFileSink(dir)` — an `eval.Sink` that writes each report
  atomically to `<dir>/<id>.json`, directory-scoped via `os.Root` so a report ID
  cannot escape the root.

Compare a candidate report against a stored baseline with `compare`:

```go
baseline, _ := reportjson.Decode(baselineBytes)
cmp, err := compare.Compare(baseline, candidate)
```

`compare.Compare` classifies each case — `added`, `removed`, `changed`,
`unchanged`, `errored`, `unverified`, `failed`, `incompatible` — and reports
per-measurement deltas, rather than comparing averages alone. Comparison is only
valid when both reports declare compatible case and evaluator identities.

## Building and verifying

The Go baseline is 1.26.8. Every command runs with `GOWORK=off` so the module
resolves through its own pinned `require` graph (a parent `go.work` must not
capture it):

```sh
GOWORK=off go test -race ./...                 # unit tests, always -race
CGO_ENABLED=0 GOWORK=off go build -trimpath ./...
GOWORK=off make secure                         # fmt-check + vet + staticcheck + gosec + mod verify + govulncheck
make check                                     # the CI surface: fmt-check, vet, staticcheck, gosec, govulncheck, race tests, build
```

The security linters (`staticcheck`, `gosec`, `govulncheck`) are wired as Go
tool dependencies in `go.mod` and are dev/tool-only — they are not linked into
the library. The library's only runtime dependencies are `github.com/looprig/core`
(root) and `github.com/looprig/inference` (`judge/` and `target/inference/`
only).

## Where it sits

Tier 3 of the Looprig workspace: `eval -> core, inference`. `pluto` builds on
it.

## License

Apache License 2.0. See [LICENSE](LICENSE).
