package drop

import (
	"context"
	"fmt"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/mailbox"
	"github.com/ShizukaJiku/gitsi/internal/module"
)

// Pending summarizes what waits in this machine's mailbox, for `gitsi status`: on the edge, the hub's outbox; on the hub, its
// inbox. It only lists: it moves nothing.
func Pending(ctx context.Context, env *module.Env) (string, error) {
	return mod{open: sshOpen}.pending(ctx, env)
}

func (m mod) pending(ctx context.Context, env *module.Env) (string, error) {
	var items []mailbox.Item
	var err error
	if env.Config.Role == config.RoleHub {
		items, err = store(env.Config).List(mailbox.Inbox)
	} else {
		var sess *session
		if sess, err = m.open(ctx, env); err != nil {
			return "", err
		}
		defer sess.Close()
		items, err = mailbox.NewClient(sess.RW).List()
	}
	if err != nil {
		return "", err
	}
	switch len(items) {
	case 0:
		return "nothing to receive", nil
	case 1:
		return fmt.Sprintf("1 to receive: %s (gitsi take)", items[0].Name), nil
	}
	return fmt.Sprintf("%d to receive, the latest %s (gitsi take -list)", len(items), items[len(items)-1].Name), nil
}

// Waiting summarizes, on the hub, what waits in its outbox for the edge to receive it, for `gitsi status`: how many and since
// when (something forgotten there shows up before retention removes it).
func Waiting(_ context.Context, env *module.Env) (string, error) {
	items, err := store(env.Config).List(mailbox.Outbox)
	if err != nil {
		return "", err
	}
	if len(items) == 0 {
		return "nothing waiting for the edge", nil
	}
	oldest := items[0] // List sorts oldest first
	when := ageText(time.Since(oldest.Created))
	if len(items) == 1 {
		return fmt.Sprintf("1 waiting for the edge: %s, %s (on the edge: gitsi take)", oldest.Name, when), nil
	}
	return fmt.Sprintf("%d waiting for the edge; the oldest, %s, %s (gitsi take -list on the edge)", len(items), oldest.Name, when), nil
}

// ageText tells an item's age at a glance.
func ageText(d time.Duration) string {
	switch {
	case d < time.Hour:
		return "just arrived"
	case d < 24*time.Hour:
		return fmt.Sprintf("%d h old", int(d.Hours()))
	case d < 48*time.Hour:
		return "1 day old"
	}
	return fmt.Sprintf("%d days old", int(d.Hours()/24))
}
