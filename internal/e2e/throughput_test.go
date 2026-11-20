package e2e

import (
	"bytes"
	"context"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/hubclient"
	"github.com/ShizukaJiku/gitsi/internal/mailbox"
	"github.com/ShizukaJiku/gitsi/internal/transport"
)

// TestUploadThroughputByLayer measures the same Put layer by layer, to locate where upload throughput is lost: the hub binary
// directly, and the same binary launched by a real powershell.exe. Opt-in: GITSI_E2E_BIG_MB=64.
func TestUploadThroughputByLayer(t *testing.T) {
	mb, _ := strconv.Atoi(os.Getenv("GITSI_E2E_BIG_MB"))
	if mb <= 0 || runtime.GOOS != "windows" {
		t.Skip("opt-in measurement: GITSI_E2E_BIG_MB=64 (Windows)")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "gitsi.exe")
	buildGitsi(t, bin)
	exe, _ := os.ReadFile(bin)
	data := make([]byte, mb<<20)
	rand.New(rand.NewSource(1)).Read(data)

	measure := func(name string, tr transport.Transport, home string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		conn, err := hubclient.Dial(ctx, tr, mailbox.CapDrop)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		defer conn.Close()
		data[0]++ // a different item per layer (another id)
		it, _ := mailbox.NewItem(mailbox.File, "m.bin", bytes.NewReader(data), int64(len(data)))
		start := time.Now()
		if _, err := mailbox.NewClient(conn.RW).Put(ctx, it, bytes.NewReader(data), nil); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		el := time.Since(start)
		t.Logf("%-28s %d MiB in %.2f s = %.1f MiB/s", name, mb, el.Seconds(), float64(mb)/el.Seconds())
	}

	mk := func() string {
		home := filepath.Join(t.TempDir(), "profile")
		os.MkdirAll(filepath.Join(home, ".gitsi"), 0o700)
		os.MkdirAll(filepath.Join(home, "gitsi"), 0o755)
		os.WriteFile(filepath.Join(home, "gitsi", "gitsi.exe"), exe, 0o755)
		os.WriteFile(filepath.Join(home, ".gitsi", "config.json"), []byte(`{"version":1,"role":"hub"}`), 0o600)
		return home
	}
	h1 := mk()
	measure("direct binary (pipes)", procTransport{bin: bin, home: h1}, h1)
	h2 := mk()
	var used []string
	measure("via powershell.exe", psTransport{procTransport: procTransport{home: h2}, used: &used}, h2)
}
