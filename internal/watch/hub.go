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
}

// Handles implements hub.Router.
func (h *Handler) Handles(k proto.Kind) bool { return k == proto.KindWatch }

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

// Handle implements hub.Handler. It does not return while the edge listens: it ends when the context is cancelled or a write fails
// (the edge left: the heartbeat detects it at most Ping later).
func (h *Handler) Handle(ctx context.Context, f proto.Frame, w io.Writer) error {
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

	poll, ping := h.Poll, h.Ping
	if poll <= 0 {
		poll = 2 * time.Second
	}
	if ping <= 0 {
		ping = 30 * time.Second
	}
	if err := check(); err != nil {
		return err
	}
	tp, tk := time.NewTicker(poll), time.NewTicker(ping)
	defer tp.Stop()
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
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
