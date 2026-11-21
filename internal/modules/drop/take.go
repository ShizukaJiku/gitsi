package drop

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/cleanup"
	"github.com/ShizukaJiku/gitsi/internal/clipboard"
	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/mailbox"
	"github.com/ShizukaJiku/gitsi/internal/module"
	"github.com/ShizukaJiku/gitsi/internal/policy"
)

func (m mod) take(ctx context.Context, env *module.Env, args []string) error {
	if env.Config.Role == config.RoleHub {
		return m.hubTake(ctx, env, args)
	}
	fs := module.NewFlagSet("take", env.Stderr)
	dir := fs.String("dir", "", `target folder (default Downloads\gitsi)`)
	keep := fs.Bool("keep", false, "do not remove the item from the outbox after receiving it")
	list := fs.Bool("list", false, "only list what is in the outbox")
	last := fs.Bool("last", false, "only the outbox's most recent item (without typing its id)")
	toClip := fs.Bool("clip", false, "copy to the clipboard instead of saving (text as text; a file or folder to paste); a single item")
	show := fs.Bool("show", false, "show a text without taking it out of the mailbox: take -show <n|id>")
	rm := fs.Bool("rm", false, "discard without receiving: take -rm <n|id>...")
	ids, err := module.ParseInterleaved(fs, args)
	if err != nil {
		return err
	}
	if len(args) == 0 {
		*last, *toClip = true, true // plain 'gitsi take' brings the latest to the clipboard
	}
	if *dir == "" { // resolved here and not as the flag's default, which -h would print as a second, escaped "(default ...)"
		*dir = cleanup.ReceiveDir()
	}
	if *last && len(ids) > 0 {
		return errors.New("-last does not combine with ids: choose one of the two")
	}
	sess, err := m.open(ctx, env)
	if err != nil {
		return err
	}
	defer sess.Close()
	c := mailbox.NewClient(sess.RW)

	items, err := c.List()
	if err != nil {
		return err
	}
	switch {
	case *show:
		it, err := pickOne(items, ids)
		if err != nil {
			return err
		}
		return showItem(env, it, func(dir string) (string, error) { return fetchVerified(ctx, c, env, it, dir) })
	case *rm:
		return discard(env, items, ids, func(it mailbox.Item) error { return c.Delete(it.ID) })
	}
	if *list {
		printItems(env, items)
		return nil
	}
	if len(ids) > 0 {
		if items, err = pick(items, ids); err != nil {
			return err
		}
	}
	if len(items) == 0 {
		fmt.Fprintln(env.Stdout, "There is nothing in the hub's outbox.")
		return nil
	}
	if *last {
		items = []mailbox.Item{newest(items)}
	}
	if *toClip {
		if len(items) != 1 {
			return fmt.Errorf("-clip copies a single item and there are %d in the outbox: use -last or give its id", len(items))
		}
		it := items[0]
		return m.takeToClipboard(ctx, env, it, *keep,
			func(dir string) (string, error) { return fetchVerified(ctx, c, env, it, dir) },
			func() error { return c.Delete(it.ID) })
	}
	if err := os.MkdirAll(*dir, 0o755); err != nil {
		return err
	}
	cleanup.Parts(*dir, time.Now(), false) // partial downloads abandoned more than 7 days ago (recent ones are resumed)
	failed := 0
	for _, it := range items {
		dest, err := fetchOne(ctx, c, env, it, *dir)
		if err != nil {
			failed++
			fmt.Fprintf(env.Stderr, "%s (%s): %v\n", it.Name, it.ID[:8], err)
			continue
		}
		_ = policy.Audit(env.Config.Policy, "take", fmt.Sprintf("kind=%s name=%s size=%d id=%s", it.Kind, it.Name, it.Size, it.ID))
		if it.Kind != mailbox.Text {
			fmt.Fprintf(env.Stdout, "Received: %s -> %s\n", it.Name, dest)
		}
		if !*keep {
			if err := c.Delete(it.ID); err != nil {
				fmt.Fprintf(env.Stderr, "could not remove %s from the outbox: %v\n", it.Name, err)
			}
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d item(s) were not received; they stay in the outbox", failed)
	}
	return nil
}

// maxClip caps the text or image copied as data to the clipboard (PowerShell passes them through memory). Files and folders have
// no limit: their path goes to the clipboard, as when copied in Explorer.
const maxClip = 64 << 20

// clipRoot is the folder where 'take -clip' leaves files and folders (injectable in tests).
func (m mod) clipRoot() (string, error) {
	if m.clipDir != "" {
		return m.clipDir, nil
	}
	return cleanup.ClipRoot()
}

// takeToClipboard copies a verified item to the clipboard. A text goes as text. A file or a folder is left in clipRoot/<id>/ and
// its path goes to the clipboard (it pastes into Explorer, an email, a chat...); an image file also goes as an image, to paste it
// into a chat or an image editor.
func (m mod) takeToClipboard(ctx context.Context, env *module.Env, it mailbox.Item, keep bool, fetch fetchFunc, del func() error) error {
	var content clipboard.Content
	var where string
	if it.Kind == mailbox.Text {
		if it.Size > maxClip {
			return fmt.Errorf("%s takes %s: too much text for the clipboard (at most %s); receive it without -clip", it.Name, human(it.Size), human(maxClip))
		}
		tmp, err := os.MkdirTemp("", "gitsi-clip-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(tmp)
		part, err := fetch(tmp)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(part)
		if err != nil {
			return err
		}
		content = clipboard.Content{Kind: clipboard.Text, Text: string(b)}
	} else {
		root, err := m.clipRoot()
		if err != nil {
			return err
		}
		cleanup.Clips(root, time.Now(), false) // copies older than 7 days
		dir := filepath.Join(root, it.ID)
		_ = os.RemoveAll(dir) // receiving the same item again replaces it
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		dest, err := fetchAndPlace(env, it, dir, fetch)
		if err != nil {
			return err
		}
		content.Files = []string{dest}
		where = dest
		if it.Kind == mailbox.File && isImage(it.Name) && it.Size <= maxClip {
			if b, err := os.ReadFile(dest); err == nil {
				content.PNG = b
			}
		}
	}
	if err := m.toClip(ctx, content); err != nil {
		return err
	}
	_ = policy.Audit(env.Config.Policy, "take", fmt.Sprintf("kind=%s name=%s size=%d id=%s clip=1", it.Kind, it.Name, it.Size, it.ID))
	switch {
	case it.Kind == mailbox.Text:
		fmt.Fprintf(env.Stdout, "Copied to the clipboard: %s (%s). Paste with Ctrl+V.\n", it.Name, human(it.Size))
	case len(content.PNG) > 0:
		fmt.Fprintf(env.Stdout, "Copied to the clipboard: %s (image and file, %s). Paste with Ctrl+V.\n  Saved in %s\n", it.Name, human(it.Size), where)
	default:
		fmt.Fprintf(env.Stdout, "Copied to the clipboard: %s (%s, %s). Paste with Ctrl+V in Explorer, an email or a chat.\n  Saved in %s\n", it.Name, it.Kind, human(it.Size), where)
	}
	if !keep {
		if err := del(); err != nil {
			fmt.Fprintf(env.Stderr, "could not take %s out of the mailbox: %v\n", it.Name, err)
		}
	}
	return nil
}

func isImage(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png", ".jpg", ".jpeg", ".bmp", ".gif":
		return true
	}
	return false
}

// fetchFunc leaves the verified item in dir/.gitsi-<id>.part and returns that path. On the edge it downloads it from the hub
// (fetchVerified); on the hub it copies it from its own mailbox (localFetch).
type fetchFunc func(dir string) (string, error)

// fetchOne downloads (resuming a partial), verifies the hash and puts the item in place.
func fetchOne(ctx context.Context, c *mailbox.Client, env *module.Env, it mailbox.Item, dir string) (string, error) {
	return fetchAndPlace(env, it, dir, func(d string) (string, error) { return fetchVerified(ctx, c, env, it, d) })
}

// fetchAndPlace gets the verified item and leaves it in dir under its name (a folder is extracted whole).
func fetchAndPlace(env *module.Env, it mailbox.Item, dir string, fetch fetchFunc) (string, error) {
	part, err := fetch(dir)
	if err != nil {
		return "", err
	}
	r, err := os.Open(part)
	if err != nil {
		return "", err
	}
	endSave := env.Progress().Phase("Extracting " + it.Name)
	dest, err := materialize(it, r, dir, env.Stdout, mailbox.DefaultMaxSize)
	endSave()
	r.Close()
	if err != nil {
		return "", err
	}
	os.Remove(part)
	return dest, nil
}

// fetchVerified downloads the item to dir/.gitsi-<id>.part (resuming a partial) and verifies its hash. It returns the verified
// file's path; on a mismatch, it removes it.
func fetchVerified(ctx context.Context, c *mailbox.Client, env *module.Env, it mailbox.Item, dir string) (string, error) {
	part := filepath.Join(dir, ".gitsi-"+it.ID+".part")
	var offset int64
	if st, err := os.Stat(part); err == nil && st.Size() <= it.Size {
		offset = st.Size()
	}
	// Windows does not allow truncating a file opened with O_APPEND: from scratch it opens with O_TRUNC, when resuming with O_APPEND.
	flags := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	if offset > 0 {
		flags = os.O_WRONLY | os.O_APPEND
	}
	f, err := os.OpenFile(part, flags, 0o600)
	if err != nil {
		return "", err
	}
	_, gerr := c.Get(ctx, it.ID, offset, f, transfer(env, it.Name, it.Size, "verifying"))
	// Close flushes the file: its failure (a full disk) is the real cause, not the hash mismatch the verification would report
	if cerr := f.Close(); gerr == nil {
		gerr = cerr
	}
	if gerr != nil {
		return "", fmt.Errorf("%w (what was downloaded stays to resume)", gerr)
	}
	r, err := os.Open(part)
	if err != nil {
		return "", err
	}
	defer r.Close()
	endVerify := env.Progress().Phase("Verifying " + it.Name)
	verr := mailbox.Verify(it, r)
	endVerify()
	if err := verr; err != nil {
		r.Close()
		os.Remove(part)
		return "", fmt.Errorf("verification failed, the download was discarded: %w", err)
	}
	return part, nil
}

// TakeByID brings item id from the hub's outbox to dir and takes it out of the outbox, like 'gitsi take <id> -dir <dir>'. 'run
// -repo -get' uses it (it gets it as a function from app, so no module depends on another). It returns where it ended up.
func TakeByID(ctx context.Context, env *module.Env, id, dir string) (string, error) {
	return mod{open: sshOpen}.takeByID(ctx, env, id, dir)
}

func (m mod) takeByID(ctx context.Context, env *module.Env, id, dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	sess, err := m.open(ctx, env)
	if err != nil {
		return "", err
	}
	defer sess.Close()
	c := mailbox.NewClient(sess.RW)
	items, err := c.List()
	if err != nil {
		return "", err
	}
	got, err := pick(items, []string{id})
	if err != nil {
		return "", err
	}
	it := got[0]
	dest, err := fetchOne(ctx, c, env, it, dir)
	if err != nil {
		return "", err
	}
	_ = policy.Audit(env.Config.Policy, "take", fmt.Sprintf("kind=%s name=%s size=%d id=%s", it.Kind, it.Name, it.Size, it.ID))
	if err := c.Delete(it.ID); err != nil {
		fmt.Fprintf(env.Stderr, "could not remove %s from the outbox: %v\n", it.Name, err)
	}
	return dest, nil
}
