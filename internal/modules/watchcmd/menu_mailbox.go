package watchcmd

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ShizukaJiku/gitsi/internal/cleanup"
	"github.com/ShizukaJiku/gitsi/internal/desktop"
	"github.com/ShizukaJiku/gitsi/internal/mailbox"
	"github.com/ShizukaJiku/gitsi/internal/policy"
	"github.com/ShizukaJiku/gitsi/internal/progress"
	"github.com/ShizukaJiku/gitsi/internal/watch"
)

// itemID is the shape of a mailbox id. Ids come from a child's JSON and go back as arguments: anything else (a value that would
// read as an option, say) is left out of the menu.
var itemID = regexp.MustCompile(`^[0-9a-f]{16}$`)

// refreshMailbox reads the mailbox this machine takes from (the hub's outbox on the edge, the inbox on the hub) and rebuilds the
// menu.
func (s *session) refreshMailbox(ctx context.Context) {
	if !s.tray || !s.featureOn(policy.Take) {
		return
	}
	s.mailbox.load(func() ([]mailbox.Item, string) {
		var items, ok []mailbox.Item
		if reason := listJSON(ctx, &items, "take", "-list"); reason != "" {
			return nil, reason
		}
		for _, it := range items {
			if itemID.MatchString(it.ID) {
				ok = append(ok, it)
			}
		}
		return ok, ""
	})
	s.refresh()
}

// takeLatest is the first-level shortcut to the newest item. It is disabled when the mailbox is known to be empty.
func (s *session) takeLatest(ctx context.Context) []desktop.MenuItem {
	if !s.featureOn(policy.Take) {
		return nil
	}
	list, reason, at := s.mailbox.get()
	return []desktop.MenuItem{{Label: "Take the latest to the clipboard", Icon: desktop.GlyphTake,
		Disabled: len(list) == 0 && reason == "" && !at.IsZero(),
		Action:   func() { s.take(ctx, "take the latest item", "The latest item is on the clipboard", "take") }}}
}

// mailboxMenu is the mailbox's submenu, one submenu per item.
func (s *session) mailboxMenu(ctx context.Context) desktop.MenuItem {
	title, from, to := "Mailbox", "From the hub", "the hub"
	if s.isHub() {
		title, from, to = "Inbox", "From the edge", "the edge"
	}
	send := desktop.MenuItem{Label: "Send what I copied to " + to, Icon: desktop.GlyphSend, Action: func() { s.send(ctx) }}
	if !s.featureOn(policy.Take) {
		return desktop.MenuItem{Label: title, Icon: desktop.GlyphInbox,
			Sub: append(s.offNote(ctx, policy.Take, "Receiving from the hub"), desktop.MenuItem{}, send)}
	}
	list, reason, at := s.mailbox.get()
	var sub []desktop.MenuItem
	switch {
	case at.IsZero():
		sub = append(sub, desktop.MenuItem{Label: "Reading the list...", Disabled: true})
	case reason != "":
		sub = append(sub, desktop.MenuItem{Label: "Could not read it: " + menuText(reason, 60), Disabled: true})
	case len(list) == 0:
		sub = append(sub, desktop.MenuItem{Label: from + ": nothing waiting (" + readAt(at) + ")", Disabled: true})
	default:
		sub = append(sub, desktop.MenuItem{Label: from + ", " + readAt(at), Disabled: true})
		for _, it := range list {
			sub = append(sub, s.itemMenu(ctx, it))
		}
	}
	sub = append(sub, desktop.MenuItem{}, send,
		desktop.MenuItem{Label: `Open Downloads\gitsi`, Icon: desktop.GlyphFolder, Action: func() {
			if err := openFolder(cleanup.ReceiveDir()); err != nil {
				s.notify("gitsi", "Could not open the folder: "+err.Error(), true)
			}
		}},
		desktop.MenuItem{Label: "Refresh", Icon: desktop.GlyphRefresh, Action: func() { s.refreshMailbox(ctx) }})
	badge := ""
	if reason == "" && len(list) > 0 {
		badge = fmt.Sprint(len(list))
	}
	return desktop.MenuItem{Label: title, Badge: badge, Icon: desktop.GlyphInbox, Sub: sub}
}

// itemMenu is one mailbox item: its name and size, and what can be done with it.
func (s *session) itemMenu(ctx context.Context, it mailbox.Item) desktop.MenuItem {
	name := watch.Printable(it.Name, 60)
	size, icon := progress.Bytes(it.Size), desktop.GlyphFile
	switch {
	case it.Kind == mailbox.Text:
		size, icon = "text", desktop.GlyphText
	case it.Kind == mailbox.Dir:
		size, icon = "folder, "+size, desktop.GlyphFolder
	case isImage(it.Name):
		icon = desktop.GlyphImage
	}
	sub := []desktop.MenuItem{
		{Label: "Take it to the clipboard", Icon: desktop.GlyphCopy,
			Action: func() { s.take(ctx, "take "+name, name+" is on the clipboard", "take", "-clip", it.ID) }},
		{Label: `Take it to Downloads\gitsi`, Icon: desktop.GlyphTake,
			Action: func() { s.take(ctx, "take "+name, name+` is in Downloads\gitsi`, "take", it.ID) }},
	}
	if it.Kind == mailbox.Text {
		sub = append(sub, desktop.MenuItem{Label: "View it", Icon: desktop.GlyphEye, Action: func() { s.view(ctx, it) }})
	}
	sub = append(sub, desktop.MenuItem{}, desktop.MenuItem{Label: "Discard...", Icon: desktop.GlyphTrash, Action: func() { s.discard(ctx, it) }})
	return desktop.MenuItem{Label: menuText(name, 60) + "\t" + size, Icon: icon, Sub: sub}
}

func isImage(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".bmp", ".webp":
		return true
	}
	return false
}

// take runs one take; the list is read again afterwards.
func (s *session) take(ctx context.Context, what, done string, args ...string) {
	s.act(ctx, "", what, done, args...)
	s.refreshMailbox(ctx)
}

// send puts what is on the clipboard in the mailbox (`gitsi send`), turning the send policy on first when the user agrees.
func (s *session) send(ctx context.Context) {
	if !s.featureOn(policy.Send) && !s.enableFeature(ctx, policy.Send) {
		return
	}
	s.act(ctx, "", "send what you copied", "Sent what you copied", "send")
}

func (s *session) view(ctx context.Context, it mailbox.Item) {
	actx, cancel := context.WithTimeout(ctx, listTimeout)
	out, errOut, err := runSelf(actx, "", "take", "-show", it.ID)
	cancel()
	u := s.currentUI()
	if u == nil {
		return
	}
	if err != nil {
		s.notify("gitsi: could not read "+watch.Printable(it.Name, 60), lastLine(errOut, err), true)
		return
	}
	u.Message(watch.Printable(it.Name, 60), cutText(out, "take it to the clipboard to get all of it"))
}

func (s *session) discard(ctx context.Context, it mailbox.Item) {
	name := watch.Printable(it.Name, 60)
	where := "the hub's outbox"
	if s.isHub() {
		where = "the inbox"
	}
	if !s.confirm(fmt.Sprintf("Discard %s?\n\nIt is removed from %s without being received.", name, where)) {
		return
	}
	s.take(ctx, "discard "+name, name+" was discarded", "take", "-rm", it.ID)
}
