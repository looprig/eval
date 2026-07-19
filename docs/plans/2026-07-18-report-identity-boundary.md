# Report Identity Boundary Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Enforce complete scenario, suite, and observed-target identity invariants at `Report.Validate`.

**Architecture:** Strengthen the single root report boundary using existing identifier and revision validators. `reportjson.Decode` and `compare.Compare` inherit the behavior because they already call `Report.Validate`; targeted tests pin each layer.

**Tech Stack:** Go 1.26, standard library, existing typed eval validation errors.

---

### Task 1: Pin root report identity failures

**Files:**
- Modify: `report_test.go`

**Step 1: Write failing table cases**

Add cases to `TestReportValidate` for:

- `ScenarioID` longer than `MaxIDBytes`;
- invalid UTF-8 `ScenarioID`;
- empty `Report.Suite` and matching empty `Provenance.Suite`;
- a successful sample with empty Report/Provenance Target;
- an all-target-error report carrying a non-empty Report/Provenance Target.

Each case must expect a typed validation failure without inspecting untrusted
input in the diagnostic.

**Step 2: Verify RED**

Run: `GOWORK=off go test -race ./ -run '^TestReportValidate$' -count=1`

Expected: FAIL because the current report boundary accepts all five cases.

### Task 2: Implement the root boundary

**Files:**
- Modify: `errors.go`
- Modify: `report.go`

**Step 1: Add fixed report reasons**

Add reasons for missing observed Target and Target present without a successful
sample. Scenario and Suite shape failures reuse `ValidationError` from the
existing identifier/revision validators.

**Step 2: Apply existing validators**

- Replace the empty-only ScenarioID check with
  `validateIdentifier("SampleReport.ScenarioID", s.ScenarioID, MaxIDBytes)`.
- Validate `Report.Suite` with `Revision.Validate`.
- Validate Provenance Suite with `Revision.Validate`.
- Add a helper that detects whether any sample has `TargetErr == nil` and checks
  Target presence accordingly before provenance consistency.

**Step 3: Verify GREEN**

Run: `GOWORK=off go test -race ./ -run '^TestReportValidate$' -count=1`

Expected: PASS.

### Task 3: Pin decode and Compare boundaries

**Files:**
- Modify: `reportjson/codec_test.go`
- Modify: `compare/compare_test.go`

**Step 1: Add decoder regression cases**

Extend `TestDecodeRejectsInvalidReport` with overlong ScenarioID, empty Suite,
successful sample with empty Target, and all-target-error with non-empty Target.

**Step 2: Add Compare wrapper cases**

Extend `TestCompareRejectsInvalidInput` with invalid baseline and candidate
reports exercising the strengthened identity boundary. Assert
`*compare.InvalidReportError`, the correct side, and an unwrap to the typed root
validation error.

**Step 3: Verify targeted packages**

Run: `GOWORK=off go test -race ./reportjson ./compare -count=1`

Expected: PASS.

### Task 4: Full release verification

**Files:**
- Inspect only

**Step 1: Run tests and build**

Run: `GOWORK=off go test -race -count=1 ./...`

Run: `GOWORK=off go build -trimpath ./...`

**Step 2: Run security gates**

Run: `GOWORK=off make lint`

Run: `GOWORK=off make vuln`

**Step 3: Verify dependencies and fuzzing**

Confirm inference occurs only in `judge` and `target/inference`, no forbidden
compiled dependencies appear, and run the report decoder fuzzer for at least one
million executions.

**Step 4: Inspect scope**

Run: `git diff --check && git status --short && git diff --stat d2e8df4..HEAD`

Expected: only the planned validation, tests, and planning documents changed.
