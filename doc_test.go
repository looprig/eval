package eval_test

// Blank import proves the module and its root package are importable and
// build cleanly. If github.com/looprig/eval does not exist or fails to
// compile, this test file will not build.
import (
	"testing"

	_ "github.com/looprig/eval"
)

// TestPackageImportable is a compile-time contract: the test binary only
// links if the root package resolves. The body asserts nothing beyond that
// the package was reachable to build this test.
func TestPackageImportable(t *testing.T) {
	t.Parallel()
}
