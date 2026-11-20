package watchcmd

import (
	"context"
	"fmt"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/module"
	"github.com/ShizukaJiku/gitsi/internal/watch"
)

// Listening summarizes, on the hub, which edges listen to its notices, for `gitsi status`.
func Listening(_ context.Context, env *module.Env) (string, error) {
	es, err := watch.Edges(stateDir(env.Config), time.Now(), staleAfter)
	if err != nil {
		return "", err
	}
	n := 0
	for _, e := range es {
		if e.Connected {
			n++
		}
	}
	switch n {
	case 0:
		return "no edge listening (on the edge: gitsi watch)", nil
	case 1:
		return "1 edge listening (gitsi edges)", nil
	}
	return fmt.Sprintf("%d edges listening (gitsi edges)", n), nil
}
