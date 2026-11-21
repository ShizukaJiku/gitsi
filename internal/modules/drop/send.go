package drop

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/archive"
	"github.com/ShizukaJiku/gitsi/internal/clipboard"
	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/mailbox"
	"github.com/ShizukaJiku/gitsi/internal/module"
	"github.com/ShizukaJiku/gitsi/internal/policy"
)

func (m mod) send(ctx context.Context, env *module.Env, args []string) error {
	if env.Config.Role == config.RoleHub {
		return m.hubSend(ctx, env, args)
	}
	fs := module.NewFlagSet("send", env.Stderr)
	text := fs.String("text", "", "text to send; '-' reads stdin")
	name := fs.String("name", "", "name of the text item (default text.txt)")
	clip := fs.Bool("clip", false, "send the clipboard: files or folders copied in Explorer, an image (PNG) or text. It is what plain 'gitsi send' does")
	var exclude excludeFlag
	fs.Var(&exclude, "exclude", "when sending a folder, leave out what matches this pattern (e.g. node_modules, *.log); repeatable")
	paths, err := module.ParseInterleaved(fs, args)
	if err != nil {
		return err
	}
	if len(args) == 0 {
		*clip = true // plain 'gitsi send' sends the clipboard
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
		return errors.New("-exclude only applies when sending folders, not with -text")
	}
	// The clipboard is read before connecting: when it is empty no SSH connection is spent. When it has files or folders copied in
	// Explorer, they are sent just like 'gitsi send <paths>' (packing, compression and -exclude included).
	var clipContent *clipboard.Content
	if *clip {
		cc, cerr := m.clip(ctx)
		if cerr != nil {
			return cerr
		}
		if cc.Kind == clipboard.Files {
			paths = cc.Files
			fmt.Fprintf(env.Stdout, "From the clipboard: %d file(s) or folder(s) copied in Explorer\n", len(paths))
		} else {
			if len(exclude) > 0 {
				return errors.New("-exclude only applies when sending folders, and the clipboard holds an image or a text")
			}
			clipContent = &cc
		}
	}
	// Everything that can be validated without connecting is validated first: a usage error or a typo must not cost an SSH connection.
	if clipContent == nil {
		if err := checkSendArgs(paths, textPtr, *name, exclude); err != nil {
			return err
		}
	}

	// It connects before packing: that tells whether the hub accepts compressed folders, and a connection failure does not cost a packing.
	sess, err := m.open(ctx, env)
	if err != nil {
		return err
	}
	defer sess.Close()
	c := mailbox.NewClient(sess.RW)
	c.Abort = sess.Close

	var srcs []*source
	if clipContent != nil {
		s, cerr := fromClipboard(*clipContent, time.Now())
		if cerr != nil {
			return cerr
		}
		srcs = []*source{s}
	} else {
		endPack := env.Progress().Phase("Packing and compressing") // only announced when slow: fast work does not clutter the output
		srcs, err = sources(paths, textPtr, *name, env.Stdin, archive.Options{Exclude: exclude, Gzip: true})
		endPack()
		if err != nil {
			return err
		}
	}
	defer func() {
		for _, s := range srcs {
			s.Close()
		}
	}()

	if err := checkUncompressed(srcs); err != nil { // before the first Put: a batch must not be left half done
		return err
	}
	for _, s := range srcs {
		if s.Excluded > 0 {
			fmt.Fprintf(env.Stderr, "%s: %d item(s) left out by -exclude\n", s.Name, s.Excluded)
		}
		endHash := env.Progress().Phase("Hashing " + s.Name)
		it, err := mailbox.NewItem(s.Kind, s.Name, s.R, s.Size)
		endHash()
		if err != nil {
			return err
		}
		it.Enc = s.Enc
		if _, err := c.Put(ctx, it, s.R, transfer(env, s.Name, s.Size, "verifying on the hub")); err != nil {
			return fmt.Errorf("%s: %w (the command can be repeated: it resumes)", s.Name, err)
		}
		_ = policy.Audit(env.Config.Policy, "send", fmt.Sprintf("kind=%s name=%s size=%d id=%s", it.Kind, it.Name, it.Size, it.ID))
		fmt.Fprintf(env.Stdout, "Sent: %s (%s, %s)  id %s\n", it.Name, it.Kind, human(it.Size), it.ID)
	}
	return nil
}
