package watch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/fsx"
	"github.com/ShizukaJiku/gitsi/internal/proto"
)

// maxSeen caps how many ids the edge remembers (outbox ids disappear once received; there is no need to keep them all).
const maxSeen = 500

// state is what the edge remembers between sessions (~/.gitsi/watch-state.json) to tell the hub what it already knows.
type state struct {
	Announced string   `json:"announced"`
	Outbox    []string `json:"outbox"`
	Jobs      []string `json:"jobs"`
	Tried     string   `json:"tried,omitempty"` // last version (hash) it tried to install on its own: once per version
	// Heads are the bridge branch tips already notified. A state without them asks for a baseline (what exists is not news).
	Heads map[string]string `json:"heads"`
}

// ErrRestart is returned by Run when a reaction asks to restart watch (a new gitsi version was installed).
var ErrRestart = errors.New("watch must restart with the new binary")

// Client is the edge side of watch: it opens the session, listens and reconnects on its own.
type Client struct {
	// Dial opens a negotiated session with the hub (with the Cap capability). close ends it.
	Dial func(ctx context.Context) (rw io.ReadWriter, close func(), err error)
	// Fatal says whether a Dial error cannot be fixed by retrying (configuration, key, hub without watch): it stops.
	Fatal     func(error) bool
	Identity  Request // Edge, User, Version, SHA256, Reactions; the client sets the rest
	StatePath string
	Out       io.Writer
	Audit     func(action, detail string)
	Now       func() time.Time
	Dead      time.Duration   // with no news from the hub for this long, the session is considered dead (75 s)
	Backoff   []time.Duration // waits between retries (5 s ... 5 min)
	// Stable: a session that lasted at least this long starts the wait sequence over (1 min)
	Stable time.Duration
	// React runs the automatic reactions (new version, outbox item, changed branch) after showing the notice. It returns true when
	// watch must restart. nil: notify only. A new version is tried only once, even if it fails.
	React func(ctx context.Context, e Event) (restart bool)
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Client) printf(format string, a ...any) {
	fmt.Fprintf(c.Out, "[%s] %s\n", c.now().Format("15:04:05"), fmt.Sprintf(format, a...))
}

func (c *Client) audit(action, detail string) {
	if c.Audit != nil {
		c.Audit(action, detail)
	}
}

// Run listens until the context is cancelled (Ctrl+C). It returns an error only when retrying makes no sense.
func (c *Client) Run(ctx context.Context) error {
	backoff := c.Backoff
	if len(backoff) == 0 {
		backoff = []time.Duration{5 * time.Second, 10 * time.Second, 30 * time.Second, time.Minute, 5 * time.Minute}
	}
	stable := c.Stable
	if stable <= 0 {
		stable = time.Minute
	}
	attempt := 0
	for {
		start := c.now()
		err := c.session(ctx)
		if errors.Is(err, ErrRestart) {
			return err
		}
		if ctx.Err() != nil {
			c.printf("watch stopped.")
			return nil
		}
		if c.Fatal != nil && c.Fatal(err) {
			return err
		}
		if c.now().Sub(start) >= stable {
			attempt = 0
		}
		wait := backoff[min(attempt, len(backoff)-1)]
		attempt++
		c.printf("no connection to the hub: %v. Retrying in %s.", err, wait)
		c.audit("watch.disconnect", err.Error())
		select {
		case <-ctx.Done():
			c.printf("watch stopped.")
			return nil
		case <-time.After(wait):
		}
	}
}

type frameOrErr struct {
	f   proto.Frame
	err error
}

func (c *Client) session(ctx context.Context) error {
	rw, closeFn, err := c.Dial(ctx)
	if err != nil {
		return err
	}
	defer closeFn()

	st, first := c.load()
	req := c.Identity
	req.Announced, req.Outbox, req.Jobs, req.Heads, req.First = st.Announced, st.Outbox, st.Jobs, st.Heads, first
	if err := proto.WriteJSON(rw, proto.KindWatch, req); err != nil {
		return err
	}

	frames := make(chan frameOrErr, 1)
	go func() {
		for {
			f, err := proto.ReadFrame(rw)
			frames <- frameOrErr{f, err}
			if err != nil {
				return
			}
		}
	}()
	dead := c.Dead
	if dead <= 0 {
		dead = 75 * time.Second
	}
	timer := time.NewTimer(dead)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return fmt.Errorf("no news from the hub in %s", dead)
		case fe := <-frames:
			if fe.err != nil {
				if errors.Is(fe.err, io.EOF) {
					return errors.New("the hub closed the session")
				}
				return fe.err
			}
			if !timer.Stop() {
				<-timer.C
			}
			timer.Reset(dead)
			switch fe.f.Kind {
			case proto.KindPing:
			case proto.KindEvent:
				var e Event
				if json.Unmarshal(fe.f.Payload, &e) != nil || !e.Valid() {
					continue // unknown type or odd fields: ignored
				}
				if c.handle(ctx, e, &st) {
					return ErrRestart
				}
			case proto.KindError:
				return &proto.RemoteError{Message: string(fe.f.Payload)}
			}
		}
	}
}

// handle shows the notice, saves the state and runs its reaction if any. It returns true when watch must restart.
func (c *Client) handle(ctx context.Context, e Event, st *state) bool {
	switch e.Type {
	case EvHello:
		c.printf("listening to the hub (gitsi %s). Ctrl+C to quit.", Printable(e.Version, 80))
		c.audit("watch.connect", e.Version)
		return false
	case EvBaseline:
		st.Outbox, st.Jobs, st.Heads = e.Outbox, e.Jobs, e.Heads
		if st.Heads == nil {
			st.Heads = map[string]string{}
		}
	case EvUpdate:
		ver := e.Version
		if ver == "" {
			ver = e.SHA256[:12]
		}
		c.printf("a new gitsi version is on the hub: %s. To install it: gitsi update", Printable(ver, 80))
		st.Announced = e.SHA256
	case EvOutbox:
		c.printf("arrived in the outbox: %s (%s, %d bytes). To receive it: gitsi take %s", Printable(e.Name, 120), e.Kind, e.Size, e.ID)
		st.Outbox = appendCapped(st.Outbox, e.ID)
	case EvJobDone:
		c.printf("job %s finished (exit code %s). Output: gitsi job logs %s", e.ID, e.Exit, e.ID)
		st.Jobs = appendCapped(st.Jobs, e.ID)
	case EvRepo:
		c.printf("repo %s changed: branch %s is at %s. In the repo: gitsi sync", e.Repo, e.Branch, e.Commit[:12])
		if st.Heads == nil {
			st.Heads = map[string]string{}
		}
		st.Heads[HeadKey(e.Repo, e.Branch)] = e.Commit
	}
	c.audit("watch.event", e.Type+" "+e.ID+e.SHA256)
	react := c.React != nil && (e.Type == EvOutbox || e.Type == EvRepo || (e.Type == EvUpdate && st.Tried != e.SHA256))
	if e.Type == EvUpdate && react {
		st.Tried = e.SHA256 // before trying: if it fails (or an antivirus removes the binary) it is not retried on its own
	}
	c.save(*st)
	if react {
		return c.React(ctx, e)
	}
	return false
}

func appendCapped(ids []string, id string) []string {
	ids = append(ids, id)
	if len(ids) > maxSeen {
		ids = ids[len(ids)-maxSeen:]
	}
	return ids
}

func (c *Client) load() (state, bool) {
	var st state
	b, err := fsx.ReadFileRetry(c.StatePath)
	if err != nil || json.Unmarshal(b, &st) != nil {
		return state{}, true // no state (or unreadable): ask for a baseline
	}
	return st, st.Heads == nil // a state without branch tips also asks for a baseline: existing branches are not news
}

func (c *Client) save(st state) {
	if c.StatePath == "" {
		return
	}
	b, _ := json.MarshalIndent(st, "", "  ")
	if err := fsx.WriteFileAtomic(c.StatePath, b, 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not save watch's state: %v\n", err)
	}
}
