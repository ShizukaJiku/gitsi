package hubview

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/fault"
	"github.com/ShizukaJiku/gitsi/internal/hub"
	"github.com/ShizukaJiku/gitsi/internal/hubclient"
	"github.com/ShizukaJiku/gitsi/internal/layout"
	"github.com/ShizukaJiku/gitsi/internal/proto"
	"github.com/ShizukaJiku/gitsi/internal/transport"
)

// hubTr is a transport whose Stream serves the real protocol with the given Info (or none).
type hubTr struct{ info map[string]string }

type pipeStream struct{ net.Conn }

func (hubTr) Run(context.Context, string) (transport.Result, error) { return transport.Result{}, nil }
func (hubTr) Forward(context.Context, []transport.Forward) (transport.Tunnel, error) {
	return nil, nil
}
func (h hubTr) Stream(context.Context, string) (transport.Stream, error) {
	cl, sv := net.Pipe()
	go func() {
		hub.Serve(context.Background(), proto.NewArmorReader(sv), proto.NewArmorWriter(sv), hub.Config{Agent: "t", Info: h.info})
		sv.Close()
	}()
	return pipeStream{cl}, nil
}

type fixture struct {
	r     *Resolver
	dials int
	now   time.Time
}

func newFixture(t *testing.T, info map[string]string, mutate func(*config.Config)) *fixture {
	cfg := config.Default()
	cfg.Hub = config.Hub{Host: "192.0.2.10", User: "user"}
	if mutate != nil {
		mutate(cfg)
	}
	f := &fixture{now: time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)}
	tr := hubTr{info}
	f.r = &Resolver{
		Transport: tr, Config: cfg, CachePath: filepath.Join(t.TempDir(), "hub-layout.json"),
		Now: func() time.Time { return f.now },
		Dial: func(ctx context.Context, t transport.Transport, caps ...string) (*hubclient.Conn, error) {
			f.dials++
			return hubclient.Dial(ctx, t, caps...)
		},
	}
	return f
}

func announced() map[string]string {
	return layout.ForHub(&config.Config{ReposDir: "D:/my/repos"}, "C:/Users/hub").Info()
}

func TestLayoutIsDiscoveredFromTheHubNotFromTheEdgeConfig(t *testing.T) {
	f := newFixture(t, announced(), nil) // the edge has NO repos_dir
	l, err := f.r.Layout(context.Background())
	if err != nil || l.Repos != "D:/my/repos" || l.Binary != "C:/Users/hub/gitsi/gitsi.exe" || l.Mailbox != "C:/Users/hub/gitsi/mailbox" {
		t.Fatalf("%+v %v", l, err)
	}
}

func TestLayoutIsCachedAndExpires(t *testing.T) {
	f := newFixture(t, announced(), nil)
	ctx := context.Background()
	f.r.Layout(ctx)
	f.r.Layout(ctx)
	f.now = f.now.Add(9 * time.Minute)
	f.r.Layout(ctx)
	if f.dials != 1 {
		t.Fatalf("within the TTL the hub is not asked again: %d greetings", f.dials)
	}
	f.now = f.now.Add(2 * time.Minute) // 11 min
	f.r.Layout(ctx)
	if f.dials != 2 {
		t.Fatalf("once the TTL expired the hub is asked again: %d greetings", f.dials)
	}
}

func TestCacheIsInvalidatedWhenTheHubChanges(t *testing.T) {
	f := newFixture(t, announced(), nil)
	ctx := context.Background()
	f.r.Layout(ctx)
	f.r.Config.Hub.Host = "10.0.0.9" // another hub
	f.r.Layout(ctx)
	if f.dials != 2 {
		t.Fatalf("another host must invalidate the cache: %d", f.dials)
	}
	f.r.Config.Hub.Port = 2222
	f.r.Layout(ctx)
	if f.dials != 3 {
		t.Fatalf("another port too: %d", f.dials)
	}
}

func TestRefreshIgnoresTheCacheAndReportsHubVersion(t *testing.T) {
	info := announced()
	info["version"] = "0.1.0 (commit abc)"
	f := newFixture(t, info, nil)
	ctx := context.Background()
	f.r.Layout(ctx)
	_, ver, err := f.r.Refresh(ctx)
	if err != nil || ver != "0.1.0 (commit abc)" || f.dials != 2 {
		t.Fatalf("%q %v dials=%d", ver, err, f.dials)
	}
}

// A greeting without the hub's layout asks to publish gitsi on the hub.
func TestOldHubAsksToUpdateTheHub(t *testing.T) {
	f := newFixture(t, nil, nil)
	_, err := f.r.Layout(context.Background())
	fe, ok := fault.As(err)
	if !ok || fe.Kind != fault.HubBinary || !strings.Contains(fe.Hint, "update -publish") {
		t.Fatalf("%v", err)
	}
}

// What the hub announces is validated: a path that would end up injecting commands into PowerShell is not accepted.
func TestHostileAnnouncementIsRejectedAndNeverCached(t *testing.T) {
	info := announced()
	info["layout.repos"] = "C:/x'; calc; '"
	f := newFixture(t, info, nil)
	if _, err := f.r.Layout(context.Background()); err == nil {
		t.Fatal("a dangerous path must be rejected")
	}
	if _, err := os.Stat(f.r.CachePath); err == nil {
		t.Fatal("it must not be cached")
	}
}

// The cache lives on disk: if someone alters it, it is validated the same way and ignored.
func TestTamperedCacheIsIgnored(t *testing.T) {
	f := newFixture(t, announced(), nil)
	f.r.Layout(context.Background())
	b, _ := os.ReadFile(f.r.CachePath)
	os.WriteFile(f.r.CachePath, []byte(strings.Replace(string(b), "D:/my/repos", "C:/a'; calc; '", 1)), 0o600)
	l, err := f.r.Layout(context.Background())
	if err != nil || l.Repos != "D:/my/repos" || f.dials != 2 {
		t.Fatalf("it must discard the altered cache and ask again: %+v %v dials=%d", l, err, f.dials)
	}
	os.WriteFile(f.r.CachePath, []byte("not json"), 0o600)
	if _, err := f.r.Layout(context.Background()); err != nil {
		t.Fatalf("an unreadable cache is not an error: %v", err)
	}
}

func TestConnectionErrorsPropagateAndInvalidateWorks(t *testing.T) {
	f := newFixture(t, announced(), nil)
	f.r.Dial = func(context.Context, transport.Transport, ...string) (*hubclient.Conn, error) {
		return nil, fault.New(fault.Unreachable, "no network", "")
	}
	if _, err := f.r.Layout(context.Background()); !isKind(err, fault.Unreachable) {
		t.Fatalf("%v", err)
	}
}

func isKind(err error, k fault.Kind) bool { fe, ok := fault.As(err); return ok && fe.Kind == k }
