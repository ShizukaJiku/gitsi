package drop

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/archive"
	"github.com/ShizukaJiku/gitsi/internal/cleanup"
	"github.com/ShizukaJiku/gitsi/internal/clipboard"
	"github.com/ShizukaJiku/gitsi/internal/layout"
	"github.com/ShizukaJiku/gitsi/internal/mailbox"
	"github.com/ShizukaJiku/gitsi/internal/module"
)

// hubTake is 'gitsi take' on the hub: it takes out of the inbox what the edge sent, with the same options as on the edge. Plain
// (nothing else) it brings the most recent one to the clipboard.
func (m mod) hubTake(ctx context.Context, env *module.Env, args []string) error {
	hubCleanup(env)
	fs := module.NewFlagSet("take", env.Stderr)
	dir := fs.String("dir", "", `target folder (default Downloads\gitsi)`)
	keep := fs.Bool("keep", false, "do not take the item out of the inbox after receiving it")
	list := fs.Bool("list", false, "only list what is in the inbox")
	last := fs.Bool("last", false, "only the inbox's most recent item (without typing its id)")
	toClip := fs.Bool("clip", false, "copy to the clipboard instead of saving (text as text; a file or folder to paste); a single item")
	show := fs.Bool("show", false, "show a text without taking it out of the inbox: take -show <n|id>")
	rm := fs.Bool("rm", false, "discard without receiving: take -rm <n|id>...")
	ids, err := module.ParseInterleaved(fs, args)
	if err != nil {
		return err
	}
	if len(args) == 0 {
		*last, *toClip = true, true
	}
	if *dir == "" { // as on the edge: not the flag's default, so -h does not print it twice
		*dir = cleanup.ReceiveDir()
	}
	if *last && len(ids) > 0 {
		return errors.New("-last does not combine with ids: choose one of the two")
	}
	st := store(env.Config)
	items, err := st.List(mailbox.Inbox)
	if err != nil {
		return err
	}
	switch {
	case *show:
		it, err := pickOne(items, ids)
		if err != nil {
			return err
		}
		return showItem(env, it, localFetch(st, it))
	case *rm:
		return discard(env, items, ids, func(it mailbox.Item) error { return st.Remove(mailbox.Inbox, it.ID) })
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
		fmt.Fprintln(env.Stdout, "There is nothing in the inbox (what the edge sends with 'gitsi send').")
		return nil
	}
	if *last {
		items = []mailbox.Item{newest(items)}
	}
	if *toClip {
		if len(items) != 1 {
			return fmt.Errorf("-clip copies a single item and there are %d in the inbox: use -last or give its id", len(items))
		}
		it := items[0]
		return m.takeToClipboard(ctx, env, it, *keep, localFetch(st, it), func() error { return st.Remove(mailbox.Inbox, it.ID) })
	}
	if err := os.MkdirAll(*dir, 0o755); err != nil {
		return err
	}
	for _, it := range items {
		dest, err := fetchAndPlace(env, it, *dir, localFetch(st, it))
		if err != nil {
			return fmt.Errorf("%s: %w", it.Name, err)
		}
		if it.Kind != mailbox.Text {
			fmt.Fprintf(env.Stdout, "Received: %s -> %s\n", it.Name, dest)
		}
		if !*keep {
			if err := st.Remove(mailbox.Inbox, it.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

// localFetch copies an item from this hub's mailbox to dir/.gitsi-<id>.part verifying its hash (what is stored may be damaged).
func localFetch(st *mailbox.Store, it mailbox.Item) fetchFunc {
	return func(dir string) (string, error) {
		src, _, err := st.Open(mailbox.Inbox, it.ID)
		if err != nil {
			return "", err
		}
		defer src.Close()
		if err := mailbox.Verify(it, src); err != nil {
			return "", fmt.Errorf("the stored content does not match its hash: %w", err)
		}
		if _, err := src.Seek(0, 0); err != nil {
			return "", err
		}
		part := filepath.Join(dir, ".gitsi-"+it.ID+".part")
		dst, err := os.Create(part)
		if err != nil {
			return "", err
		}
		if _, err := io.Copy(dst, src); err != nil {
			dst.Close()
			os.Remove(part)
			return "", err
		}
		return part, dst.Close()
	}
}

// hubSend is 'gitsi send' on the hub: it leaves content in the outbox for the edge to receive with 'gitsi take'. Plain, it sends
// the clipboard (files or folders copied in Explorer, an image or a text), as on the edge.
func (m mod) hubSend(ctx context.Context, env *module.Env, args []string) error {
	hubCleanup(env)
	fs := module.NewFlagSet("send", env.Stderr)
	text := fs.String("text", "", "text to leave; '-' reads stdin")
	name := fs.String("name", "", "name of the text item (default text.txt)")
	clip := fs.Bool("clip", false, "leave the clipboard: files or folders copied in Explorer, an image or a text. It is what plain 'gitsi send' does")
	var exclude excludeFlag
	fs.Var(&exclude, "exclude", "when leaving a folder, leave out what matches this pattern (e.g. node_modules, *.log); repeatable")
	paths, err := module.ParseInterleaved(fs, args)
	if err != nil {
		return err
	}
	if len(args) == 0 {
		*clip = true
	}
	var textPtr *string
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "text" {
			textPtr = text
		}
	})
	if *clip && (len(paths) > 0 || textPtr != nil) {
		return errors.New("-clip does not combine with paths or -text")
	}
	if len(exclude) > 0 && textPtr != nil {
		return errors.New("-exclude only applies when leaving folders, not with -text")
	}
	var srcs []*source
	if *clip {
		cc, err := m.clip(ctx)
		if err != nil {
			return err
		}
		if cc.Kind == clipboard.Files {
			paths = cc.Files
			fmt.Fprintf(env.Stdout, "From the clipboard: %d file(s) or folder(s) copied in Explorer\n", len(paths))
		} else {
			if len(exclude) > 0 {
				return errors.New("-exclude only applies when leaving folders, and the clipboard holds an image or a text")
			}
			s, err := fromClipboard(cc, time.Now())
			if err != nil {
				return err
			}
			srcs = []*source{s}
		}
	}
	if srcs == nil {
		if srcs, err = sources(paths, textPtr, *name, env.Stdin, archive.Options{Exclude: exclude, Gzip: true}); err != nil {
			return err
		}
	}
	for _, s := range srcs {
		if s.Excluded > 0 {
			fmt.Fprintf(env.Stderr, "%s: %d item(s) left out by -exclude\n", s.Name, s.Excluded)
		}
	}
	st := store(env.Config)
	for _, s := range srcs {
		func() {
			defer s.Close()
			var it mailbox.Item
			it, err = st.AddEnc(mailbox.Outbox, s.Kind, s.Enc, s.Name, newSectionReader(s))
			if err == nil {
				fmt.Fprintf(env.Stdout, "Left for the edge: %s (%s, %s)  id %s\n", it.Name, it.Kind, human(it.Size), it.ID)
			}
		}()
		if err != nil {
			return fmt.Errorf("%s: %w", s.Name, err)
		}
	}
	return nil
}

// hubCleanup applies the hub's retention (mailbox and jobs older than 14 days) at most once per hour.
func hubCleanup(env *module.Env) {
	home, _ := os.UserHomeDir()
	cleanup.HubAuto(layout.ForHub(env.Config, home), time.Now())
}

// pickOne resolves exactly one id (or prefix) among the items.
func pickOne(items []mailbox.Item, ids []string) (mailbox.Item, error) {
	if len(ids) != 1 {
		return mailbox.Item{}, errors.New("usage: gitsi take -show <n|id>")
	}
	got, err := pick(items, ids)
	if err != nil {
		return mailbox.Item{}, err
	}
	return got[0], nil
}

// showItem shows a text (verified against its hash) without taking it out of the mailbox; for a file or a folder, its details.
func showItem(env *module.Env, it mailbox.Item, fetch fetchFunc) error {
	if it.Kind != mailbox.Text {
		fmt.Fprintf(env.Stdout, "%s  %s  %s  %s  (%s)\nNot a text: receive it with  gitsi take %s [-dir D] [-keep]\n",
			it.ID, it.Kind, human(it.Size), it.Name, it.Created.Local().Format("2006-01-02 15:04"), it.ID)
		return nil
	}
	tmp, err := os.MkdirTemp("", "gitsi-show-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	part, err := fetch(tmp)
	if err != nil {
		return err
	}
	f, err := os.Open(part)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = materialize(it, f, "", env.Stdout, mailbox.DefaultMaxSize)
	return err
}

// discard discards items without receiving them. It first resolves every id: if any is missing, it removes none.
func discard(env *module.Env, items []mailbox.Item, ids []string, del func(mailbox.Item) error) error {
	if len(ids) == 0 {
		return errors.New("usage: gitsi take -rm <n|id>...")
	}
	got, err := pick(items, ids)
	if err != nil {
		return err
	}
	for _, it := range got {
		if err := del(it); err != nil {
			return err
		}
		fmt.Fprintf(env.Stdout, "Discarded: %s  %s\n", it.ID, it.Name)
	}
	return nil
}
