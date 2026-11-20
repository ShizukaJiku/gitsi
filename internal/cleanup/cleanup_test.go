package cleanup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/layout"
	"github.com/ShizukaJiku/gitsi/internal/mailbox"
)

func touch(t *testing.T, p string, age time.Duration) {
	t.Helper()
	ts := time.Now().Add(-age)
	if err := os.Chtimes(p, ts, ts); err != nil {
		t.Fatal(err)
	}
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func TestMailboxExpiresOnlyItemsOlderThan14Days(t *testing.T) {
	now := time.Now()
	st := &mailbox.Store{Root: t.TempDir()}
	st.Now = func() time.Time { return now.Add(-15 * 24 * time.Hour) }
	oldIn, _ := st.Add(mailbox.Inbox, mailbox.Text, "old.txt", strings.NewReader("v"))
	oldOut, _ := st.Add(mailbox.Outbox, mailbox.File, "old.zip", strings.NewReader("vv"))
	st.Now = func() time.Time { return now.Add(-13 * 24 * time.Hour) }
	recent, _ := st.Add(mailbox.Inbox, mailbox.Text, "recent.txt", strings.NewReader("r"))

	if got := Mailbox(st, now, true); len(got) != 2 {
		t.Fatalf("dry-run must list the two old ones: %+v", got)
	}
	if left, _ := st.List(mailbox.Inbox); len(left) != 2 {
		t.Fatal("dry-run removes nothing")
	}
	got := Mailbox(st, now, false)
	if len(got) != 2 || got[0].Age < 14*24*time.Hour {
		t.Fatalf("%+v", got)
	}
	in, _ := st.List(mailbox.Inbox)
	out, _ := st.List(mailbox.Outbox)
	if len(in) != 1 || in[0].ID != recent.ID || len(out) != 0 {
		t.Fatalf("only the recent one may remain: %+v %+v (removed %s %s)", in, out, oldIn.ID, oldOut.ID)
	}
}

func TestJobsRemovesOnlyFinishedAndOld(t *testing.T) {
	dir := t.TempDir()
	mk := func(id string, exit bool, age time.Duration) string {
		jd := filepath.Join(dir, id)
		os.MkdirAll(jd, 0o755)
		os.WriteFile(filepath.Join(jd, "out.log"), []byte(strings.Repeat("x", 100)), 0o644)
		if exit {
			os.WriteFile(filepath.Join(jd, "exit"), []byte("0"), 0o644)
			touch(t, filepath.Join(jd, "exit"), age)
		}
		touch(t, jd, age)
		return jd
	}
	old := mk("aaaaaaaa", true, 20*24*time.Hour)
	recent := mk("bbbbbbbb", true, 2*24*time.Hour)
	running := mk("cccccccc", false, 60*24*time.Hour) // no 'exit': never touched, however old
	got := Jobs(dir, time.Now(), false)
	if len(got) != 1 || got[0].Name != "aaaaaaaa" || got[0].Size < 100 {
		t.Fatalf("%+v", got)
	}
	if exists(old) || !exists(recent) || !exists(running) {
		t.Fatal("only the job finished more than 14 days ago is removed")
	}
}

func TestPartsAndClips(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, ".gitsi-1111111111111111.part")
	fresh := filepath.Join(dir, ".gitsi-2222222222222222.part")
	foreign := filepath.Join(dir, "report.part") // not gitsi's
	for _, p := range []string{old, fresh, foreign} {
		os.WriteFile(p, []byte("x"), 0o644)
	}
	touch(t, old, 8*24*time.Hour)
	touch(t, foreign, 30*24*time.Hour)
	if got := Parts(dir, time.Now(), false); len(got) != 1 || exists(old) || !exists(fresh) || !exists(foreign) {
		t.Fatalf("only gitsi's partials older than 7 days: %+v", got)
	}

	root := t.TempDir()
	c1 := filepath.Join(root, "1111111111111111")
	c2 := filepath.Join(root, "2222222222222222")
	os.MkdirAll(c1, 0o700)
	os.MkdirAll(c2, 0o700)
	os.WriteFile(filepath.Join(c1, "a.pdf"), []byte("pdf"), 0o644)
	touch(t, c1, 8*24*time.Hour)
	if got := Clips(root, time.Now(), false); len(got) != 1 || got[0].Size != 3 || exists(c1) || !exists(c2) {
		t.Fatalf("%+v", got)
	}
	if Parts(filepath.Join(dir, "missing"), time.Now(), false) != nil || Clips(filepath.Join(dir, "no"), time.Now(), false) != nil {
		t.Fatal("without a folder there is nothing")
	}
}

// Automatic cleanup runs at most once per hour: several sessions in a row do not walk the folders every time.
func TestHubAutoRunsAtMostOncePerInterval(t *testing.T) {
	root := filepath.ToSlash(t.TempDir())
	lay := layout.Layout{Home: root, Mailbox: root + "/mailbox", Jobs: root + "/jobs"}
	jd := filepath.Join(filepath.FromSlash(lay.Jobs), "aaaaaaaa")
	mkOld := func() {
		os.MkdirAll(jd, 0o755)
		os.WriteFile(filepath.Join(jd, "exit"), []byte("0"), 0o644)
		touch(t, filepath.Join(jd, "exit"), 20*24*time.Hour)
	}
	now := time.Now()
	mkOld()
	if got := HubAuto(lay, now); len(got) != 1 {
		t.Fatalf("the first time it cleans: %+v", got)
	}
	mkOld()
	if got := HubAuto(lay, now.Add(30*time.Minute)); got != nil || !exists(jd) {
		t.Fatal("within the hour it does not clean again")
	}
	if got := HubAuto(lay, now.Add(2*time.Hour)); len(got) != 1 {
		t.Fatalf("after the hour it cleans again: %+v", got)
	}
}

func TestReceiveDirIsDownloadsGitsi(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	if got := ReceiveDir(); got != filepath.Join(home, "Downloads", "gitsi") {
		t.Fatal(got)
	}
}
