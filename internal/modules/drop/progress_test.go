package drop

import (
	"context"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShizukaJiku/gitsi/internal/mailbox"
)

func bigFile(t *testing.T, r *rig, name string, size int) string {
	t.Helper()
	b := make([]byte, size)
	rand.New(rand.NewSource(3)).Read(b)
	return r.file(name, string(b))
}

func TestSendShowsTransferProgressForLargeFilesAndQuietSilencesIt(t *testing.T) {
	r := newRig(t)
	p := bigFile(t, r, "big.bin", 3<<20)
	if err := r.m.send(context.Background(), r.edge, []string{p}); err != nil {
		t.Fatal(err)
	}
	out := r.eOut.String()
	for _, want := range []string{"big.bin: 100%", "MiB", "verifying on the hub", "Sent: big.bin"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}

	// -q: only the result
	r2 := newRig(t)
	r2.edge.Quiet = true
	if err := r2.m.send(context.Background(), r2.edge, []string{bigFile(t, r2, "big.bin", 3<<20)}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(r2.eOut.String(), "%") || !strings.Contains(r2.eOut.String(), "Sent: big.bin") {
		t.Fatalf("-q shows no progress but does show the result:\n%s", r2.eOut)
	}
}

func TestSendOfSmallItemsStaysQuiet(t *testing.T) {
	r := newRig(t)
	if err := r.m.send(context.Background(), r.edge, []string{r.file("small.txt", "hello")}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(r.eOut.String(), "%") || strings.Contains(r.eOut.String(), "...") {
		t.Fatalf("small, fast work does not clutter the output:\n%s", r.eOut)
	}
}

func TestReceiveShowsDownloadProgress(t *testing.T) {
	r := newRig(t)
	p := bigFile(t, r, "download.bin", 3<<20)
	f, err := openFile(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	st := &mailbox.Store{Root: mailboxRoot(r.hubEnv)}
	if _, err := st.Add(mailbox.Outbox, mailbox.File, "download.bin", f); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(r.home, "received")
	if err := r.m.take(context.Background(), r.edge, []string{"-dir", dest}); err != nil {
		t.Fatalf("%v\n%s", err, r.eOut)
	}
	out := r.eOut.String()
	for _, want := range []string{"download.bin: 100%", "verifying", "Received: download.bin"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func openFile(p string) (*os.File, error) { return os.Open(p) }
