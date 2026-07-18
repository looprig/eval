package eval

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

// newValidDescriptor builds a fully valid descriptor whose single required
// evidence kind (EvidenceUsage) is present in newValidObservation's trace.
func newValidDescriptor() Descriptor {
	return Descriptor{
		Name:        "answer-relevance",
		Revision:    "v1",
		Method:      MethodModel,
		Description: "scores whether the answer is relevant to the question",
		Requires:    []EvidenceKind{EvidenceUsage},
	}
}

func TestDescriptorValidateHappyPath(t *testing.T) {
	t.Parallel()
	if err := newValidDescriptor().Validate(); err != nil {
		t.Fatalf("valid descriptor rejected: %v", err)
	}
	// A descriptor with no requirements is valid: it needs no evidence.
	d := newValidDescriptor()
	d.Requires = nil
	if err := d.Validate(); err != nil {
		t.Fatalf("descriptor with no requirements rejected: %v", err)
	}
}

func TestDescriptorValidateIdentity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mutate  func(*Descriptor)
		wantErr bool
	}{
		{name: "valid", mutate: func(*Descriptor) {}, wantErr: false},
		{name: "empty name rejected", mutate: func(d *Descriptor) { d.Name = "" }, wantErr: true},
		{name: "empty revision rejected", mutate: func(d *Descriptor) { d.Revision = "" }, wantErr: true},
		{name: "unknown method rejected", mutate: func(d *Descriptor) { d.Method = Method(99) }, wantErr: true},
		{name: "empty description allowed", mutate: func(d *Descriptor) { d.Description = "" }, wantErr: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			d := newValidDescriptor()
			tt.mutate(&d)
			err := d.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil && isBareError(err) {
				t.Fatalf("Validate() returned bare error %v; want typed error", err)
			}
		})
	}
}

func TestDescriptorValidateDescriptionBound(t *testing.T) {
	t.Parallel()
	d := newValidDescriptor()
	d.Description = strings.Repeat("x", MaxDescriptionBytes+1)
	err := d.Validate()
	if err == nil {
		t.Fatal("Validate() = nil, want error for oversized description")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("Validate() error = %v, want *ValidationError", err)
	}
}

func TestDescriptorValidateInvalidRequires(t *testing.T) {
	t.Parallel()
	d := newValidDescriptor()
	d.Requires = []EvidenceKind{EvidenceKind("wombat")}
	err := d.Validate()
	if err == nil {
		t.Fatal("Validate() = nil, want error for unknown required evidence kind")
	}
	var ee *InvalidEnumError
	if !errors.As(err, &ee) {
		t.Fatalf("Validate() error = %v, want *InvalidEnumError", err)
	}
	// The offending token must not leak into the diagnostic.
	assertNoUntrustedEcho(t, err, "wombat")
}

func TestDescriptorValidateDuplicateRequires(t *testing.T) {
	t.Parallel()
	d := newValidDescriptor()
	d.Requires = []EvidenceKind{EvidenceUsage, EvidenceUsage}
	err := d.Validate()
	if err == nil {
		t.Fatal("Validate() = nil, want error for duplicate required evidence kind")
	}
	var dk *DuplicateEvidenceKindError
	if !errors.As(err, &dk) {
		t.Fatalf("Validate() error = %v, want *DuplicateEvidenceKindError", err)
	}
}

// TestDescriptorCheckRequires verifies the missing-required-evidence seam:
// principle #4 — a missing required EvidenceKind yields unverified, never pass.
func TestDescriptorCheckRequires(t *testing.T) {
	t.Parallel()

	// newValidObservation carries EvidenceConversationExcerpt and EvidenceUsage.
	sample := Sample{Observation: newValidObservation()}

	// All required kinds present -> ok, proceed.
	d := newValidDescriptor() // Requires EvidenceUsage
	if a, ok := d.CheckRequires(sample); !ok {
		t.Fatalf("CheckRequires reported missing evidence when present: %+v", a)
	}

	// A required kind absent from the sample -> unverified, not ok.
	d.Requires = []EvidenceKind{EvidenceTiming} // absent from the trace
	a, ok := d.CheckRequires(sample)
	if ok {
		t.Fatal("CheckRequires reported satisfied when required kind is absent")
	}
	if a.Status != StatusUnverified {
		t.Fatalf("missing required evidence produced status %q, want %q (never pass)", a.Status, StatusUnverified)
	}
	if a.Status == StatusPass {
		t.Fatal("missing required evidence must never produce a pass")
	}
	// The produced unverified assessment must itself be valid.
	if err := a.Validate(); err != nil {
		t.Fatalf("unverified assessment from CheckRequires is invalid: %v", err)
	}
	// It must carry the evaluator identity from the descriptor.
	if a.Evaluator != d.Name || a.Revision != d.Revision {
		t.Fatalf("unverified assessment identity = (%q,%q), want (%q,%q)", a.Evaluator, a.Revision, d.Name, d.Revision)
	}
}

// fakeEvaluator is an Evaluator whose Descriptor and Evaluate results are
// preconfigured, used to exercise the interface contract and the
// error-vs-quality-verdict boundary.
type fakeEvaluator struct {
	desc Descriptor
	a    Assessment
	err  error
}

func (f fakeEvaluator) Descriptor() Descriptor { return f.desc }

func (f fakeEvaluator) Evaluate(_ context.Context, _ Sample) (Assessment, error) {
	return f.a, f.err
}

func TestEvaluatorContract(t *testing.T) {
	t.Parallel()

	desc := newValidDescriptor()
	sample := Sample{Observation: newValidObservation()}

	// A quality verdict: the evaluator succeeds and returns a fail assessment
	// with a nil error. A fail is a quality verdict, not an infrastructure error.
	var ev Evaluator = fakeEvaluator{
		desc: desc,
		a:    Fail(desc, Finding{Code: "irrelevant", Severity: SeverityHigh, Message: "answer did not address the question"}),
	}
	a, err := ev.Evaluate(context.Background(), sample)
	if err != nil {
		t.Fatalf("quality-verdict evaluate returned infra error: %v", err)
	}
	if a.Status != StatusFail {
		t.Fatalf("status = %q, want %q", a.Status, StatusFail)
	}
	if err := a.Validate(); err != nil {
		t.Fatalf("fail assessment invalid: %v", err)
	}

	// An infrastructure failure surfaces as the error return, NOT as a fail
	// quality score. The assessment must not read as a fail verdict.
	infra := errors.New("judge endpoint unreachable")
	ev = fakeEvaluator{desc: desc, err: infra}
	a, err = ev.Evaluate(context.Background(), sample)
	if err == nil {
		t.Fatal("infrastructure failure did not surface as an error return")
	}
	if a.Status == StatusFail {
		t.Fatal("infrastructure failure masqueraded as a fail quality score")
	}
	if !errors.Is(err, infra) {
		t.Fatalf("error = %v, want the injected infrastructure error", err)
	}
}

// TestTruncateUTF8 asserts the missing-evidence message truncation cuts on a
// rune boundary and never emits invalid UTF-8, even when the byte cap lands in
// the middle of a multibyte rune.
func TestTruncateUTF8(t *testing.T) {
	t.Parallel()
	// "é" is 2 bytes (0xC3 0xA9); "世" is 3 bytes. Repeating them lets a byte cap
	// fall mid-rune so a naive s[:max] would split it.
	tests := []struct {
		name     string
		in       string
		maxBytes int
	}{
		{"empty", "", 8},
		{"ascii under cap", "hello", 8},
		{"ascii at cap", "hello", 5},
		{"ascii over cap", "hello world", 5},
		{"multibyte cap mid-rune", strings.Repeat("é", 10), 5},
		{"multibyte cap mid-rune three-byte", strings.Repeat("世", 10), 7},
		{"cap zero", "héllo", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := truncateUTF8(tt.in, tt.maxBytes)
			if len(got) > tt.maxBytes {
				t.Fatalf("truncateUTF8(%q, %d) = %q (%d bytes), exceeds cap", tt.in, tt.maxBytes, got, len(got))
			}
			if !utf8.ValidString(got) {
				t.Fatalf("truncateUTF8(%q, %d) = %q, not valid UTF-8", tt.in, tt.maxBytes, got)
			}
			if !strings.HasPrefix(tt.in, got) {
				t.Fatalf("truncateUTF8(%q, %d) = %q, not a prefix of input", tt.in, tt.maxBytes, got)
			}
		})
	}
}
