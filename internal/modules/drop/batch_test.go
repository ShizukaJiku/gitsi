package drop

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShizukaJiku/gitsi/internal/mailbox"
	"github.com/ShizukaJiku/gitsi/internal/module"
)

func TestSendBatchIsRejectedAsAWholeBeforeAnythingIsUploaded(t *testing.T) {
	r := newRig(t)
	old := maxUncompressed
	maxUncompressed = 100
	defer func() { maxUncompressed = old }()
	small := r.file("small.txt", "hello")
	r.file("proy/big.txt", strings.Repeat("A", 5000))
	err := r.m.send(context.Background(), r.edge, []string{small, filepath.Join(r.home, "src", "proy")})
	if err == nil || !strings.Contains(err.Error(), "uncompressed") {
		t.Fatalf("%v", err)
	}
	if items, _ := (&mailbox.Store{Root: mailboxRoot(r.hubEnv)}).List(mailbox.Inbox); len(items) != 0 {
		t.Fatalf("the batch must not be left half done (small.txt was uploaded): %+v", items)
	}
}

func TestSendRejectsABrokenExcludePatternBeforeConnecting(t *testing.T) {
	r := newRig(t)
	opens := 0
	inner := r.m.open
	r.m.open = func(ctx context.Context, env *module.Env) (*session, error) { opens++; return inner(ctx, env) }
	r.file("proy/a.txt", "A")
	err := r.m.send(context.Background(), r.edge, []string{filepath.Join(r.home, "src", "proy"), "-exclude", "[x"})
	if err == nil || opens != 0 {
		t.Fatalf("a broken pattern must fail before connecting: err=%v connections=%d", err, opens)
	}
}

func TestHubPutReportsExcludedEntriesAndRejectsExcludeWithText(t *testing.T) {
	r := newRig(t)
	r.file("proy/a.txt", "A")
	r.file("proy/b.log", "L")
	if err := r.m.send(context.Background(), r.hubEnv, []string{filepath.Join(r.home, "src", "proy"), "-exclude", "*.log"}); err != nil {
		t.Fatalf("%v\n%s", err, r.hOut)
	}
	if !strings.Contains(r.hOut.String(), "1 item(s) left out by -exclude") {
		t.Fatalf("send on the hub must report what was left out, like on the edge:\n%s", r.hOut)
	}
	if err := r.m.send(context.Background(), r.hubEnv, []string{"-text", "hello", "-exclude", "x"}); err == nil {
		t.Fatal("-exclude with -text must be rejected on the hub too")
	}
}
