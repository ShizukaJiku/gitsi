package provision

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/procx"
	"github.com/ShizukaJiku/gitsi/internal/transport"
	"github.com/ShizukaJiku/gitsi/internal/winps"
)

// psHub simulates the hub: the scripts REALLY run with powershell.exe over a temporary key file, and the "login" is accepted only
// when the key used is in that file (what sshd does).
type psHub struct {
	t      *testing.T
	dir    string // plays the hub's %ProgramData% and %USERPROFILE%
	key    string // private key this connection "logs in" with
	broken bool   // the connection always fails (e.g. network)
}

func (h *psHub) Run(ctx context.Context, cmd string) (transport.Result, error) {
	if h.broken {
		return transport.Result{}, errors.New("ssh: connect to host: timed out")
	}
	_, blob, err := pubBlob(h.key + ".pub")
	if err != nil {
		return transport.Result{}, err
	}
	if b, _ := os.ReadFile(hubAuthFile(h.t, h.dir)); !bytes.Contains(b, []byte(blob)) {
		return transport.Result{ExitCode: 255, Stderr: "Permission denied (publickey)."}, errors.New("Permission denied (publickey)")
	}
	if cmd == "echo gitsi-ok" {
		return transport.Result{Stdout: "gitsi-ok\r\n"}, nil
	}
	c := procx.Background(winps.Command(ctx, "-NoProfile", "-NonInteractive", "-Command", cmd))
	c.Env = append(os.Environ(), "ProgramData="+h.dir, "USERPROFILE="+h.dir)
	var out, errb bytes.Buffer
	c.Stdout, c.Stderr = &out, &errb
	err = c.Run()
	code := 0
	if err != nil {
		code = 1
	}
	return transport.Result{Stdout: out.String(), Stderr: errb.String(), ExitCode: code}, nil
}
func (h *psHub) Stream(context.Context, string) (transport.Stream, error) {
	return nil, errors.New("no")
}
func (h *psHub) Forward(context.Context, []transport.Forward) (transport.Tunnel, error) {
	return nil, errors.New("no")
}

var adminCache = map[string]bool{}

// hubAuthFile is the file the scripts pick: it depends on whether whoever runs the tests is an administrator.
func hubAuthFile(t *testing.T, dir string) string {
	isAdmin, ok := adminCache["v"]
	if !ok {
		out, err := procx.Background(winps.Command(context.Background(), "-NoProfile", "-Command", "(whoami /groups) -match 'S-1-5-32-544' | Measure-Object | ForEach-Object Count")).Output()
		if err != nil {
			t.Fatal(err)
		}
		isAdmin = strings.TrimSpace(string(out)) != "0"
		adminCache["v"] = isAdmin
	}
	if isAdmin {
		return filepath.Join(dir, "ssh", "administrators_authorized_keys")
	}
	return filepath.Join(dir, ".ssh", "authorized_keys")
}

type keyWorld struct {
	sshDir, hubDir, authFile, oldKey, newKey string
	cfg                                      *config.Config
	out                                      *bytes.Buffer
	dialFails                                bool
}

func newKeyWorld(t *testing.T) *keyWorld {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skip("the hub's scripts are Windows PowerShell")
	}
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	w := &keyWorld{sshDir: filepath.Join(home, ".ssh"), hubDir: t.TempDir(), out: &bytes.Buffer{}}
	w.oldKey = filepath.Join(w.sshDir, "old_key_ed25519")
	w.newKey = filepath.Join(w.sshDir, "gitsi_ed25519")
	if err := os.MkdirAll(w.sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := sshKeygen(context.Background(), w.oldKey); err != nil {
		t.Skipf("ssh-keygen not available: %v", err)
	}
	w.authFile = hubAuthFile(t, w.hubDir)
	if err := os.MkdirAll(filepath.Dir(w.authFile), 0o700); err != nil {
		t.Fatal(err)
	}
	oldPub, _, _ := pubBlob(w.oldKey + ".pub")
	// another unrelated key with options in front, CRLF and no trailing newline: all of that must survive
	other := `from="192.0.2.7" ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAnotherForeignKeyNotTouched other`
	if err := os.WriteFile(w.authFile, []byte(other+"\r\n"+oldPub), 0o600); err != nil {
		t.Fatal(err)
	}
	w.cfg = config.Default()
	w.cfg.Hub = config.Hub{Host: "192.0.2.3", User: "user", KeyFile: w.oldKey, Alias: "gitsi-hub"}
	return w
}

func (w *keyWorld) deps(t *testing.T) KeyDeps {
	return KeyDeps{
		Current: &psHub{t: t, dir: w.hubDir, key: w.cfg.Hub.KeyFile},
		Dial:    func(k string) transport.Transport { return &psHub{t: t, dir: w.hubDir, key: k, broken: w.dialFails} },
		Keygen:  sshKeygen,
		Out:     w.out,
	}
}

func (w *keyWorld) rotate(t *testing.T, retire bool) error {
	_, err := RotateKey(context.Background(), w.deps(t), w.cfg, KeyOptions{SSHDir: w.sshDir, Retire: retire, ConfigSSH: true})
	return err
}

func (w *keyWorld) auth(t *testing.T) string {
	b, err := os.ReadFile(w.authFile)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.HasPrefix(b, []byte{0xEF, 0xBB, 0xBF}) {
		t.Fatal("the key file must have no BOM (sshd would reject it)")
	}
	return string(b)
}

func blobOf(t *testing.T, key string) string {
	_, b, err := pubBlob(key + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRotateKeyMigratesWithoutCuttingAccessThenRetiresTheOldOne(t *testing.T) {
	w := newKeyWorld(t)

	// 1. migrate: the new one is registered, tested and used; the old one stays as a fallback
	if err := w.rotate(t, false); err != nil {
		t.Fatalf("%v\n%s", err, w.out)
	}
	a := w.auth(t)
	if !strings.Contains(a, blobOf(t, w.oldKey)) || !strings.Contains(a, blobOf(t, w.newKey)) || !strings.Contains(a, "AnotherForeignKey") {
		t.Fatalf("the old, the new and the unrelated ones must remain:\n%s", a)
	}
	newPub, _, _ := pubBlob(w.newKey + ".pub")
	ownLine := false
	for _, l := range strings.Split(strings.ReplaceAll(a, "\r\n", "\n"), "\n") {
		ownLine = ownLine || l == newPub
	}
	if !ownLine {
		t.Fatalf("the old key came without a trailing newline: the new one must go on its own line:\n%q", a)
	}
	if w.cfg.Hub.KeyFile != w.newKey {
		t.Fatalf("the configuration must use the new one: %s", w.cfg.Hub.KeyFile)
	}
	sc, _ := os.ReadFile(filepath.Join(w.sshDir, "config"))
	if !strings.Contains(string(sc), "IdentityFile "+filepath.ToSlash(w.newKey)) {
		t.Fatalf("~/.ssh/config must use the new one (git):\n%s", sc)
	}
	saved, err := config.Load()
	if err != nil || saved.Hub.KeyFile != w.newKey || saved.Hub.PrevKeyFile != w.oldKey {
		t.Fatalf("it must be saved in config.json: %v %+v", err, saved.Hub)
	}
	for _, want := range []string{"Registered on the hub", "Login with the new key: OK", "stays registered on the hub as a fallback", "-retire-old"} {
		if !strings.Contains(w.out.String(), want) {
			t.Errorf("missing %q:\n%s", want, w.out)
		}
	}

	// 2. repeating does nothing
	w.out.Reset()
	if err := w.rotate(t, false); err != nil || !strings.Contains(w.out.String(), "You already use the key") {
		t.Fatalf("%v\n%s", err, w.out)
	}
	if strings.Count(w.auth(t), blobOf(t, w.newKey)) != 1 {
		t.Fatal("the new one must not be duplicated")
	}

	// 3. retire the old one (in another run, as the user would)
	w.out.Reset()
	if err := w.rotate(t, true); err != nil {
		t.Fatalf("%v\n%s", err, w.out)
	}
	a = w.auth(t)
	if strings.Contains(a, blobOf(t, w.oldKey)) || !strings.Contains(a, blobOf(t, w.newKey)) || !strings.Contains(a, `from="192.0.2.7"`) {
		t.Fatalf("the new and the unrelated ones must remain, without the old one:\n%s", a)
	}
	if _, err := (&psHub{t: t, dir: w.hubDir, key: w.oldKey}).Run(context.Background(), "echo gitsi-ok"); err == nil {
		t.Fatal("the old key must no longer log in")
	}
	if !strings.Contains(w.out.String(), "Previous key removed from the hub") || !strings.Contains(w.out.String(), "you can delete them") {
		t.Fatal(w.out.String())
	}
	if !exists(w.oldKey) {
		t.Fatal("the old key's local files are not deleted on their own")
	}

	// the configuration remembered the previous one to retire it; once retired it is forgotten
	if w.cfg.Hub.PrevKeyFile != "" {
		t.Fatalf("after retiring it no previous key may remain: %s", w.cfg.Hub.PrevKeyFile)
	}

	// 4. retire again: there is nothing to retire
	w.out.Reset()
	if err := w.rotate(t, true); err == nil || !strings.Contains(err.Error(), "there is no previous key") {
		t.Fatalf("%v\n%s", err, w.out)
	}
	// with an explicit -old it can (and it was no longer on the hub)
	w.out.Reset()
	_, err = RotateKey(context.Background(), w.deps(t), w.cfg, KeyOptions{SSHDir: w.sshDir, Retire: true, OldKey: w.oldKey})
	if err != nil || !strings.Contains(w.out.String(), "no longer registered") {
		t.Fatalf("%v\n%s", err, w.out)
	}
}

func TestRotateKeyMigrateAndRetireInOneStep(t *testing.T) {
	w := newKeyWorld(t)
	if err := w.rotate(t, true); err != nil {
		t.Fatalf("%v\n%s", err, w.out)
	}
	if a := w.auth(t); strings.Contains(a, blobOf(t, w.oldKey)) || !strings.Contains(a, blobOf(t, w.newKey)) {
		t.Fatalf("%s", a)
	}
}

func TestRotateKeyKeepsTheOldKeyIfTheNewLoginFails(t *testing.T) {
	w := newKeyWorld(t)
	w.dialFails = true
	err := w.rotate(t, true)
	if err == nil || !strings.Contains(err.Error(), "keeps using") {
		t.Fatalf("%v", err)
	}
	if w.cfg.Hub.KeyFile != w.oldKey {
		t.Fatal("when the new one cannot log in, the configuration does not change")
	}
	if _, serr := os.Stat(filepath.Join(w.sshDir, "config")); serr == nil {
		t.Fatal("~/.ssh/config must not be touched")
	}
	if !strings.Contains(w.auth(t), blobOf(t, w.oldKey)) {
		t.Fatal("the old one is never removed when the new one was not checked")
	}
}

func TestRotateKeyStopsWhenTheHubHasNoKeyFile(t *testing.T) {
	w := newKeyWorld(t)
	// the current connection "logs in" (simulated), but the file the script picks does not exist
	cur := &psHub{t: t, dir: w.hubDir, key: w.oldKey}
	other := t.TempDir()
	d := w.deps(t)
	d.Current = runFunc(func(ctx context.Context, cmd string) (transport.Result, error) {
		if cmd == "echo gitsi-ok" {
			return cur.Run(ctx, cmd)
		}
		return (&psHub{t: t, dir: other, key: w.oldKey}).runScript(ctx, cmd)
	})
	_, err := RotateKey(context.Background(), d, w.cfg, KeyOptions{SSHDir: w.sshDir, ConfigSSH: true})
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("%v", err)
	}
	if w.cfg.Hub.KeyFile != w.oldKey {
		t.Fatal("it must change nothing")
	}
}

func TestRetireRefusesWhenTheNewKeyIsNotInTheHubFile(t *testing.T) {
	w := newKeyWorld(t)
	if err := sshKeygen(context.Background(), w.newKey); err != nil {
		t.Fatal(err)
	}
	w.cfg.Hub.KeyFile, w.cfg.Hub.PrevKeyFile = w.newKey, w.oldKey // the configuration says "new", but the hub only has the old one
	d := w.deps(t)
	d.Dial = func(k string) transport.Transport {
		return runFunc(func(ctx context.Context, cmd string) (transport.Result, error) {
			return (&psHub{t: t, dir: w.hubDir, key: w.oldKey}).runScript(ctx, cmd)
		})
	}
	_, err := RotateKey(context.Background(), d, w.cfg, KeyOptions{SSHDir: w.sshDir, Retire: true})
	if err == nil || !strings.Contains(err.Error(), "the previous one is not removed") {
		t.Fatalf("%v", err)
	}
	if !strings.Contains(w.auth(t), blobOf(t, w.oldKey)) {
		t.Fatal("the old one must stay: it is the only access")
	}
}

func TestRotateKeyNeedsAConfiguredHubAndReportsUnreachableHub(t *testing.T) {
	w := newKeyWorld(t)
	empty := config.Default()
	if _, err := RotateKey(context.Background(), w.deps(t), empty, KeyOptions{SSHDir: w.sshDir}); err == nil || !strings.Contains(err.Error(), "setup edge") {
		t.Fatalf("%v", err)
	}
	d := w.deps(t)
	d.Current = &psHub{t: t, dir: w.hubDir, key: w.oldKey, broken: true}
	_, err := RotateKey(context.Background(), d, w.cfg, KeyOptions{SSHDir: w.sshDir})
	if err == nil || !strings.Contains(err.Error(), "you keep") {
		t.Fatalf("%v", err)
	}
}

func TestPubBlobRejectsWhatCouldBreakTheScript(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		"quote":   "ssh-ed25519 AAAAC3x' ; Remove-Item C:\\ -Recurse #",
		"private": "-----BEGIN OPENSSH PRIVATE KEY-----",
		"dollar":  "ssh-ed25519 AAAAC3x $env:x",
	} {
		p := filepath.Join(dir, name+".pub")
		os.WriteFile(p, []byte(content), 0o600)
		if _, _, err := pubBlob(p); err == nil {
			t.Errorf("%s: must be rejected", name)
		}
	}
	if _, _, err := pubBlob(filepath.Join(dir, "missing.pub")); err == nil {
		t.Error("a missing .pub is an error")
	}
}

// runFunc adapts a function to transport.Transport (Run only).
type runFunc func(ctx context.Context, cmd string) (transport.Result, error)

func (f runFunc) Run(ctx context.Context, cmd string) (transport.Result, error) { return f(ctx, cmd) }
func (f runFunc) Stream(context.Context, string) (transport.Stream, error) {
	return nil, errors.New("no")
}
func (f runFunc) Forward(context.Context, []transport.Forward) (transport.Tunnel, error) {
	return nil, errors.New("no")
}

// runScript runs a script without the login check.
func (h *psHub) runScript(ctx context.Context, cmd string) (transport.Result, error) {
	c := procx.Background(winps.Command(ctx, "-NoProfile", "-NonInteractive", "-Command", cmd))
	c.Env = append(os.Environ(), "ProgramData="+h.dir, "USERPROFILE="+h.dir)
	var out, errb bytes.Buffer
	c.Stdout, c.Stderr = &out, &errb
	err := c.Run()
	code := 0
	if err != nil {
		code = 1
	}
	return transport.Result{Stdout: out.String(), Stderr: errb.String(), ExitCode: code}, nil
}
