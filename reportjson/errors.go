package reportjson

import (
	"strconv"
	"unicode/utf8"
)

// This file declares the concrete, classifiable error types the report codec and
// file sink return. Every distinct failure mode is a typed struct so callers
// classify with errors.As, never by matching strings. Diagnostics carry only
// safe locators — a fixed reason vocabulary, safe integers, and caller-supplied
// (safe) directory names — and never the offending report bytes, conversation
// text, judge explanations, target-error cause text, or an attacker-supplied
// token. Types that wrap a cause expose it via Unwrap so a caller can inspect
// it, but the Error() text never renders untrusted content.

// maxVersionTokenBytes bounds how much of an unknown version token may appear in
// a diagnostic. A token longer than this is withheld entirely.
const maxVersionTokenBytes = 64

// Fixed vocabulary for MalformedReportError.Reason. Drawing every reason from
// this closed set guarantees a diagnostic never embeds untrusted content.
const (
	reasonInvalidJSON  = "invalid JSON"
	reasonInvalidUTF8  = "invalid UTF-8"
	reasonTrailingData = "trailing data after report"
	reasonEmptyReport  = "missing report payload"
	reasonUnknownField = "unknown field"
	reasonNonFinite    = "non-finite measurement value"
)

// UnknownVersionError reports that a report's version discriminator was missing
// or names a wire version this codec does not implement. The version token may
// originate from untrusted input, so it is bounded and withheld when hostile;
// Version is either a short, valid token or "" when redacted.
type UnknownVersionError struct {
	// Version is a bounded, safe rendering of the offending token, or "" when it
	// was missing or withheld.
	Version string
}

func (e *UnknownVersionError) Error() string {
	if e.Version == "" {
		return "eval/reportjson: unknown or missing report version"
	}
	return "eval/reportjson: unknown report version " + strconv.Quote(e.Version)
}

// ReportTooLargeError reports that an encoded report exceeded MaxReportBytes.
// Only safe integers are carried; no report content is embedded.
type ReportTooLargeError struct {
	Size int
	Max  int
}

func (e *ReportTooLargeError) Error() string {
	return "eval/reportjson: report of " + strconv.Itoa(e.Size) +
		" bytes exceeds max " + strconv.Itoa(e.Max)
}

// MalformedReportError reports that the bytes were not exactly one well-formed
// report/v1 document. Reason is drawn only from the fixed vocabulary above, so no
// untrusted content leaks.
type MalformedReportError struct {
	Reason string
}

func (e *MalformedReportError) Error() string {
	return "eval/reportjson: malformed report: " + e.Reason
}

// NonFiniteValueError reports that a measurement carried a NaN or ±Inf value,
// which JSON cannot represent and which the codec rejects fail-closed. No value
// is embedded (it is not finite and not safe to render as a number).
type NonFiniteValueError struct{}

func (e *NonFiniteValueError) Error() string {
	return "eval/reportjson: measurement value must be finite"
}

// InvalidReportError reports that a decoded report was well-formed JSON but a
// reconstructed part failed domain validation. Cause is the eval package's own
// typed validation error — itself free of untrusted content — and is exposed via
// Unwrap so callers can classify it.
type InvalidReportError struct {
	Cause error
}

func (e *InvalidReportError) Error() string {
	return "eval/reportjson: invalid report: " + e.Cause.Error()
}

func (e *InvalidReportError) Unwrap() error { return e.Cause }

// EncodeError reports that a report could not be serialized. Cause is exposed via
// Unwrap. It is an encode-side failure, not a decode-boundary rejection.
type EncodeError struct {
	Cause error
}

func (e *EncodeError) Error() string { return "eval/reportjson: cannot encode report" }

func (e *EncodeError) Unwrap() error { return e.Cause }

// InvalidReportIDError reports that a report's ID could not be used as a single,
// safe filename component (it was empty, or contained a path separator or a "."
// or ".." traversal element). Reason is drawn from a fixed vocabulary; the
// offending ID is caller-supplied and withheld.
type InvalidReportIDError struct {
	Reason string
}

func (e *InvalidReportIDError) Error() string {
	return "eval/reportjson: invalid report id for filename: " + e.Reason
}

const (
	idReasonEmpty     = "must not be empty"
	idReasonSeparator = "must not contain a path separator"
	idReasonTraversal = "must not be a traversal element"
	idReasonTooLong   = "exceeds the filename length bound"
)

// PathEscapeError reports that a report's derived file name resolved outside the
// caller-provided sink directory — for example via a symlink or a "../"
// traversal — and was refused by the os.Root-scoped writer. Dir is the
// caller-supplied (safe) directory; Cause is the underlying os error, exposed via
// Unwrap but never rendered.
type PathEscapeError struct {
	Dir   string
	Cause error
}

func (e *PathEscapeError) Error() string {
	return "eval/reportjson: report file escapes the sink root " + strconv.Quote(e.Dir)
}

func (e *PathEscapeError) Unwrap() error { return e.Cause }

// DirectoryError reports that the sink directory itself could not be opened as an
// os.Root. Dir is the caller-supplied (safe) directory; Cause is exposed via
// Unwrap.
type DirectoryError struct {
	Dir   string
	Cause error
}

func (e *DirectoryError) Error() string {
	return "eval/reportjson: cannot open sink directory " + strconv.Quote(e.Dir)
}

func (e *DirectoryError) Unwrap() error { return e.Cause }

// WriteError reports that writing, syncing, or renaming the report file failed.
// Cause is exposed via Unwrap.
type WriteError struct {
	Cause error
}

func (e *WriteError) Error() string { return "eval/reportjson: cannot write report file" }

func (e *WriteError) Unwrap() error { return e.Cause }

// safeVersionToken returns a bounded, safe rendering of an unknown version token
// for a diagnostic, or "" when the token is oversized or not valid UTF-8.
func safeVersionToken(v string) string {
	if v == "" || len(v) > maxVersionTokenBytes || !utf8.ValidString(v) {
		return ""
	}
	return v
}
