package reportjson_test

import (
	"bytes"
	"testing"

	"github.com/looprig/eval/reportjson"
)

// FuzzDecode exercises the report decode boundary with arbitrary bytes. It must
// never panic and must return either a decodable report or a typed error. When a
// report decodes, re-encoding and re-decoding must be a byte-stable fixed point.
func FuzzDecode(f *testing.F) {
	valid, err := reportjson.Encode(baseReport())
	if err != nil {
		f.Fatalf("seed Encode: %v", err)
	}
	seeds := [][]byte{
		valid,
		[]byte(`{"version":"report/v99","report":{}}`),
		[]byte(`{"version":"report/v1","report":`),
		append([]byte(`{"version":"report/v1","report":{"id":"`), []byte{0xff, 0xfe}...),
		bytes.Repeat([]byte("a"), reportjson.MaxReportBytes+1),
		[]byte(``),
		[]byte(`not json`),
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		r, err := reportjson.Decode(data)
		if err != nil {
			return // a typed rejection is fine; only panics fail the fuzz
		}
		out, err := reportjson.Encode(r)
		if err != nil {
			t.Fatalf("re-Encode of accepted report failed: %v", err)
		}
		r2, err := reportjson.Decode(out)
		if err != nil {
			t.Fatalf("re-Decode of re-encoded bytes failed: %v", err)
		}
		out2, err := reportjson.Encode(r2)
		if err != nil {
			t.Fatalf("second re-Encode failed: %v", err)
		}
		if !bytes.Equal(out, out2) {
			t.Fatalf("codec not stable:\n first=%s\n  second=%s", out, out2)
		}
	})
}
