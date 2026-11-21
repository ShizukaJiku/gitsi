package drop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/clipboard"
	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/hub"
	"github.com/ShizukaJiku/gitsi/internal/layout"
	"github.com/ShizukaJiku/gitsi/internal/mailbox"
	"github.com/ShizukaJiku/gitsi/internal/module"
	"github.com/ShizukaJiku/gitsi/internal/policy"
	"github.com/ShizukaJiku/gitsi/internal/proto"
)

// rig gathers a "hub" (Serve + Handler + on-disk Store) and the edge and hub environments that share it.
type rig struct {
	t      *testing.T
	m      mod
	edge   *module.Env
	hubEnv *module.Env
	eOut   *bytes.Buffer
	hOut   *bytes.Buffer
	home   string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	reposDir := filepath.ToSlash(filepath.Join(home, "repos"))
	mk := func(role config.Role) (*module.Env, *bytes.Buffer) {
		cfg := config.Default()
		cfg.Role, cfg.ReposDir = role, reposDir
		var out bytes.Buffer
		return &module.Env{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &out, Config: cfg}, &out
	}
	r := &rig{t: t, home: home}
	r.edge, r.eOut = mk(config.RoleEdge)
	r.hubEnv, r.hOut = mk(config.RoleHub)
	r.m = mod{clip: func(context.Context) (clipboard.Content, error) { return clipboard.Content{}, clipboard.ErrEmpty }, open: func(ctx context.Context, env *module.Env) (*session, error) {
		cl, sv := net.Pipe()
		go func() {
			h, caps := HubHandler(r.hubEnv)
			hub.Serve(ctx, proto.NewArmorReader(sv), proto.NewArmorWriter(sv), hub.Config{Agent: "t", Caps: caps, Handler: h})
			sv.Close()
		}()
		rw := proto.Armor(cl)
		_, err := proto.ClientHandshake(rw, "edge", mailbox.CapDrop)
		if err != nil {
			return nil, err
		}
		s := &session{RW: rw, Close: func() { cl.Close() }}
		return s, nil
	}}
	return r
}

func (r *rig) file(rel, content string) string {
	p := filepath.Join(r.home, "src", rel)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
	return p
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSendFileThenHubTakesIt(t *testing.T) {
	r := newRig(t)
	p := r.file("report.txt", "report content")
	if err := r.m.send(context.Background(), r.edge, []string{p}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.eOut.String(), "Sent: report.txt") {
		t.Fatal(r.eOut.String())
	}
	if err := r.m.take(context.Background(), r.hubEnv, []string{"-list"}); err != nil || !strings.Contains(r.hOut.String(), "report.txt") {
		t.Fatalf("%v %s", err, r.hOut)
	}
	if !strings.HasPrefix(r.hOut.String(), "[1] ") {
		t.Fatalf("the list numbers the items: %s", r.hOut)
	}
	dest := filepath.Join(r.home, "collected")
	if err := r.m.take(context.Background(), r.hubEnv, []string{"1", "-dir", dest}); err != nil {
		t.Fatal(err)
	}
	if readFile(t, filepath.Join(dest, "report.txt")) != "report content" {
		t.Fatal("different content")
	}
	r.hOut.Reset()
	r.m.take(context.Background(), r.hubEnv, []string{"-list"})
	if !strings.Contains(r.hOut.String(), "(empty)") {
		t.Fatalf("take must empty the inbox: %s", r.hOut)
	}
}

func TestSendFolderPreservesTree(t *testing.T) {
	r := newRig(t)
	r.file("proy/a.txt", "A")
	r.file("proy/sub/b.txt", "B")
	if err := r.m.send(context.Background(), r.edge, []string{filepath.Join(r.home, "src", "proy")}); err != nil {
		t.Fatal(err)
	}
	items, _ := (&mailbox.Store{Root: mailboxRoot(r.hubEnv)}).List(mailbox.Inbox)
	if len(items) != 1 || items[0].Kind != mailbox.Dir {
		t.Fatalf("%+v", items)
	}
	dest := filepath.Join(r.home, "out")
	if err := r.m.take(context.Background(), r.hubEnv, []string{items[0].ID, "-dir", dest}); err != nil {
		t.Fatal(err)
	}
	if readFile(t, filepath.Join(dest, "proy", "sub", "b.txt")) != "B" || readFile(t, filepath.Join(dest, "proy", "a.txt")) != "A" {
		t.Fatal("the tree was not kept")
	}
}

func TestSendTextFlagAndStdin(t *testing.T) {
	r := newRig(t)
	if err := r.m.send(context.Background(), r.edge, []string{"-text", "hello from the edge"}); err != nil {
		t.Fatal(err)
	}
	r.edge.Stdin = strings.NewReader("text from stdin\n")
	if err := r.m.send(context.Background(), r.edge, []string{"-text", "-", "-name", "note.txt"}); err != nil {
		t.Fatal(err)
	}
	items, _ := (&mailbox.Store{Root: mailboxRoot(r.hubEnv)}).List(mailbox.Inbox)
	if len(items) != 2 {
		t.Fatalf("%+v", items)
	}
	r.hOut.Reset()
	for _, it := range items {
		if err := r.m.take(context.Background(), r.hubEnv, []string{it.ID}); err != nil {
			t.Fatal(err)
		}
	}
	out := r.hOut.String()
	if !strings.Contains(out, "hello from the edge") || !strings.Contains(out, "text from stdin") {
		t.Fatalf("the text must be shown on stdout: %q", out)
	}
}

func TestPutThenReceive(t *testing.T) {
	r := newRig(t)
	r.file("res/build.log", "ok")
	if err := r.m.send(context.Background(), r.hubEnv, []string{filepath.Join(r.home, "src", "res")}); err != nil {
		t.Fatal(err)
	}
	if err := r.m.send(context.Background(), r.hubEnv, []string{"-text", "message from the hub"}); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(r.home, "received")
	if err := r.m.take(context.Background(), r.edge, []string{"-dir", dest}); err != nil {
		t.Fatal(err)
	}
	if readFile(t, filepath.Join(dest, "res", "build.log")) != "ok" {
		t.Fatal("folder not received")
	}
	if !strings.Contains(r.eOut.String(), "message from the hub") {
		t.Fatalf("the text is shown: %s", r.eOut)
	}
	// the outbox ends up empty and no partials remain
	r.eOut.Reset()
	r.m.take(context.Background(), r.edge, []string{"-dir", dest})
	if !strings.Contains(r.eOut.String(), "There is nothing") {
		t.Fatal(r.eOut.String())
	}
	if m, _ := filepath.Glob(filepath.Join(dest, ".gitsi-*")); len(m) != 0 {
		t.Fatalf("partials remained: %v", m)
	}
}

func TestReceiveKeepListAndSelect(t *testing.T) {
	r := newRig(t)
	r.m.send(context.Background(), r.hubEnv, []string{r.file("a.txt", "AAA")})
	r.m.send(context.Background(), r.hubEnv, []string{r.file("b.txt", "BBB")})
	r.m.take(context.Background(), r.edge, []string{"-list"})
	if !strings.Contains(r.eOut.String(), "a.txt") || !strings.Contains(r.eOut.String(), "b.txt") {
		t.Fatal(r.eOut.String())
	}
	st := &mailbox.Store{Root: mailboxRoot(r.hubEnv)}
	items, _ := st.List(mailbox.Outbox)
	var idA string
	for _, it := range items {
		if it.Name == "a.txt" {
			idA = it.ID
		}
	}
	dest := filepath.Join(r.home, "d")
	if err := r.m.take(context.Background(), r.edge, []string{"-dir", dest, "-keep", idA[:5]}); err != nil {
		t.Fatalf("%v | %s", err, r.eOut)
	}
	if readFile(t, filepath.Join(dest, "a.txt")) != "AAA" {
		t.Fatal("a.txt was not received")
	}
	if _, err := os.Stat(filepath.Join(dest, "b.txt")); err == nil {
		t.Fatal("only the requested item should be received")
	}
	if left, _ := st.List(mailbox.Outbox); len(left) != 2 {
		t.Fatalf("-keep must keep the items: %+v", left)
	}
	if err := r.m.take(context.Background(), r.edge, []string{"-dir", dest, "zzzzzz"}); err == nil {
		t.Fatal("a missing id must fail")
	}
}

func TestReceiveDoesNotOverwriteAndKeepsItemInOutbox(t *testing.T) {
	r := newRig(t)
	r.m.send(context.Background(), r.hubEnv, []string{r.file("dup.txt", "new")})
	dest := filepath.Join(r.home, "d")
	os.MkdirAll(dest, 0o755)
	os.WriteFile(filepath.Join(dest, "dup.txt"), []byte("local"), 0o644)
	if err := r.m.take(context.Background(), r.edge, []string{"-dir", dest}); err == nil {
		t.Fatal("it should report the failure")
	}
	if readFile(t, filepath.Join(dest, "dup.txt")) != "local" {
		t.Fatal("an existing file was overwritten")
	}
	st := &mailbox.Store{Root: mailboxRoot(r.hubEnv)}
	if left, _ := st.List(mailbox.Outbox); len(left) != 1 {
		t.Fatal("the item must not be removed if it could not be received")
	}
}

func TestReceiveResumesPartialDownload(t *testing.T) {
	r := newRig(t)
	content := strings.Repeat("0123456789", 1000)
	st := &mailbox.Store{Root: mailboxRoot(r.hubEnv)}
	it, _ := st.Add(mailbox.Outbox, mailbox.File, "big.txt", strings.NewReader(content))
	dest := filepath.Join(r.home, "d")
	os.MkdirAll(dest, 0o755)
	os.WriteFile(filepath.Join(dest, ".gitsi-"+it.ID+".part"), []byte(content[:4000]), 0o600) // an earlier interrupted download
	if err := r.m.take(context.Background(), r.edge, []string{"-dir", dest}); err != nil {
		t.Fatal(err)
	}
	if readFile(t, filepath.Join(dest, "big.txt")) != content {
		t.Fatal("resuming produced a different file")
	}
}

func TestPartialWithWrongPrefixIsDetected(t *testing.T) {
	r := newRig(t)
	content := strings.Repeat("abcdefghij", 500)
	st := &mailbox.Store{Root: mailboxRoot(r.hubEnv)}
	it, _ := st.Add(mailbox.Outbox, mailbox.File, "x.txt", strings.NewReader(content))
	dest := filepath.Join(r.home, "d")
	os.MkdirAll(dest, 0o755)
	os.WriteFile(filepath.Join(dest, ".gitsi-"+it.ID+".part"), []byte(strings.Repeat("Z", 1000)), 0o600) // corrupt partial
	if err := r.m.take(context.Background(), r.edge, []string{"-dir", dest}); err == nil || !strings.Contains(r.eOut.String(), "verification failed") {
		t.Fatalf("the hash must detect the corrupt partial: %v\n%s", err, r.eOut)
	}
	if _, err := os.Stat(filepath.Join(dest, "x.txt")); err == nil {
		t.Fatal("a corrupt file must not be delivered")
	}
}

func TestSendArgumentErrors(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if err := r.m.send(ctx, r.edge, nil); !errors.Is(err, clipboard.ErrEmpty) {
		t.Errorf("without arguments it sends the clipboard; when empty, it says so: %v", err)
	}
	if err := r.m.send(ctx, r.edge, []string{filepath.Join(r.home, "missing")}); err == nil {
		t.Error("missing path")
	}
	if err := r.m.send(ctx, r.edge, []string{r.file("a", "x"), "-text", "t"}); err == nil {
		t.Error("paths and text at once")
	}
	if err := r.m.send(ctx, r.edge, []string{"-text", ""}); err == nil {
		t.Error("empty text")
	}
}

func TestCommandsDeclarePolicyAndRoles(t *testing.T) {
	got := map[string]module.Command{}
	for _, c := range (mod{}).Commands() {
		got[c.Name] = c
	}
	// send and take are the same verbs on both machines; the policy is only required on the edge (app.authorize)
	if c := got["send"]; c.Feature != policy.Send || !c.NeedsHub || c.Roles != module.Both {
		t.Errorf("send: %+v", c)
	}
	if c := got["take"]; c.Feature != policy.Take || !c.NeedsHub || c.Roles != module.Both {
		t.Errorf("take: %+v", c)
	}
}

func TestMaterializeRefusesOverwriteOfFolder(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "folder"), 0o755)
	_, err := materialize(mailbox.Item{Kind: mailbox.Dir, Name: "folder"}, strings.NewReader(""), dir, io.Discard, 0)
	if err == nil {
		t.Fatal("it must not mix with an existing folder")
	}
}

func TestSendClipboardImageAndText(t *testing.T) {
	r := newRig(t)
	png := []byte("\x89PNG\r\n\x1a\n" + strings.Repeat("pixel", 100))
	r.m.clip = func(context.Context) (clipboard.Content, error) {
		return clipboard.Content{Kind: clipboard.Image, PNG: png}, nil
	}
	if err := r.m.send(context.Background(), r.edge, []string{"-clip"}); err != nil {
		t.Fatal(err)
	}
	st := &mailbox.Store{Root: mailboxRoot(r.hubEnv)}
	items, _ := st.List(mailbox.Inbox)
	if len(items) != 1 || items[0].Kind != mailbox.File || !strings.HasPrefix(items[0].Name, "capture-") || !strings.HasSuffix(items[0].Name, ".png") {
		t.Fatalf("%+v", items)
	}
	dest := filepath.Join(r.home, "img")
	if err := r.m.take(context.Background(), r.hubEnv, []string{items[0].ID, "-dir", dest}); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(dest, items[0].Name)); got != string(png) {
		t.Fatal("the image arrived different")
	}

	r.m.clip = func(context.Context) (clipboard.Content, error) {
		return clipboard.Content{Kind: clipboard.Text, Text: "copied"}, nil
	}
	if err := r.m.send(context.Background(), r.edge, []string{"-clip"}); err != nil {
		t.Fatal(err)
	}
	items, _ = st.List(mailbox.Inbox)
	if len(items) != 1 || items[0].Kind != mailbox.Text {
		t.Fatalf("%+v", items)
	}
}

func TestSendClipboardErrors(t *testing.T) {
	r := newRig(t)
	r.m.clip = func(context.Context) (clipboard.Content, error) { return clipboard.Content{}, clipboard.ErrEmpty }
	if err := r.m.send(context.Background(), r.edge, []string{"-clip"}); !errors.Is(err, clipboard.ErrEmpty) {
		t.Fatalf("empty clipboard: %v", err)
	}
	if err := r.m.send(context.Background(), r.edge, []string{"-clip", "-text", "x"}); err == nil {
		t.Error("-clip and -text at once")
	}
	if err := r.m.send(context.Background(), r.edge, []string{"-clip", r.file("a", "x")}); err == nil {
		t.Error("-clip and a path at once")
	}
}

func TestListingsAreJSONWithJSONMode(t *testing.T) {
	r := newRig(t)
	r.edge.JSON, r.hubEnv.JSON = true, true
	// empty: [] (not null, not text)
	r.hOut.Reset()
	r.m.take(context.Background(), r.hubEnv, []string{"-list"})
	if strings.TrimSpace(r.hOut.String()) != "[]" {
		t.Fatalf("empty inbox: %q", r.hOut)
	}
	r.m.send(context.Background(), r.hubEnv, []string{"-text", "hello"})
	r.eOut.Reset()
	r.m.take(context.Background(), r.edge, []string{"-list"})
	var items []mailbox.Item
	if err := json.Unmarshal(r.eOut.Bytes(), &items); err != nil || len(items) != 1 || items[0].Kind != mailbox.Text {
		t.Fatalf("take -list --json: %v\n%s", err, r.eOut)
	}
}

// -q suppresses the progress of big transfers; the result is still shown.
func TestQuietSuppressesProgressButNotResult(t *testing.T) {
	r := newRig(t)
	big := strings.Repeat("x", 3<<20)
	p := r.file("big.bin", big)
	if err := r.m.send(context.Background(), r.edge, []string{p}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.eOut.String(), "%") {
		t.Fatalf("without -q a big transfer shows progress:\n%s", r.eOut)
	}
	r2 := newRig(t)
	r2.edge.Quiet = true
	p2 := r2.file("big.bin", big)
	if err := r2.m.send(context.Background(), r2.edge, []string{p2}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(r2.eOut.String(), "%") || !strings.Contains(r2.eOut.String(), "Sent: big.bin") {
		t.Fatalf("-q: no progress but a result:\n%s", r2.eOut)
	}
}

// mailboxRoot is the mailbox folder of a hub environment, as the hub itself decides it.
func mailboxRoot(env *module.Env) string {
	home, _ := os.UserHomeDir()
	return layout.ForHub(env.Config, home).Mailbox
}

// The mailbox lives where the hub decides (config.home), not where an edge derives or asks for it.
func TestMailboxLivesWhereTheHubDecides(t *testing.T) {
	r := newRig(t)
	custom := filepath.ToSlash(filepath.Join(r.home, "data", "gitsi"))
	r.hubEnv.Config.Home = custom
	if err := r.m.send(context.Background(), r.hubEnv, []string{"-text", "hello"}); err != nil {
		t.Fatal(err)
	}
	items, _ := (&mailbox.Store{Root: filepath.FromSlash(custom + "/mailbox")}).List(mailbox.Outbox)
	if len(items) != 1 {
		t.Fatalf("the item must be in <home>/mailbox: %+v", items)
	}
}

func TestSendFolderWithExclude(t *testing.T) {
	r := newRig(t)
	r.file("proy/main.go", "M")
	r.file("proy/node_modules/dep/i.js", "JS")
	r.file("proy/debug.log", "L")
	err := r.m.send(context.Background(), r.edge, []string{filepath.Join(r.home, "src", "proy"), "-exclude", "node_modules", "-exclude", "*.log"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.eOut.String(), "2 item(s) left out by -exclude") {
		t.Fatalf("it must report what was left out: %s", r.eOut)
	}
	items, _ := (&mailbox.Store{Root: mailboxRoot(r.hubEnv)}).List(mailbox.Inbox)
	dest := filepath.Join(r.home, "out")
	if err := r.m.take(context.Background(), r.hubEnv, []string{items[0].ID, "-dir", dest}); err != nil {
		t.Fatal(err)
	}
	if readFile(t, filepath.Join(dest, "proy", "main.go")) != "M" {
		t.Fatal("what was not excluded is missing")
	}
	for _, gone := range []string{"node_modules", "debug.log"} {
		if _, err := os.Stat(filepath.Join(dest, "proy", gone)); err == nil {
			t.Errorf("%s should not be sent", gone)
		}
	}
}

func TestSendRejectsBadExcludePattern(t *testing.T) {
	r := newRig(t)
	r.file("proy/a.txt", "A")
	if err := r.m.send(context.Background(), r.edge, []string{filepath.Join(r.home, "src", "proy"), "-exclude", "[x"}); err == nil {
		t.Fatal("an invalid pattern must fail before sending")
	}
}

func inboxItem(t *testing.T, r *rig) mailbox.Item {
	t.Helper()
	items, _ := (&mailbox.Store{Root: mailboxRoot(r.hubEnv)}).List(mailbox.Inbox)
	if len(items) != 1 {
		t.Fatalf("%+v", items)
	}
	return items[0]
}

func TestSendFolderIsCompressedWhenHubSupportsIt(t *testing.T) {
	r := newRig(t)
	text := strings.Repeat("line of code\n", 4000)
	r.file("proy/a.txt", text)
	if err := r.m.send(context.Background(), r.edge, []string{filepath.Join(r.home, "src", "proy")}); err != nil {
		t.Fatal(err)
	}
	it := inboxItem(t, r)
	if it.Enc != mailbox.EncGzip || it.Size*5 > int64(len(text)) {
		t.Fatalf("it should travel compressed: enc=%q size=%d (original %d)", it.Enc, it.Size, len(text))
	}
	dest := filepath.Join(r.home, "out")
	if err := r.m.take(context.Background(), r.hubEnv, []string{it.ID, "-dir", dest}); err != nil {
		t.Fatal(err)
	}
	if readFile(t, filepath.Join(dest, "proy", "a.txt")) != text {
		t.Fatal("the content does not match after decompressing")
	}
}

func TestMaterializeRejectsUnknownEncoding(t *testing.T) {
	_, err := materialize(mailbox.Item{Kind: mailbox.Dir, Name: "folder", Enc: "zstd"}, strings.NewReader(""), t.TempDir(), io.Discard, 0)
	if err == nil || !strings.Contains(err.Error(), "zstd") {
		t.Fatalf("an unknown encoding must ask to update: %v", err)
	}
}

func TestSendChecksArgumentsBeforeConnecting(t *testing.T) {
	r := newRig(t)
	opens := 0
	inner := r.m.open
	r.m.open = func(ctx context.Context, env *module.Env) (*session, error) { opens++; return inner(ctx, env) }

	for name, args := range map[string][]string{
		"missing path":        {filepath.Join(r.home, "missing")},
		"-exclude with -text": {"-text", "hello", "-exclude", "x"},
		"-exclude with -clip": {"-clip", "-exclude", "x"},
		"no arguments":        nil,
		"empty text":          {"-text", ""},
		"invalid name":        {"-text", "hello", "-name", "con"},
		"paths and -text":     {"-text", "hello", "some.txt"},
	} {
		if err := r.m.send(context.Background(), r.edge, args); err == nil {
			t.Errorf("%s must be rejected", name)
		}
	}
	if opens != 0 {
		t.Fatalf("%d connections were opened: argument errors must be detected before connecting", opens)
	}
}

func TestSendFolderRefusedWhenItsUncompressedSizeExceedsWhatTheReceiverExtracts(t *testing.T) {
	r := newRig(t)
	old := maxUncompressed
	maxUncompressed = 100
	defer func() { maxUncompressed = old }()
	r.file("proy/big.txt", strings.Repeat("A", 5000)) // compresses to almost nothing, but takes 5000 uncompressed
	err := r.m.send(context.Background(), r.edge, []string{filepath.Join(r.home, "src", "proy")})
	if err == nil || !strings.Contains(err.Error(), "uncompressed") {
		t.Fatalf("it must be rejected BEFORE sending: %v", err)
	}
	if items, _ := (&mailbox.Store{Root: mailboxRoot(r.hubEnv)}).List(mailbox.Inbox); len(items) != 0 {
		t.Fatalf("nothing should have been uploaded: %+v", items)
	}
}

// 'take -rm' discards items without extracting them; if any id is missing it removes none (it leaves nothing half done).
func TestInboxRmDiscardsAndIsAllOrNothing(t *testing.T) {
	r := newRig(t)
	for _, n := range []string{"a.txt", "b.txt", "c.txt"} {
		if err := r.m.send(context.Background(), r.edge, []string{r.file(n, "content "+n)}); err != nil {
			t.Fatal(err)
		}
	}
	items, err := store(r.hubEnv.Config).List(mailbox.Inbox)
	if err != nil || len(items) != 3 {
		t.Fatalf("%v %d", err, len(items))
	}
	if err := r.m.take(context.Background(), r.hubEnv, []string{"-rm", items[0].ID, "missing"}); err == nil {
		t.Fatal("a missing id must give an error")
	}
	if left, _ := store(r.hubEnv.Config).List(mailbox.Inbox); len(left) != 3 {
		t.Fatalf("with a missing id none may be removed: %d remain", len(left))
	}
	r.hOut.Reset()
	if err := r.m.take(context.Background(), r.hubEnv, []string{"-rm", items[0].ID, items[1].ID[:8]}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.hOut.String(), "Discarded: "+items[0].ID) {
		t.Fatal(r.hOut.String())
	}
	left, _ := store(r.hubEnv.Config).List(mailbox.Inbox)
	if len(left) != 1 || left[0].ID != items[2].ID {
		t.Fatalf("only the third may remain: %+v", left)
	}
	if err := r.m.take(context.Background(), r.hubEnv, []string{"-rm"}); err == nil {
		t.Fatal("rm without ids must give the usage")
	}
}

// 'take -show' shows a text without taking it out of the mailbox; for a file it only gives its details and how to receive it.
func TestInboxShowDisplaysTextAndKeepsIt(t *testing.T) {
	r := newRig(t)
	if err := r.m.send(context.Background(), r.edge, []string{"-text", "report from the edge"}); err != nil {
		t.Fatal(err)
	}
	if err := r.m.send(context.Background(), r.edge, []string{r.file("data.bin", "binary")}); err != nil {
		t.Fatal(err)
	}
	items, _ := store(r.hubEnv.Config).List(mailbox.Inbox)
	var text, file mailbox.Item
	for _, it := range items {
		if it.Kind == mailbox.Text {
			text = it
		} else {
			file = it
		}
	}
	r.hOut.Reset()
	if err := r.m.take(context.Background(), r.hubEnv, []string{"-show", text.ID[:8]}); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(r.hOut.String()) != "report from the edge" {
		t.Fatalf("it must show the text as is: %q", r.hOut.String())
	}
	r.hOut.Reset()
	if err := r.m.take(context.Background(), r.hubEnv, []string{"-show", file.ID}); err != nil || !strings.Contains(r.hOut.String(), "Not a text") || strings.Contains(r.hOut.String(), "binary") {
		t.Fatalf("%v %q", err, r.hOut.String())
	}
	if left, _ := store(r.hubEnv.Config).List(mailbox.Inbox); len(left) != 2 {
		t.Fatalf("show must take nothing out of the inbox: %d remain", len(left))
	}
	for _, args := range [][]string{{"-show"}, {"-show", "a", "b"}, {"-show", "missing"}} {
		if err := r.m.take(context.Background(), r.hubEnv, args); err == nil {
			t.Errorf("%q must fail", args)
		}
	}
}

// putOutbox leaves items in the hub's outbox, spaced in time (so -last is clear).
func (r *rig) putOutbox(kind mailbox.Kind, name, content string) mailbox.Item {
	r.t.Helper()
	it, err := store(r.hubEnv.Config).Add(mailbox.Outbox, kind, name, strings.NewReader(content))
	if err != nil {
		r.t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	return it
}

func TestReceiveLastTakesOnlyTheNewest(t *testing.T) {
	r := newRig(t)
	r.edge.Config.Policy.AllowTake = true
	old := r.putOutbox(mailbox.File, "old.txt", "old")
	r.putOutbox(mailbox.File, "new.txt", "new")
	dest := filepath.Join(r.home, "received")
	if err := r.m.take(context.Background(), r.edge, []string{"-last", "-dir", dest}); err != nil {
		t.Fatal(err)
	}
	if readFile(t, filepath.Join(dest, "new.txt")) != "new" {
		t.Fatal("it must receive the most recent")
	}
	if _, err := os.Stat(filepath.Join(dest, "old.txt")); err == nil {
		t.Fatal("-last must not receive the others")
	}
	left, _ := store(r.hubEnv.Config).List(mailbox.Outbox)
	if len(left) != 1 || left[0].ID != old.ID {
		t.Fatalf("the old one stays in the outbox: %+v", left)
	}
	if err := r.m.take(context.Background(), r.edge, []string{"-last", old.ID}); err == nil {
		t.Fatal("-last with an id is ambiguous: it must give an error")
	}
}

// clipRecorder is a fake clipboard.
type clipRecorder struct{ got []clipboard.Content }

func (c *clipRecorder) write(_ context.Context, ct clipboard.Content) error {
	c.got = append(c.got, ct)
	return nil
}

func TestReceiveClipCopiesTextOrImageAndWritesNoFile(t *testing.T) {
	r := newRig(t)
	rec := &clipRecorder{}
	r.m.toClip = rec.write
	r.m.clipDir = filepath.Join(r.home, "clip")
	r.edge.Config.Policy.AllowTake = true
	dir := filepath.Join(r.home, "must-not-be-created")

	r.putOutbox(mailbox.Text, "note.txt", "text to paste")
	if err := r.m.take(context.Background(), r.edge, []string{"-clip", "-dir", dir}); err != nil {
		t.Fatal(err)
	}
	if len(rec.got) != 1 || rec.got[0].Kind != clipboard.Text || rec.got[0].Text != "text to paste" {
		t.Fatalf("%+v", rec.got)
	}
	if !strings.Contains(r.eOut.String(), "Copied to the clipboard: note.txt") || strings.Contains(r.eOut.String(), "text to paste") {
		t.Fatalf("with -clip the text goes to the clipboard, not to the screen:\n%s", r.eOut)
	}

	png := "\x89PNG\r\n\x1a\nfake"
	r.putOutbox(mailbox.File, "capture.PNG", png)
	if err := r.m.take(context.Background(), r.edge, []string{"-clip", "-dir", dir}); err != nil {
		t.Fatal(err)
	}
	if len(rec.got) != 2 || string(rec.got[1].PNG) != png || len(rec.got[1].Files) != 1 {
		t.Fatalf("an image goes as an image (and as a file): %+v", rec.got[1])
	}
	if _, err := os.Stat(dir); err == nil {
		t.Fatal("-clip does not write to the target folder")
	}
	if left, _ := store(r.hubEnv.Config).List(mailbox.Outbox); len(left) != 0 {
		t.Fatalf("what is copied leaves the outbox (unless -keep): %d", len(left))
	}
}

// Any file (or folder) is left in -clip's folder and its path goes to the clipboard, as when copied in Explorer; that way it
// pastes into Explorer, an email or a chat. An image also goes as an image.
func TestReceiveClipPutsFilesAndFoldersAsPaths(t *testing.T) {
	r := newRig(t)
	rec := &clipRecorder{}
	r.m.toClip = rec.write
	r.m.clipDir = filepath.Join(r.home, "clip")
	r.edge.Config.Policy.AllowTake = true

	pdf := r.putOutbox(mailbox.File, "report.pdf", "%PDF-1.7 content")
	if err := r.m.take(context.Background(), r.edge, []string{"-clip", pdf.ID[:8]}); err != nil {
		t.Fatal(err)
	}
	got := rec.got[0]
	if len(got.Files) != 1 || len(got.PNG) != 0 || got.Text != "" {
		t.Fatalf("a PDF goes only as a file: %+v", got)
	}
	if want := filepath.Join(r.m.clipDir, pdf.ID, "report.pdf"); got.Files[0] != want || readFile(t, want) != "%PDF-1.7 content" {
		t.Fatalf("it must end up verified in %s: %v", want, got.Files)
	}

	img := r.putOutbox(mailbox.File, "capture.png", "\u0089PNGdata")
	if err := r.m.take(context.Background(), r.edge, []string{"-clip", img.ID[:8]}); err != nil {
		t.Fatal(err)
	}
	if g := rec.got[1]; len(g.Files) != 1 || string(g.PNG) != "\u0089PNGdata" {
		t.Fatalf("an image goes as an image and as a file: %+v", g)
	}

	// a folder: the hub leaves it with 'send' and it pastes whole
	src := filepath.Join(r.home, "results")
	os.MkdirAll(filepath.Join(src, "sub"), 0o755)
	os.WriteFile(filepath.Join(src, "sub", "a.txt"), []byte("A"), 0o644)
	if err := r.m.send(context.Background(), r.hubEnv, []string{src}); err != nil {
		t.Fatal(err)
	}
	if err := r.m.take(context.Background(), r.edge, []string{"-clip", "-last"}); err != nil {
		t.Fatal(err)
	}
	g := rec.got[2]
	if len(g.Files) != 1 || filepath.Base(g.Files[0]) != "results" || readFile(t, filepath.Join(g.Files[0], "sub", "a.txt")) != "A" {
		t.Fatalf("the folder pastes whole: %+v", g)
	}
	if !strings.Contains(r.eOut.String(), "Saved in") {
		t.Fatal(r.eOut.String())
	}
}

func TestReceiveClipNeedsASingleItemAndPrunesOldCopies(t *testing.T) {
	r := newRig(t)
	rec := &clipRecorder{}
	r.m.toClip = rec.write
	r.m.clipDir = filepath.Join(r.home, "clip")
	r.edge.Config.Policy.AllowTake = true
	old := filepath.Join(r.m.clipDir, "0000000000000000")
	os.MkdirAll(old, 0o700)
	past := time.Now().Add(-8 * 24 * time.Hour)
	os.Chtimes(old, past, past)

	r.putOutbox(mailbox.Text, "a.txt", "a")
	r.putOutbox(mailbox.File, "b.bin", "b")
	if err := r.m.take(context.Background(), r.edge, []string{"-clip"}); err == nil || !strings.Contains(err.Error(), "use -last") {
		t.Fatalf("with several items one must choose: %v", err)
	}
	if err := r.m.take(context.Background(), r.edge, []string{"-clip", "-last", "-keep"}); err != nil {
		t.Fatal(err)
	}
	if left, _ := store(r.hubEnv.Config).List(mailbox.Outbox); len(left) != 2 {
		t.Fatalf("with -keep it stays in the outbox: %d", len(left))
	}
	if _, err := os.Stat(old); err == nil {
		t.Fatal("copies older than 7 days are removed")
	}
}

// Without -dir, what is received goes to Downloads\gitsi and not to the current folder.
func TestReceiveDefaultsToDownloadsGitsi(t *testing.T) {
	r := newRig(t)
	r.edge.Config.Policy.AllowTake = true
	r.putOutbox(mailbox.File, "result.txt", "hello")
	if err := r.m.take(context.Background(), r.edge, []string{"-keep"}); err != nil { // with any parameter it is not -clip
		t.Fatal(err)
	}
	if readFile(t, filepath.Join(r.home, "Downloads", "gitsi", "result.txt")) != "hello" {
		t.Fatal("it must end up in Downloads/gitsi")
	}
}

// plain 'gitsi take' = 'take -last -clip'; plain 'gitsi send' = 'send -clip'.
func TestBareReceiveAndSendUseTheClipboard(t *testing.T) {
	r := newRig(t)
	rec := &clipRecorder{}
	r.m.toClip = rec.write
	r.m.clipDir = filepath.Join(r.home, "clip")
	r.edge.Config.Policy.AllowTake = true
	r.putOutbox(mailbox.Text, "old.txt", "old")
	r.putOutbox(mailbox.Text, "new.txt", "new")
	if err := r.m.take(context.Background(), r.edge, nil); err != nil {
		t.Fatal(err)
	}
	if len(rec.got) != 1 || rec.got[0].Text != "new" {
		t.Fatalf("plain take brings the latest to the clipboard: %+v", rec.got)
	}
	if left, _ := store(r.hubEnv.Config).List(mailbox.Outbox); len(left) != 1 || left[0].Name != "old.txt" {
		t.Fatalf("%+v", left)
	}

	r.m.clip = func(context.Context) (clipboard.Content, error) {
		return clipboard.Content{Kind: clipboard.Text, Text: "from the clipboard"}, nil
	}
	if err := r.m.send(context.Background(), r.edge, nil); err != nil {
		t.Fatal(err)
	}
	in, _ := store(r.hubEnv.Config).List(mailbox.Inbox)
	if len(in) != 1 || in[0].Name != "clipboard.txt" {
		t.Fatalf("plain send sends the clipboard: %+v", in)
	}
}

// Files and folders copied in Explorer: 'send' sends them as if their paths had been given (with -exclude included).
func TestSendClipWithFilesCopiedInExplorer(t *testing.T) {
	r := newRig(t)
	dir := filepath.Join(r.home, "project")
	os.MkdirAll(filepath.Join(dir, "node_modules"), 0o755)
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0o644)
	os.WriteFile(filepath.Join(dir, "node_modules", "heavy.js"), []byte("x"), 0o644)
	file := r.file("notes.txt", "notes")
	r.m.clip = func(context.Context) (clipboard.Content, error) {
		return clipboard.Content{Kind: clipboard.Files, Files: []string{dir, file}}, nil
	}
	if err := r.m.send(context.Background(), r.edge, []string{"-clip", "-exclude", "node_modules"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.eOut.String(), "From the clipboard: 2 file(s) or folder(s)") {
		t.Fatal(r.eOut.String())
	}
	in, _ := store(r.hubEnv.Config).List(mailbox.Inbox)
	kinds := map[string]mailbox.Kind{}
	for _, it := range in {
		kinds[it.Name] = it.Kind
	}
	if kinds["project"] != mailbox.Dir || kinds["notes.txt"] != mailbox.File {
		t.Fatalf("the copied folder and file must arrive as such: %+v", kinds)
	}
	// the folder arrives without what was excluded
	dest := filepath.Join(r.home, "collected")
	if err := r.m.take(context.Background(), r.hubEnv, []string{"-last", "-dir", dest}); err != nil {
		t.Fatal(err)
	}
	if err := r.m.take(context.Background(), r.hubEnv, []string{"-last", "-dir", dest}); err != nil {
		t.Fatal(err)
	}
	if readFile(t, filepath.Join(dest, "project", "main.go")) != "package main" {
		t.Fatal("the folder is missing")
	}
	if _, err := os.Stat(filepath.Join(dest, "project", "node_modules")); err == nil {
		t.Fatal("-exclude also applies to what is copied in Explorer")
	}
}

func TestInboxTakeLast(t *testing.T) {
	r := newRig(t)
	if err := r.m.take(context.Background(), r.hubEnv, []string{"-last"}); err != nil || !strings.Contains(r.hOut.String(), "There is nothing in the inbox") {
		t.Fatalf("empty inbox: %v %s", err, r.hOut)
	}
	r.m.send(context.Background(), r.edge, []string{r.file("uno.txt", "1")})
	time.Sleep(20 * time.Millisecond)
	r.m.send(context.Background(), r.edge, []string{r.file("dos.txt", "2")})
	dest := filepath.Join(r.home, "collected")
	if err := r.m.take(context.Background(), r.hubEnv, []string{"-last", "-dir", dest}); err != nil {
		t.Fatal(err)
	}
	if readFile(t, filepath.Join(dest, "dos.txt")) != "2" {
		t.Fatal("-last picks the most recent")
	}
	if left, _ := store(r.hubEnv.Config).List(mailbox.Inbox); len(left) != 1 || left[0].Name != "uno.txt" {
		t.Fatalf("%+v", left)
	}
	if err := r.m.take(context.Background(), r.hubEnv, []string{"-last", left0(t, r)}); err == nil {
		t.Fatal("-last with an id is ambiguous")
	}
}

func left0(t *testing.T, r *rig) string {
	l, _ := store(r.hubEnv.Config).List(mailbox.Inbox)
	return l[0].ID
}

// On the hub, take and send are the same verbs with its own mailbox: take takes out of the inbox and send leaves in the outbox.
func TestHubReceiveAndSendWithTheirOwnMailbox(t *testing.T) {
	r := newRig(t)
	rec := &clipRecorder{}
	r.m.toClip = rec.write
	r.m.clipDir = filepath.Join(r.home, "clip")
	// the edge sends two things
	r.m.send(context.Background(), r.edge, []string{"-text", "first"})
	time.Sleep(20 * time.Millisecond)
	r.m.send(context.Background(), r.edge, []string{"-text", "second"})

	// on the hub, plain take: the latest, to the hub's clipboard
	if err := r.m.take(context.Background(), r.hubEnv, nil); err != nil {
		t.Fatal(err)
	}
	if len(rec.got) != 1 || rec.got[0].Text != "second" {
		t.Fatalf("%+v", rec.got)
	}
	if in, _ := store(r.hubEnv.Config).List(mailbox.Inbox); len(in) != 1 {
		t.Fatalf("what is received leaves the inbox: %d", len(in))
	}
	// with -list it only lists
	r.hOut.Reset()
	if err := r.m.take(context.Background(), r.hubEnv, []string{"-list"}); err != nil || !strings.Contains(r.hOut.String(), "text.txt") {
		t.Fatalf("%v %s", err, r.hOut)
	}

	// on the hub, plain send: what is copied, to the outbox for the edge
	r.m.clip = func(context.Context) (clipboard.Content, error) {
		return clipboard.Content{Kind: clipboard.Text, Text: "from the hub"}, nil
	}
	if err := r.m.send(context.Background(), r.hubEnv, nil); err != nil {
		t.Fatal(err)
	}
	out, _ := store(r.hubEnv.Config).List(mailbox.Outbox)
	if len(out) != 1 || out[0].Name != "clipboard.txt" || !strings.Contains(r.hOut.String(), "Left for the edge") {
		t.Fatalf("%+v %s", out, r.hOut)
	}
	// and the edge receives it with its take
	rec.got = nil
	r.edge.Config.Policy.AllowTake = true
	if err := r.m.take(context.Background(), r.edge, nil); err != nil || len(rec.got) != 1 || rec.got[0].Text != "from the hub" {
		t.Fatalf("%v %+v", err, rec.got)
	}
}

// A folder the hub leaves with 'send' travels compressed (as from the edge) and the edge extracts it the same way.
func TestHubSendCompressesFolders(t *testing.T) {
	r := newRig(t)
	src := filepath.Join(r.home, "data")
	os.MkdirAll(src, 0o755)
	os.WriteFile(filepath.Join(src, "big.txt"), []byte(strings.Repeat("text that compresses very well ", 4000)), 0o644)
	if err := r.m.send(context.Background(), r.hubEnv, []string{src}); err != nil {
		t.Fatal(err)
	}
	out, _ := store(r.hubEnv.Config).List(mailbox.Outbox)
	if len(out) != 1 || out[0].Enc != mailbox.EncGzip || out[0].Size > 20000 {
		t.Fatalf("it must be stored compressed: %+v", out)
	}
	r.edge.Config.Policy.AllowTake = true
	dest := filepath.Join(r.home, "received")
	if err := r.m.take(context.Background(), r.edge, []string{"-last", "-dir", dest}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dest, "data", "big.txt")); len(b) != 4000*len("text that compresses very well ") {
		t.Fatal("the edge must extract it whole")
	}
}

// On the edge, 'take -show' and 'take -rm' work on the hub's outbox just as on the hub they work on the inbox.
func TestTakeAcceptsListNumbers(t *testing.T) {
	r := newRig(t)
	r.edge.Config.Policy.AllowTake = true
	a := r.putOutbox(mailbox.Text, "a.txt", "first")
	time.Sleep(10 * time.Millisecond)
	b := r.putOutbox(mailbox.Text, "b.txt", "second")
	items, _ := store(r.hubEnv.Config).List(mailbox.Outbox)
	got, err := pick(items, []string{"2", "1"})
	if err != nil || len(got) != 2 || got[0].ID != b.ID || got[1].ID != a.ID {
		t.Fatalf("%v %+v", err, got)
	}
	if _, err := pick(items, []string{"3"}); err == nil || !strings.Contains(err.Error(), "there are 2") {
		t.Fatalf("number outside the list: %v", err)
	}
	if err := r.m.take(context.Background(), r.edge, []string{"-show", "2"}); err != nil || !strings.Contains(r.eOut.String(), "second") {
		t.Fatalf("%v %s", err, r.eOut)
	}
}

func TestEdgeTakeShowAndRm(t *testing.T) {
	r := newRig(t)
	r.edge.Config.Policy.AllowTake = true
	nota := r.putOutbox(mailbox.Text, "note.txt", "message from the hub")
	zip := r.putOutbox(mailbox.File, "data.zip", "zip")
	if err := r.m.take(context.Background(), r.edge, []string{"-show", nota.ID[:8]}); err != nil || !strings.Contains(r.eOut.String(), "message from the hub") {
		t.Fatalf("%v %s", err, r.eOut)
	}
	if err := r.m.take(context.Background(), r.edge, []string{"-rm", zip.ID[:8]}); err != nil || !strings.Contains(r.eOut.String(), "Discarded: "+zip.ID) {
		t.Fatalf("%v %s", err, r.eOut)
	}
	left, _ := store(r.hubEnv.Config).List(mailbox.Outbox)
	if len(left) != 1 || left[0].ID != nota.ID {
		t.Fatalf("show takes nothing out; rm does: %+v", left)
	}
}
