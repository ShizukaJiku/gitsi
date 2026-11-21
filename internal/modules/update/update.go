// Package update updates the edge's binary by bringing it from the hub over SSH (no internet needed).
//
// Flow: on the hub `gitsi update -publish` leaves a copy at <hub home>/gitsi.exe; on the edge `gitsi update` asks for the SHA256
// over SSH, downloads with scp, verifies and replaces the executable.
package update

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/dockercli"
	"github.com/ShizukaJiku/gitsi/internal/fsx"
	"github.com/ShizukaJiku/gitsi/internal/install"
	"github.com/ShizukaJiku/gitsi/internal/module"
	"github.com/ShizukaJiku/gitsi/internal/safe"
	"github.com/ShizukaJiku/gitsi/internal/transport"
)

type mod struct{}

// New returns the module.
func New() module.Module { return mod{} }

func (mod) Name() string { return "update" }

func (mod) Commands() []module.Command {
	return []module.Command{{
		Name:     "update",
		Summary:  "Update gitsi from the hub; on the hub, -publish publishes it",
		Roles:    module.Both,
		NeedsHub: true, // does not apply on the hub (-publish): the dispatcher only requires it on the edge
		Run:      run,
		Flags:    []string{"-check", "-publish"},
	}}
}

func run(ctx context.Context, env *module.Env, args []string) error {
	fs := module.NewFlagSet("update", env.Stderr)
	publish := fs.Bool("publish", false, "(hub) publish this binary for the edges to download")
	check := fs.Bool("check", false, "(edge) only check whether a different version exists")
	if err := fs.Parse(args); err != nil {
		return err
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if env.Config.Role == config.RoleHub {
		if !*publish {
			return errors.New("on the hub use 'gitsi update -publish'")
		}
		return publishSelf(ctx, env, self)
	}
	if *publish {
		return errors.New("-publish only applies on the hub")
	}
	f, ok := env.Transport.(transport.Fetcher)
	if !ok {
		return errors.New("the transport cannot download files")
	}
	return apply(ctx, env, f, self, *check)
}

func publishSelf(ctx context.Context, env *module.Env, self string) error {
	lay, err := env.HubLayout(ctx) // on the hub it is computed from its own configuration
	if err != nil {
		return err
	}
	dst := filepath.FromSlash(lay.Binary)
	sum, err := install.PublishSelf(self, dst)
	if err != nil {
		return err
	}
	fmt.Fprintln(env.Stdout, "Published: "+dst)
	fmt.Fprintln(env.Stdout, "SHA256:    "+sum)
	// and the official docker client, when this hub has it, for 'gitsi docker' on the edges
	if _, err := dockercli.Publish(lay.Home, env.Stdout); err != nil {
		return err
	}
	return nil
}

// remoteHashCmd asks for the published binary's SHA256 and size (PowerShell on the hub): "HASH|BYTES". The size makes it
// possible to show the download's progress.
func remoteHashCmd(remote string) string {
	return "$f = Get-Item -LiteralPath '" + remote + "'; (Get-FileHash -Algorithm SHA256 -LiteralPath $f.FullName).Hash + '|' + $f.Length"
}

// parseHashAndSize interprets remoteHashCmd's answer ("HASH|BYTES"). When it does not have that form it returns an empty hash.
func parseHashAndSize(out string) (hash string, size int64) {
	hash, rest, ok := strings.Cut(strings.TrimSpace(out), "|")
	n, err := strconv.ParseInt(strings.TrimSpace(rest), 10, 64)
	if !ok || err != nil || n <= 0 {
		return "", 0
	}
	return strings.ToLower(strings.TrimSpace(hash)), n
}

// watchSize reports to cb the size a file reaches while it downloads (scp does not report its progress without a console). The
// returned function stops watching and waits for it to end: afterwards cb is never called again.
func watchSize(path string, total int64, cb func(done, total int64)) (stop func()) {
	if cb == nil {
		return func() {}
	}
	quit, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(200 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-quit:
				return
			case <-t.C:
				if st, err := os.Stat(path); err == nil {
					cb(st.Size(), total)
				}
			}
		}
	}()
	return func() { close(quit); <-done }
}

func apply(ctx context.Context, env *module.Env, f transport.Fetcher, self string, checkOnly bool) error {
	// With -v each step goes to stderr with its relative time and the file's state: if something removes or locks the executable
	// (an antivirus), it shows at WHICH step it happens.
	t0 := time.Now()
	trace := func(format string, a ...any) {
		env.Debugf("update +%dms: "+format, append([]any{time.Since(t0).Milliseconds()}, a...)...)
	}
	lay, err := env.HubLayout(ctx) // the hub says where its published binary is
	if err != nil {
		return err
	}
	remote := lay.Binary
	if err := safe.Path("binary path", remote); err != nil {
		return err
	}
	endAsk := env.Progress().Phase("Asking the hub")
	res, err := env.RunOnHub(ctx, remoteHashCmd(remote))
	endAsk()
	if err != nil {
		return err
	}
	want, size := parseHashAndSize(res.Stdout)
	if res.ExitCode != 0 || len(want) != 64 {
		return fmt.Errorf("the hub has no binary published at %s (run 'gitsi update -publish' on the hub): %s",
			remote, strings.TrimSpace(res.Stderr))
	}
	trace("the hub publishes %s: sha256 %s, %d bytes", remote, want[:12], size)
	cur, err := install.FileSHA256(self)
	if err != nil {
		return err
	}
	trace("current executable %s: sha256 %s; %s", self, cur[:12], describeFile(self))
	if cur == want {
		fmt.Fprintln(env.Stdout, "gitsi is up to date.")
		return nil
	}
	if checkOnly {
		fmt.Fprintf(env.Stdout, "The hub has a different version (%s). Run 'gitsi update'.\n", want[:12])
		return nil
	}
	tmp := self + ".new"
	_ = os.Remove(tmp)
	trace("downloading with scp to %s", tmp)
	cb := env.Progress().Transfer("Downloading gitsi", size, "verifying")
	var endDownload func()
	if cb == nil {
		endDownload = env.Progress().Phase("Downloading gitsi") // with -q or a small file there is no percentage: it only says so if slow
	} else {
		endDownload = watchSize(tmp, size, cb)
	}
	ferr := f.Fetch(ctx, remote, tmp)
	endDownload()
	trace("scp ended (error: %v); downloaded file: %s", ferr, describeFile(tmp))
	if ferr != nil {
		_ = os.Remove(tmp)
		return ferr
	}
	if cb != nil {
		cb(size, size) // closes the progress line (watching only sees partial sizes)
	}
	if st, serr := os.Stat(tmp); size > 0 && serr == nil && st.Size() != size {
		_ = os.Remove(tmp)
		return fmt.Errorf("the download is incomplete: %d of %d bytes; nothing was replaced", st.Size(), size)
	}
	got, herr := install.FileSHA256(tmp)
	if herr != nil {
		// The file was just downloaded fine (scp ended): being unable to read it now means something removed or locked it.
		_ = os.Remove(tmp)
		return fmt.Errorf("could not read the download (%s): %w; the file disappeared or was locked right after downloading it (an antivirus?); nothing was replaced", tmp, herr)
	}
	trace("downloaded sha256 %s (expected %s)", got[:12], want[:12])
	if got != want {
		_ = os.Remove(tmp)
		return fmt.Errorf("the downloaded hash does not match (expected %s, got %s); nothing was replaced", want, got)
	}
	trace("hash correct: moving the current executable aside and putting the new one in place")
	if err := replaceExe(self, tmp); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	trace("replacement done; %s", describeFile(self))
	if err := verifyInstalled(self, want); err != nil {
		return err
	}
	if env.Verbose && postCheckDelay > 0 { // a delayed detection would remove it a moment after it is put in place
		trace("waiting %v to check the installed executable again", postCheckDelay)
		time.Sleep(postCheckDelay)
		trace("late check: %s", describeFile(self))
		if err := verifyInstalled(self, want); err != nil {
			return err
		}
	}
	fmt.Fprintf(env.Stdout, "Updated. SHA256 %s\n", want[:12])
	return nil
}

// postCheckDelay is the wait, only with -v, before the second check of the installed executable. A variable: tests do not wait.
var postCheckDelay = 2 * time.Second

// describeFile summarizes a file's state for diagnostics: it exists (size and time) or the reason it cannot be seen.
func describeFile(path string) string {
	st, err := os.Stat(path)
	if err != nil {
		return "does NOT exist or is not accessible (" + err.Error() + ")"
	}
	return fmt.Sprintf("exists, %d bytes, modified %s", st.Size(), st.ModTime().Format("15:04:05"))
}

// verifyInstalled checks that the installed executable exists and is the published one: if something changed or removed it
// right after it was put in place, it is reported now and not when the next command runs.
func verifyInstalled(self, want string) error {
	got, err := install.FileSHA256(self)
	if err != nil {
		return fmt.Errorf("after replacing, the installed executable %s cannot be read: %w (something may have removed or locked it, e.g. an antivirus)", self, err)
	}
	if got != want {
		return fmt.Errorf("after replacing, the installed executable does not match the published one (expected %s, found %s)", want[:12], got[:12])
	}
	return nil
}

// replaceExe swaps the executable in use: Windows does not allow overwriting it, but it does allow renaming it.
func replaceExe(self, newFile string) error {
	old := self + ".old"
	_ = os.Remove(old)
	if err := fsx.RenameRetry(self, old); err != nil {
		return fmt.Errorf("could not move the current executable aside: %w", err)
	}
	if err := fsx.RenameRetry(newFile, self); err != nil {
		_ = os.Rename(old, self) // undo
		return fmt.Errorf("could not put the new executable in place (the previous one was restored): %w", err)
	}
	_ = os.Remove(old) // if still in use it is removed on the next update
	return nil
}
