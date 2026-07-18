package dataset

import "strconv"

// This file declares the concrete, classifiable error types the dataset codec
// returns. Every distinct failure mode is a typed struct so callers classify
// with errors.As, never by matching strings. Diagnostics carry only safe
// locators — a caller-supplied file name and a 1-based line number — and never
// the offending record bytes, conversation text, tool output, or an
// attacker-supplied token. Types that wrap a cause expose it via Unwrap so a
// caller can inspect it, but the Error() text never renders untrusted content.

// maxVersionTokenBytes bounds how much of an unknown version token may appear in
// a diagnostic. A token longer than this is withheld entirely.
const maxVersionTokenBytes = 64

// Fixed vocabulary for MalformedRecordError.Reason. Drawing every reason from
// this closed set guarantees a diagnostic never embeds untrusted content.
const (
	reasonInvalidJSON    = "invalid JSON"
	reasonInvalidUTF8    = "invalid UTF-8"
	reasonTrailingData   = "trailing data after record"
	reasonBlankLine      = "blank line"
	reasonEmptyScenario  = "missing scenario payload"
	reasonUnknownRole    = "unknown message role"
	reasonInvalidMessage = "invalid conversation message"
)

// locPrefix renders the safe "line N: " diagnostic prefix, or "" when no line
// context is available (the standalone DecodeRecord path uses line 0).
func locPrefix(line int) string {
	if line <= 0 {
		return ""
	}
	return "line " + strconv.Itoa(line) + ": "
}

// UnknownVersionError reports that a record's version discriminator was missing
// or names a wire version this codec does not implement. The version token may
// originate from untrusted input, so it is bounded and withheld when hostile;
// Version is either a short, valid token or "" when redacted.
type UnknownVersionError struct {
	// Line is the 1-based record line, or 0 when decoded standalone.
	Line int
	// Version is a bounded, safe rendering of the offending token, or "" when
	// it was missing or withheld.
	Version string
}

func (e *UnknownVersionError) Error() string {
	if e.Version == "" {
		return "eval/dataset: " + locPrefix(e.Line) + "unknown or missing dataset version"
	}
	return "eval/dataset: " + locPrefix(e.Line) + "unknown dataset version " + strconv.Quote(e.Version)
}

// RecordTooLargeError reports that a single record exceeded MaxRecordBytes. Only
// safe integers are carried; no record content is embedded.
type RecordTooLargeError struct {
	Line int
	Size int
	Max  int
}

func (e *RecordTooLargeError) Error() string {
	return "eval/dataset: " + locPrefix(e.Line) + "record of " + strconv.Itoa(e.Size) +
		" bytes exceeds max " + strconv.Itoa(e.Max)
}

// FileTooLargeError reports that a dataset file exceeded MaxFileBytes. Path is
// the caller-supplied (safe) file name.
type FileTooLargeError struct {
	Path string
	Max  int
}

func (e *FileTooLargeError) Error() string {
	return "eval/dataset: file " + strconv.Quote(e.Path) + " exceeds max " + strconv.Itoa(e.Max) + " bytes"
}

// MalformedRecordError reports that a record was not exactly one well-formed
// JSON value, or that its conversation could not be reconstructed. Reason is
// drawn only from the fixed vocabulary above, so no untrusted content leaks.
type MalformedRecordError struct {
	Line   int
	Reason string
}

func (e *MalformedRecordError) Error() string {
	return "eval/dataset: " + locPrefix(e.Line) + "malformed record: " + e.Reason
}

// DuplicateScenarioError reports that two records carried the same scenario ID,
// which would make two cases indistinguishable in a report and in baseline
// comparison. The offending ID is caller-supplied and withheld; only the safe
// line numbers are reported.
type DuplicateScenarioError struct {
	// Line is the 1-based line of the duplicate.
	Line int
	// FirstLine is the 1-based line where the ID was first seen.
	FirstLine int
}

func (e *DuplicateScenarioError) Error() string {
	return "eval/dataset: " + locPrefix(e.Line) + "duplicate scenario id (first seen at line " +
		strconv.Itoa(e.FirstLine) + ")"
}

// InvalidScenarioError reports that a decoded record was well-formed JSON but the
// reconstructed scenario failed domain validation. Cause is the eval package's
// own typed validation error — itself free of untrusted content — and is exposed
// via Unwrap so callers can classify it (for example errors.As to
// *eval.ValidationError).
type InvalidScenarioError struct {
	Line  int
	Cause error
}

func (e *InvalidScenarioError) Error() string {
	return "eval/dataset: " + locPrefix(e.Line) + "invalid scenario: " + e.Cause.Error()
}

func (e *InvalidScenarioError) Unwrap() error { return e.Cause }

// PathEscapeError reports that a file name resolved outside the caller-provided
// root directory — for example via a symlink or a "../" traversal — and was
// refused by the os.Root-scoped loader. Path is the caller-supplied (safe) name;
// Cause is the underlying os error, exposed via Unwrap but never rendered.
type PathEscapeError struct {
	Path  string
	Cause error
}

func (e *PathEscapeError) Error() string {
	return "eval/dataset: path " + strconv.Quote(e.Path) + " escapes the dataset root"
}

func (e *PathEscapeError) Unwrap() error { return e.Cause }

// OpenError reports that a file under the root could not be opened for a reason
// other than a root escape (it does not exist, or permission was denied). Path
// is the caller-supplied (safe) name; Cause is exposed via Unwrap.
type OpenError struct {
	Path  string
	Cause error
}

func (e *OpenError) Error() string {
	return "eval/dataset: cannot open " + strconv.Quote(e.Path)
}

func (e *OpenError) Unwrap() error { return e.Cause }

// DirectoryError reports that the dataset root directory itself could not be
// opened. Dir is the caller-supplied (safe) directory; Cause is exposed via
// Unwrap.
type DirectoryError struct {
	Dir   string
	Cause error
}

func (e *DirectoryError) Error() string {
	return "eval/dataset: cannot open dataset root " + strconv.Quote(e.Dir)
}

func (e *DirectoryError) Unwrap() error { return e.Cause }

// ReadError reports that reading the dataset file failed partway through. Path
// is the caller-supplied (safe) name; Cause is exposed via Unwrap.
type ReadError struct {
	Path  string
	Cause error
}

func (e *ReadError) Error() string {
	return "eval/dataset: read failed for " + strconv.Quote(e.Path)
}

func (e *ReadError) Unwrap() error { return e.Cause }

// EncodeError reports that a scenario could not be serialized to a record. Cause
// is exposed via Unwrap. It is a programming/input error on the encode side, not
// a decode-boundary failure.
type EncodeError struct {
	Cause error
}

func (e *EncodeError) Error() string {
	return "eval/dataset: cannot encode record"
}

func (e *EncodeError) Unwrap() error { return e.Cause }

// WriteError reports that writing an encoded record to the output stream failed.
// Cause is exposed via Unwrap.
type WriteError struct {
	Cause error
}

func (e *WriteError) Error() string {
	return "eval/dataset: cannot write record"
}

func (e *WriteError) Unwrap() error { return e.Cause }
