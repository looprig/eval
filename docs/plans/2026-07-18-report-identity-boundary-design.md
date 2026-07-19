# Report Identity Boundary Design

**Date:** 2026-07-18

## Goal

Make `Report.Validate` enforce the complete runner-shaped identity contract so
`reportjson.Decode` and `compare.Compare` cannot accept identities that `Run`
itself never emits.

## Design

Use the root package's existing `validateIdentifier` helper for
`SampleReport.ScenarioID`. This preserves the same non-empty, UTF-8, and
`MaxIDBytes` constraints already enforced by `Scenario.Validate`, instead of
maintaining a weaker report-only check.

Require `Report.Suite` and `Provenance.Suite` to be valid non-empty revisions.
The Suite revision is always known for a runner-produced report. Keep Target
conditionally optional because a cancelled run with no completed sample, or a
run where every target invocation failed, has no observed target revision.

Tie Target presence to the samples:

- if any sample reached assessment (that is, `TargetErr == nil`), Report.Target
  must be a valid non-empty revision;
- if no sample reached assessment, Report.Target must be empty;
- the existing provenance equality check continues to require
  `Provenance.Target == Report.Target`.

Complete the remaining runner-shaped identities as well:

- `Report.ID` must be valid UTF-8 in addition to its existing non-empty and
  byte-length requirements, preventing JSON replacement from changing or
  collapsing malformed identities;
- every successful sample must carry exactly the evaluator identity set in
  provenance, because `Run` records one assessment per configured evaluator
  even when the evaluator errors or cannot verify the sample;
- provenance-only evaluator identities remain valid when no sample succeeded,
  because target failure or cancellation can prevent every assessment.

`reportjson.Encode` must apply `Report.Validate` before either JSON marshal. Go's
JSON encoder replaces malformed UTF-8 bytes with U+FFFD; without an encode-side
gate, two malformed identities can collapse to the same wire value and a file
sink can name the file from different bytes than the body records. Projection
runs first only to preserve precise projection errors such as
`NonFiniteValueError`; validation still precedes serialization.

All new failures remain typed and content-free. Identifier/revision shape
failures use the existing `ValidationError` vocabulary; cross-field Target
presence contradictions use `ReportValidationError` with fixed reasons. Neither
error type echoes a scenario identity or other untrusted value.

## Alternatives Considered

1. **Validate only in `reportjson.Decode`.** Rejected because hand-built reports
   passed directly to `Compare` would still bypass the invariant.
2. **Permit empty Suite/Target as generic partial reports.** Rejected because
   `Report` is documented as a `Run` result and downstream provenance depends on
   distinguishing an unobserved target from missing metadata.
3. **Recommended: enforce once in `Report.Validate`.** Decode and Compare already
   call this boundary, so one implementation closes every consumer consistently.

## Testing

Add table-driven parallel tests proving root validation rejects overlong and
invalid-UTF-8 scenario IDs, an empty Suite, a successful sample with an empty
Target, and an all-target-failed report with a non-empty Target. Extend decoder
tests for the serialized forms and Compare tests for both baseline and candidate
wrapping. Add regressions for malformed Report IDs and a report-wide evaluator
union that masks an evaluator missing from one successful sample. Confirm genuine
runner outputs, including all-target-failed and cancelled/empty-sample reports,
still validate. Require Encode and FileSink to reject invalid reports without
writing, and construct decoder-invalid fixtures by mutating an already-valid wire
envelope so decoder coverage does not rely on an unsafe encoder.
