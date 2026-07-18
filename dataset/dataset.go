// Package dataset is the versioned JSONL codec for eval scenarios. It is the
// eval framework's untrusted deserialization boundary: a dataset file is one
// dataset/v1 envelope per line, each carrying an explicit version discriminator
// and a scenario payload. The codec reads the version first and rejects unknown
// or missing versions before trusting the payload (fail-closed), reconstructs
// the conversation by discriminating each message's role, and validates every
// scenario against the strict eval domain types before returning it.
//
// All sizes are bounded (MaxRecordBytes per line, MaxFileBytes per file), records
// load and are returned in file order, duplicate scenario IDs are rejected, and
// every failure is a typed error carrying only safe locators (a caller-supplied
// file name and a 1-based line number) — never the offending record bytes,
// conversation text, or tool output. Directory-scoped loading uses os.Root so a
// symlink or "../" in a file name cannot escape the caller-provided root.
package dataset

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"

	"github.com/looprig/eval"
)

// Byte bounds for the untrusted decode boundary. They reject an oversized line
// or file before it can exhaust memory. Conservative starting values; tune to
// real dataset sizes later.
const (
	// MaxRecordBytes bounds a single JSONL record (one line).
	MaxRecordBytes = 1 << 20 // 1 MiB
	// MaxFileBytes bounds a whole dataset file.
	MaxFileBytes = 16 << 20 // 16 MiB
)

// newline separates records in the JSONL wire form.
const newline = '\n'

// Dataset is an ordered set of validated scenarios decoded from a dataset file.
// Order mirrors file order, so a dataset is reproducible.
type Dataset struct {
	Scenarios []eval.Scenario
}

// Load opens name under dir using an os.Root, so a symlink or "../" in name
// cannot escape dir, and decodes the file. dir and name are caller-supplied and
// treated as safe locators. A name that resolves outside the root is refused
// with a *PathEscapeError.
func Load(ctx context.Context, dir, name string) (*Dataset, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, &DirectoryError{Dir: dir, Cause: err}
	}
	defer root.Close()

	f, err := root.Open(name)
	if err != nil {
		// os.Root refuses an escaping path with an error that is not
		// fs.ErrNotExist/fs.ErrPermission; a genuinely absent or unreadable file
		// carries those sentinels. Classify accordingly, fail closed.
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission) {
			return nil, &OpenError{Path: name, Cause: err}
		}
		return nil, &PathEscapeError{Path: name, Cause: err}
	}
	defer f.Close()

	return Decode(ctx, f, name)
}

// Decode reads a JSONL dataset from r and returns its scenarios in file order.
// name is a safe locator used only in diagnostics. Decode enforces the file and
// per-record size bounds, requires each non-terminal line to be exactly one JSON
// value, and rejects duplicate scenario IDs.
func Decode(ctx context.Context, r io.Reader, name string) (*Dataset, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// Read at most MaxFileBytes+1 so an oversize file is detected without
	// reading (or buffering) an unbounded amount.
	data, err := io.ReadAll(io.LimitReader(r, MaxFileBytes+1))
	if err != nil {
		return nil, &ReadError{Path: name, Cause: err}
	}
	if len(data) > MaxFileBytes {
		return nil, &FileTooLargeError{Path: name, Max: MaxFileBytes}
	}

	ds := &Dataset{}
	seen := make(map[string]int) // scenario ID -> 1-based line first seen
	lineNo := 0
	start := 0
	for i := 0; i <= len(data); i++ {
		if i < len(data) && data[i] != newline {
			continue
		}
		// A final newline yields an empty terminal segment; that is the normal
		// line terminator, not a record.
		if i == len(data) && start == i {
			break
		}
		lineNo++
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		seg := trimCR(data[start:i])
		if isBlank(seg) {
			return nil, &MalformedRecordError{Line: lineNo, Reason: reasonBlankLine}
		}

		sc, derr := decodeRecordAt(seg, lineNo)
		if derr != nil {
			return nil, derr
		}
		if first, dup := seen[sc.ID]; dup {
			return nil, &DuplicateScenarioError{Line: lineNo, FirstLine: first}
		}
		seen[sc.ID] = lineNo
		ds.Scenarios = append(ds.Scenarios, sc)

		start = i + 1
	}
	return ds, nil
}

// Encode writes each scenario as one JSONL record terminated by a newline, in
// order. It rejects duplicate scenario IDs so an encoded dataset never decodes
// into a duplicate.
func Encode(w io.Writer, scenarios []eval.Scenario) error {
	seen := make(map[string]int, len(scenarios))
	for i, sc := range scenarios {
		if first, dup := seen[sc.ID]; dup {
			return &DuplicateScenarioError{Line: i + 1, FirstLine: first}
		}
		seen[sc.ID] = i + 1

		line, err := EncodeRecord(sc)
		if err != nil {
			return err
		}
		if _, err := w.Write(line); err != nil {
			return &WriteError{Cause: err}
		}
		if _, err := w.Write([]byte{newline}); err != nil {
			return &WriteError{Cause: err}
		}
	}
	return nil
}

// trimCR strips a single trailing carriage return so CRLF-terminated files
// decode identically to LF-terminated ones.
func trimCR(b []byte) []byte {
	if n := len(b); n > 0 && b[n-1] == '\r' {
		return b[:n-1]
	}
	return b
}

// isBlank reports whether a segment is empty or only ASCII whitespace.
func isBlank(b []byte) bool {
	for _, c := range b {
		switch c {
		case ' ', '\t', '\r', '\n', '\v', '\f':
		default:
			return false
		}
	}
	return true
}
