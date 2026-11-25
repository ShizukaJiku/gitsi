package e2e

import (
	"os/exec"
	"testing"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/procx"
)

// buildGitsi builds gitsi the way scripts/build.ps1 publishes it: with -trimpath and with commit and date in -ldflags. That way
// the test binary announces its version just like the published one ("X.Y.Z (commit ..., built ...)"), and a defect that only
// shows up with that data (comparing versions, showing them) surfaces here and not on real machines.
func buildGitsi(t *testing.T, out string) {
	t.Helper()
	ld := "-X github.com/ShizukaJiku/gitsi/internal/version.Commit=e2e0000 -X github.com/ShizukaJiku/gitsi/internal/version.Date=" + time.Now().Format("2006-01-02")
	if b, err := procx.Background(exec.Command("go", "build", "-trimpath", "-ldflags", ld, "-o", out, "github.com/ShizukaJiku/gitsi/cmd/gitsi")).CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, b)
	}
}
