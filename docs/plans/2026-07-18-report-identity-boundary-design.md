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
wrapping. Confirm genuine runner outputs, including all-target-failed and
cancelled/empty-sample reports, still validate.
