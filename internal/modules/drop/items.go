package drop

import (
	"errors"
	"fmt"

	"github.com/ShizukaJiku/gitsi/internal/mailbox"
	"github.com/ShizukaJiku/gitsi/internal/module"
)

// newest returns the most recent item.
func newest(items []mailbox.Item) mailbox.Item {
	n := items[0]
	for _, it := range items[1:] {
		if it.Created.After(n.Created) {
			n = it
		}
	}
	return n
}

// pick resolves each argument as a list number ("2", the one -list shows) or as an id (or a unique id prefix).
func pick(items []mailbox.Item, ids []string) ([]mailbox.Item, error) {
	var out []mailbox.Item
	for _, id := range ids {
		if n, ok := module.Index(id); ok {
			if n > len(items) {
				return nil, fmt.Errorf("there is no item number %d: there are %d (see gitsi take -list)", n, len(items))
			}
			out = append(out, items[n-1])
			continue
		}
		var hit []mailbox.Item
		for _, it := range items {
			if len(id) > 0 && len(id) <= len(it.ID) && it.ID[:len(id)] == id {
				hit = append(hit, it)
			}
		}
		switch len(hit) {
		case 0:
			return nil, fmt.Errorf("there is no item with id %q in the mailbox", id)
		case 1:
			out = append(out, hit[0])
		default:
			return nil, errors.New("the id " + id + " is ambiguous; use more characters")
		}
	}
	return out, nil
}

func printItems(env *module.Env, items []mailbox.Item) {
	if env.JSON {
		if items == nil {
			items = []mailbox.Item{}
		}
		_ = env.PrintJSON(items)
		return
	}
	if len(items) == 0 {
		fmt.Fprintln(env.Stdout, "(empty)")
		return
	}
	for i, it := range items {
		fmt.Fprintf(env.Stdout, "[%d] %-6s %-10s %s  (%s)  %s\n", i+1, it.Kind, human(it.Size), it.Name, it.Created.Local().Format("2006-01-02 15:04"), it.ID)
	}
}
