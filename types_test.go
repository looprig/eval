package eval

import (
	"errors"
	"strings"
	"testing"
)

// validator is the shared contract every domain identity/enum type satisfies.
// Using it keeps the tables below strictly typed while covering many types.
type validator interface {
	Validate() error
}

func TestName(t *testing.T) {
	t.Parallel()

	atMax := Name(strings.Repeat("n", MaxNameBytes))
	overMax := Name(strings.Repeat("n", MaxNameBytes+1))

	tests := []struct {
		name    string
		val     Name
		wantErr bool
	}{
		{"simple", Name("exact-match"), false},
		{"unicode", Name("café-eval-Δ"), false},
		{"at max bytes", atMax, false},
		{"empty", Name(""), true},
		{"over max bytes", overMax, true},
		{"invalid utf8", Name(string([]byte{0xff, 0xfe, 0x00})), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.val.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Name(%q).Validate() error = %v, wantErr %v", tt.val, err, tt.wantErr)
			}
			if err == nil {
				return
			}
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("error %v (%T) is not a *ValidationError", err, err)
			}
			assertNoUntrustedEcho(t, err, string(tt.val))
		})
	}
}

func TestRevision(t *testing.T) {
	t.Parallel()

	atMax := Revision(strings.Repeat("r", MaxRevisionBytes))
	overMax := Revision(strings.Repeat("r", MaxRevisionBytes+1))

	tests := []struct {
		name    string
		val     Revision
		wantErr bool
	}{
		{"semver", Revision("v1.2.3"), false},
		{"git sha", Revision("9f3a1c2"), false},
		{"at max bytes", atMax, false},
		{"empty", Revision(""), true},
		{"over max bytes", overMax, true},
		{"invalid utf8", Revision(string([]byte{0xc3, 0x28})), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.val.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Revision(%q).Validate() error = %v, wantErr %v", tt.val, err, tt.wantErr)
			}
			if err == nil {
				return
			}
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("error %v (%T) is not a *ValidationError", err, err)
			}
			assertNoUntrustedEcho(t, err, string(tt.val))
		})
	}
}

func TestEnums(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		val     validator
		wantErr bool
	}{
		// Scope — ScopeCase is the valid zero value.
		{"scope case", ScopeCase, false},
		{"scope turn", ScopeTurn, false},
		{"scope session", ScopeSession, false},
		{"scope run", ScopeRun, false},
		{"scope unknown high", Scope(4), true},
		{"scope unknown max", Scope(255), true},

		// Method — MethodProgrammatic is the valid zero value.
		{"method programmatic", MethodProgrammatic, false},
		{"method model", MethodModel, false},
		{"method composite", MethodComposite, false},
		{"method unknown high", Method(3), true},
		{"method unknown max", Method(255), true},

		// AssessmentStatus — no valid zero value (fail-secure).
		{"status pass", StatusPass, false},
		{"status fail", StatusFail, false},
		{"status unverified", StatusUnverified, false},
		{"status error", StatusError, false},
		{"status skipped", StatusSkipped, false},
		{"status zero", AssessmentStatus(""), true},
		{"status unknown", AssessmentStatus("passed"), true},
		{"status injection", AssessmentStatus("pass\x00<script>SECRET</script>"), true},

		// Severity — no valid zero value.
		{"severity info", SeverityInfo, false},
		{"severity low", SeverityLow, false},
		{"severity medium", SeverityMedium, false},
		{"severity high", SeverityHigh, false},
		{"severity critical", SeverityCritical, false},
		{"severity zero", Severity(""), true},
		{"severity unknown", Severity("fatal"), true},

		// Unit — no valid zero value.
		{"unit count", UnitCount, false},
		{"unit ratio", UnitRatio, false},
		{"unit second", UnitSecond, false},
		{"unit token", UnitToken, false},
		{"unit byte", UnitByte, false},
		{"unit zero", Unit(""), true},
		{"unit unknown", Unit("furlong"), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.val.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("%T(%v).Validate() error = %v, wantErr %v", tt.val, tt.val, err, tt.wantErr)
			}
			if err == nil {
				return
			}
			var ee *InvalidEnumError
			if !errors.As(err, &ee) {
				t.Fatalf("error %v (%T) is not a *InvalidEnumError", err, err)
			}
			if ee.Enum == "" {
				t.Errorf("InvalidEnumError.Enum is empty for %q", tt.name)
			}
		})
	}
}

// TestDiagnosticsWithholdUntrustedContent proves string-enum diagnostics never
// echo the offending token, which may originate from untrusted input.
func TestDiagnosticsWithholdUntrustedContent(t *testing.T) {
	t.Parallel()

	const secret = "SECRET-INJECTED-TOKEN"
	vals := []validator{
		AssessmentStatus(secret),
		Severity(secret),
		Unit(secret),
	}
	for _, v := range vals {
		err := v.Validate()
		if err == nil {
			t.Fatalf("%T(%q) unexpectedly validated", v, secret)
		}
		assertNoUntrustedEcho(t, err, secret)
	}
}

func assertNoUntrustedEcho(t *testing.T, err error, untrusted string) {
	t.Helper()
	if untrusted != "" && strings.Contains(err.Error(), untrusted) {
		t.Fatalf("diagnostic %q echoed untrusted content %q", err.Error(), untrusted)
	}
}
