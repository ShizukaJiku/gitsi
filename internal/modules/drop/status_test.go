package drop

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/mailbox"
)

func TestPendingSummarizesTheMailboxOfEachSide(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if got, err := r.m.pending(ctx, r.edge); err != nil || got != "nothing to receive" {
		t.Fatalf("%q %v", got, err)
	}
	r.putOutbox(mailbox.Text, "uno.txt", "1")
	if got, _ := r.m.pending(ctx, r.edge); got != "1 to receive: uno.txt (gitsi take)" {
		t.Fatalf("%q", got)
	}
	r.putOutbox(mailbox.File, "dos.zip", "2")
	if got, _ := r.m.pending(ctx, r.edge); !strings.HasPrefix(got, "2 to receive, the latest dos.zip") {
		t.Fatalf("%q", got)
	}
	// on the hub: its inbox, without a session
	if got, err := r.m.pending(ctx, r.hubEnv); err != nil || got != "nothing to receive" {
		t.Fatalf("hub: %q %v", got, err)
	}
	left, _ := store(r.hubEnv.Config).List(mailbox.Outbox)
	if len(left) != 2 {
		t.Fatal("status only lists: it takes nothing out of the mailbox")
	}
}

func TestTakeByIDBringsTheItemAndTakesItOutOfTheOutbox(t *testing.T) {
	r := newRig(t)
	it := r.putOutbox(mailbox.File, "report.xml", "<ok/>")
	dir := t.TempDir()
	dest, err := r.m.takeByID(context.Background(), r.edge, it.ID, dir)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dest); string(b) != "<ok/>" || filepath.Dir(dest) != dir {
		t.Fatalf("%s %q", dest, b)
	}
	if left, _ := store(r.hubEnv.Config).List(mailbox.Outbox); len(left) != 0 {
		t.Fatal("like take: it leaves the outbox")
	}
	if _, err := r.m.takeByID(context.Background(), r.edge, it.ID, dir); err == nil {
		t.Fatal("it is gone")
	}
}

func TestWaitingShowsWhatTheEdgeHasNotTakenAndHowOld(t *testing.T) {
	r := newRig(t)
	if got, err := Waiting(context.Background(), r.hubEnv); err != nil || got != "nothing waiting for the edge" {
		t.Fatalf("%q %v", got, err)
	}
	r.putOutbox(mailbox.Text, "old.txt", "1")
	if got, _ := Waiting(context.Background(), r.hubEnv); got != "1 waiting for the edge: old.txt, just arrived (on the edge: gitsi take)" {
		t.Fatalf("%q", got)
	}
	r.putOutbox(mailbox.Text, "new.txt", "2")
	if got, _ := Waiting(context.Background(), r.hubEnv); !strings.HasPrefix(got, "2 waiting for the edge; the oldest, old.txt") {
		t.Fatalf("%q", got)
	}
	for d, want := range map[time.Duration]string{10 * time.Minute: "just arrived", 5 * time.Hour: "5 h old", 30 * time.Hour: "1 day old", 100 * time.Hour: "4 days old"} {
		if got := ageText(d); got != want {
			t.Errorf("%v: %q", d, got)
		}
	}
}
