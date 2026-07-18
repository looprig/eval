package dataset_test

import (
	"bytes"
	"testing"

	"github.com/looprig/eval/dataset"
)

// FuzzDecode exercises the single-record decode boundary with arbitrary bytes.
// It must never panic and must return either a valid scenario (which then also
// validates) or a typed error. When a record decodes, re-encoding and
// re-decoding must be a stable fixed point.
func FuzzDecode(f *testing.F) {
	valid, err := dataset.EncodeRecord(sampleScenario("case-1"))
	if err != nil {
		f.Fatalf("seed EncodeRecord: %v", err)
	}
	seeds := [][]byte{
		valid,
		[]byte(`{"version":"dataset/v99","scenario":{"id":"x"}}`),
		[]byte(`{"version":"dataset/v1","scenario":`),
		append([]byte(`{"version":"dataset/v1","scenario":{"id":"`), []byte{0xff, 0xfe}...),
		bytes.Repeat([]byte("a"), dataset.MaxRecordBytes+1),
		[]byte(``),
		[]byte(`not json`),
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		sc, err := dataset.DecodeRecord(data)
		if err != nil {
			return // a typed rejection is fine; only panics fail the fuzz
		}
		// An accepted record must be a valid scenario.
		if verr := sc.Validate(); verr != nil {
			t.Fatalf("DecodeRecord accepted an invalid scenario: %v", verr)
		}
		// The codec must be a stable fixed point on accepted input.
		out, err := dataset.EncodeRecord(sc)
		if err != nil {
			t.Fatalf("re-EncodeRecord of accepted scenario failed: %v", err)
		}
		sc2, err := dataset.DecodeRecord(out)
		if err != nil {
			t.Fatalf("re-DecodeRecord of re-encoded bytes failed: %v", err)
		}
		out2, err := dataset.EncodeRecord(sc2)
		if err != nil {
			t.Fatalf("second re-EncodeRecord failed: %v", err)
		}
		if !bytes.Equal(out, out2) {
			t.Fatalf("codec not stable:\n first = %s\n  second = %s", out, out2)
		}
	})
}
