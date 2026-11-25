package watch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/clip"
	"github.com/ShizukaJiku/gitsi/internal/fsx"
	"github.com/ShizukaJiku/gitsi/internal/proto"
)

// OutboxItem is what the hub knows about an item of its outbox.
type OutboxItem struct {
	ID, Kind, Name string
	Size           int64
}

// Source is the watched hub state. The real implementation looks at files (FSSource); tests use a fake one.
type Source interface {
	Binary() (sha256, version string, err error) // binary published for the edges
	Outbox() ([]OutboxItem, error)
	FinishedJobs() (map[string]string, error) // id -> exit code
	Heads() (map[string]string, error)        // bridge branch tips: HeadKey(repo, branch) -> commit
}

// Handler serves the Watch frame on the hub: one watch session per connected edge, alive while the edge listens.
type Handler struct {
	Source     Source
	StateDir   string        // <hub home>/watch: one file per edge (listed by 'gitsi edges')
	HubVersion string        // announced in the hello notice
	Poll       time.Duration // how often the state is checked (2 s)
	Ping       time.Duration // how often the heartbeat is sent (30 s)
	Now        func() time.Time

	// Clip is the hub's clipboard slot; nil when the hub does not share its clipboard (its clip policy is off).
	Clip     *clip.Slot
	ClipMax  int64         // the largest item the hub accepts from an edge
	ClipPoll time.Duration // how often the slot is checked (100 ms, a file date: a copy should arrive almost at once)
}

// Handles implements hub.Router.
func (h *Handler) Handles(k proto.Kind) bool { return k == proto.KindWatch }

// TakesOver implements hub.SessionHandler: a watch session lasts while the edge listens and carries its clipboard items.
func (h *Handler) TakesOver(k proto.Kind) bool { return k == proto.KindWatch }

func (h *Handler) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

// EdgeState is what the hub keeps about each listening edge (shown by 'gitsi edges').
type EdgeState struct {
	Edge      string    `json:"edge"`
	User      string    `json:"user"`
	Version   string    `json:"version"`
	Reactions []string  `json:"reactions"`
	Since     time.Time `json:"since"`
	LastPing  time.Time `json:"last_ping"`
	Connected bool      `json:"connected"`
}

// Handle implements hub.Handler: a session that does not read what the edge sends (no clipboard from the edge).
func (h *Handler) Handle(ctx context.Context, f proto.Frame, w io.Writer) error {
	return h.HandleSession(ctx, f, nil, w)
}

// inbound is what the edge sent inside the session: a clipboard item (or why it was refused), or the end of the channel.
type inbound struct {
	it   clip.Item
	data []byte
	err  error
}

// HandleSession implements hub.SessionHandler. It does not return while the edge listens: it ends when the context is cancelled,
// the edge closes the channel or a write fails. Only this goroutine writes to w: notices, heartbeats and clipboard items never
// interleave.
func (h *Handler) HandleSession(ctx context.Context, f proto.Frame, r io.Reader, w io.Writer) error {
	var req Request
	if err := json.Unmarshal(f.Payload, &req); err != nil {
		return fmt.Errorf("invalid Watch: %w", err)
	}
	if !ValidIdentity(req.Edge) || !ValidIdentity(req.User) || len(req.Version) > 80 || len(req.Reactions) > 10 {
		return errors.New("invalid Watch: invalid edge identity")
	}
	for _, r := range req.Reactions {
		if !ValidIdentity(r) {
			return errors.New("invalid Watch: invalid reaction")
		}
	}
	st := EdgeState{Edge: req.Edge, User: req.User, Version: req.Version, Reactions: req.Reactions, Since: h.now(), LastPing: h.now(), Connected: true}
	h.saveState(st)
	defer func() { st.Connected = false; h.saveState(st) }()

	send := func(e Event) error {
		e.Time = h.now()
		return proto.WriteJSON(w, proto.KindEvent, e)
	}
	if err := send(Event{Type: EvHello, Version: h.HubVersion}); err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	in := make(chan inbound, 1)
	if r != nil {
		go h.read(ctx, r, req.Clip && h.Clip != nil, in)
	}
	edgeID := req.User + "@" + req.Edge
	clipOn := req.Clip && h.Clip != nil
	since := h.now().Add(-time.Duration(max(req.ClipAgeMS, 0)) * time.Millisecond)
	lastClip := req.ClipSeen // the last slot item this session dealt with (delivered, skipped or stored from this edge)
	checkClip := func() error {
		cur, ok := h.Clip.Current(h.now())
		if !ok || cur.ID == lastClip {
			return nil
		}
		if cur.Origin == edgeID || !cur.Time.After(since) {
			lastClip = cur.ID // its own copy, or one made before this edge's watch started: not news
			return nil
		}
		if req.ClipMax > 0 && cur.Size > req.ClipMax {
			lastClip = cur.ID
			return send(Event{Type: EvClipRefused, ID: cur.ID, Detail: fmt.Sprintf("the hub's copy (%s) is larger than this edge accepts (clip.max_size_mb)", cur.Describe())})
		}
		data, err := h.Clip.Data(cur)
		if err != nil {
			return nil // replaced in the meantime: the next check sees the newer one
		}
		lastClip = cur.ID
		return clip.WriteItem(w, cur, data)
	}
	store := func(m inbound) error {
		if m.err != nil {
			return send(Event{Type: EvClipRefused, ID: m.it.ID, Detail: Printable(m.err.Error(), 280)})
		}
		m.it.Origin, m.it.Time = edgeID, h.now() // the hub decides both: an edge cannot pose as another or make an item outlive TTL
		if err := h.Clip.Put(m.it, m.data); err != nil {
			return send(Event{Type: EvClipRefused, ID: m.it.ID, Detail: Printable("the hub could not store it: "+err.Error(), 280)})
		}
		lastClip = m.it.ID
		return send(Event{Type: EvClipStored, ID: m.it.ID})
	}

	seenOutbox := set(req.Outbox)
	seenJobs := set(req.Jobs)
	seenHeads := map[string]string{}
	for k, c := range req.Heads {
		seenHeads[k] = c
	}
	announced := req.Announced
	if req.First { // first time: what already exists is not news
		items, _ := h.Source.Outbox()
		jobs, _ := h.Source.FinishedJobs()
		heads, _ := h.Source.Heads()
		b := Event{Type: EvBaseline, Heads: heads}
		for k, c := range heads {
			seenHeads[k] = c
		}
		for _, it := range items {
			b.Outbox = append(b.Outbox, it.ID)
			seenOutbox[it.ID] = true
		}
		for id := range jobs {
			b.Jobs = append(b.Jobs, id)
			seenJobs[id] = true
		}
		sort.Strings(b.Outbox)
		sort.Strings(b.Jobs)
		if err := send(b); err != nil {
			return err
		}
	}

	check := func() error {
		if sha, ver, err := h.Source.Binary(); err == nil && sha != "" && sha != req.SHA256 && sha != announced {
			if err := send(Event{Type: EvUpdate, SHA256: sha, Version: ver}); err != nil {
				return err
			}
			announced = sha
		}
		if items, err := h.Source.Outbox(); err == nil {
			for _, it := range items {
				if !seenOutbox[it.ID] {
					if err := send(Event{Type: EvOutbox, ID: it.ID, Kind: it.Kind, Name: it.Name, Size: it.Size}); err != nil {
						return err
					}
					seenOutbox[it.ID] = true
				}
			}
		}
		if jobs, err := h.Source.FinishedJobs(); err == nil {
			ids := make([]string, 0, len(jobs))
			for id := range jobs {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			for _, id := range ids {
				if !seenJobs[id] {
					if err := send(Event{Type: EvJobDone, ID: id, Exit: jobs[id]}); err != nil {
						return err
					}
					seenJobs[id] = true
				}
			}
		}
		if heads, err := h.Source.Heads(); err == nil {
			keys := make([]string, 0, len(heads))
			for k := range heads {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				if seenHeads[k] == heads[k] {
					continue
				}
				repo, branch, _ := strings.Cut(k, "/")
				if err := send(Event{Type: EvRepo, Repo: repo, Branch: branch, Commit: heads[k]}); err != nil {
					return err
				}
				seenHeads[k] = heads[k]
			}
		}
		return nil
	}

	poll, ping, clipPoll := h.Poll, h.Ping, h.ClipPoll
	if poll <= 0 {
		poll = 2 * time.Second
	}
	if ping <= 0 {
		ping = 30 * time.Second
	}
	if clipPoll <= 0 {
		clipPoll = 100 * time.Millisecond
	}
	if err := check(); err != nil {
		return err
	}
	tp, tk := time.NewTicker(poll), time.NewTicker(ping)
	defer tp.Stop()
	defer tk.Stop()
	var clipTick <-chan time.Time
	if clipOn {
		tc := time.NewTicker(clipPoll)
		defer tc.Stop()
		clipTick = tc.C
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case m := <-in:
			if m.err != nil && !errors.Is(m.err, clip.ErrRefused) {
				return nil // the edge closed the channel (it left) or broke the protocol: the session is over
			}
			if err := store(m); err != nil {
				return err
			}
		case <-clipTick:
			if err := checkClip(); err != nil {
				return err
			}
		case <-tp.C:
			if err := check(); err != nil {
				return err
			}
		case <-tk.C:
			if err := proto.WriteFrame(w, proto.Frame{Kind: proto.KindPing}); err != nil {
				return err
			}
			st.LastPing = h.now()
			h.saveState(st)
		}
	}
}

// read receives what the edge sends inside the session. Only clipboard items are expected; anything else is ignored. A refused item
// is passed on (the edge is told why) and reading goes on: ReadItem already drained its content.
func (h *Handler) read(ctx context.Context, r io.Reader, clipOn bool, in chan<- inbound) {
	limit := h.ClipMax
	if !clipOn {
		limit = -1 // nothing is accepted, but the content is still drained so the channel stays in sync
	}
	for {
		f, err := proto.ReadFrame(r)
		if err == nil && f.Kind != proto.KindClip {
			continue
		}
		var m inbound
		if err != nil {
			m.err = err
		} else {
			m.it, m.data, m.err = clip.ReadItem(f, r, limit)
			if !clipOn && errors.Is(m.err, clip.ErrRefused) {
				m.err = fmt.Errorf("%w: the hub does not share its clipboard (on the hub: gitsi policy set clip on)", clip.ErrRefused)
			}
		}
		select {
		case in <- m:
		case <-ctx.Done():
			return
		}
		if m.err != nil && !errors.Is(m.err, clip.ErrRefused) {
			return
		}
	}
}

func (h *Handler) saveState(st EdgeState) {
	if h.StateDir == "" {
		return
	}
	b, _ := json.MarshalIndent(st, "", "  ")
	_ = fsx.WriteFileAtomic(filepath.Join(h.StateDir, st.User+"@"+st.Edge+".json"), b, 0o600)
}

// Edges reads the state of the edges that have listened to this hub. One without a recent heartbeat shows as disconnected even if
// its file says otherwise (the session died without closing it).
func Edges(dir string, now time.Time, stale time.Duration) ([]EdgeState, error) {
	m, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	var out []EdgeState
	for _, p := range m {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var st EdgeState
		if json.Unmarshal(b, &st) != nil || !ValidIdentity(st.Edge) || !ValidIdentity(st.User) {
			continue
		}
		if st.Connected && now.Sub(st.LastPing) > stale {
			st.Connected = false
		}
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].User+out[i].Edge < out[j].User+out[j].Edge })
	return out, nil
}

func set(ids []string) map[string]bool {
	m := make(map[string]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	return m
}
