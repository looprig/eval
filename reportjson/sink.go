package reportjson

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"strings"

	"github.com/looprig/eval"
)

// This file implements eval.Sink over the redacted report/v1 codec: a file sink
// that writes each report to its own JSON file under an explicitly provided
// directory. Writes are atomic (a temp file in the same directory is written,
// fsync'd, then renamed over the final name) and directory-scoped via os.Root so
// a report ID that resolves outside the root — through a "../" traversal or a
// symlink — is refused rather than followed.

// maxReportIDBytes bounds a report ID used as a filename component. Report IDs
// are short derived identifiers; a longer value is rejected.
const maxReportIDBytes = 200

// reportExt is the extension every report file carries.
const reportExt = ".json"

// tempPrefix marks the in-progress temp file so a crash mid-write leaves an
// obvious, skippable artifact rather than a half-written report.
const tempPrefix = ".reportjson-"

// FileSink writes each report as a redacted report/v1 JSON file under a fixed
// directory. The zero value is not usable; construct one with NewFileSink. It is
// safe for concurrent use: each WriteReport opens its own os.Root and uses a
// randomly named temp file, so concurrent writes of distinct reports do not
// collide.
type FileSink struct {
	dir string
}

// NewFileSink returns a FileSink that writes reports under dir. The directory
// must already exist; it is opened as an os.Root on each write so a report ID
// cannot escape it. dir is treated as a safe, caller-supplied locator.
func NewFileSink(dir string) *FileSink {
	return &FileSink{dir: dir}
}

// WriteReport encodes r to its canonical redacted wire form and writes it
// atomically to <dir>/<id>.json. It fails closed: a report ID that is empty,
// contains a path separator, is a traversal element, or resolves outside the
// sink root is refused with a typed error and nothing is written. The context is
// honoured before the write begins.
func (s *FileSink) WriteReport(ctx context.Context, r eval.Report) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	name, err := reportFileName(r.ID)
	if err != nil {
		return err
	}

	data, err := Encode(r)
	if err != nil {
		return err
	}

	root, err := os.OpenRoot(s.dir)
	if err != nil {
		return &DirectoryError{Dir: s.dir, Cause: err}
	}
	defer root.Close()

	tmp, err := tempName()
	if err != nil {
		return &WriteError{Cause: err}
	}

	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return classifyOpenErr(s.dir, err)
	}
	// From here a failure must not leave the temp file behind.
	if err := writeSync(f, data); err != nil {
		_ = f.Close()
		_ = root.Remove(tmp)
		return &WriteError{Cause: err}
	}
	if err := f.Close(); err != nil {
		_ = root.Remove(tmp)
		return &WriteError{Cause: err}
	}
	if err := root.Rename(tmp, name); err != nil {
		_ = root.Remove(tmp)
		return classifyOpenErr(s.dir, err)
	}
	return nil
}

// writeSync writes all of data to f and fsyncs it, so the bytes are durable
// before the rename makes them visible under the final name.
func writeSync(f *os.File, data []byte) error {
	if _, err := f.Write(data); err != nil {
		return err
	}
	return f.Sync()
}

// reportFileName derives the safe, single-component filename for a report ID, or
// a typed error when the ID cannot be one. It rejects an empty ID, any path
// separator, and the "." / ".." traversal elements before the name ever reaches
// os.Root, so a rejection is classified precisely rather than as a generic open
// failure.
func reportFileName(id string) (string, error) {
	switch {
	case id == "":
		return "", &InvalidReportIDError{Reason: idReasonEmpty}
	case len(id) > maxReportIDBytes:
		return "", &InvalidReportIDError{Reason: idReasonTooLong}
	case strings.ContainsRune(id, '/') || strings.ContainsRune(id, os.PathSeparator) || strings.ContainsRune(id, '\\'):
		return "", &InvalidReportIDError{Reason: idReasonSeparator}
	case id == "." || id == "..":
		return "", &InvalidReportIDError{Reason: idReasonTraversal}
	}
	return id + reportExt, nil
}

// tempName returns a random, hidden temp filename in the sink directory. It uses
// crypto/rand so concurrent writers never collide on a name.
func tempName() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return tempPrefix + hex.EncodeToString(b[:]) + ".tmp", nil
}

// classifyOpenErr distinguishes a genuine open/rename failure (the parent is
// missing or unwritable) from an os.Root path escape. os.Root refuses an escaping
// path with an error that is not fs.ErrNotExist/fs.ErrPermission; a genuinely
// absent or unpermitted target carries those sentinels. Fail closed either way.
func classifyOpenErr(dir string, err error) error {
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission) || errors.Is(err, fs.ErrExist) {
		return &WriteError{Cause: err}
	}
	return &PathEscapeError{Dir: dir, Cause: err}
}
