package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShizukaJiku/gitsi/internal/fault"
)

func isolate(t *testing.T) string {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	return home
}

func write(t *testing.T, home, content string) {
	t.Helper()
	os.MkdirAll(filepath.Join(home, ".gitsi"), 0o700)
	if err := os.WriteFile(filepath.Join(home, ".gitsi", "config.json"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSaveWritesSchemaVersionAndRoundTrips(t *testing.T) {
	home := isolate(t)
	c := Default()
	c.Role, c.ReposDir = RoleHub, "C:/x/repos"
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(home, ".gitsi", "config.json"))
	if !strings.Contains(string(b), `"version": 1`) {
		t.Fatalf("the file must carry its version: %s", b)
	}
	got, err := Load()
	if err != nil || got.Version != SchemaVersion || got.Role != RoleHub || got.ReposDir != "C:/x/repos" {
		t.Fatalf("%+v %v", got, err)
	}
}

// A file without "version" was not written by gitsi: it is not interpreted; the user is asked to set up again.
func TestLoadRejectsFilesWithoutVersion(t *testing.T) {
	home := isolate(t)
	write(t, home, `{"role":"edge","hub":{"host":"192.0.2.4","user":"u"}}`)
	_, err := Load()
	fe, ok := fault.As(err)
	if !ok || fe.Kind != fault.NotConfigured || !strings.Contains(fe.Hint, "gitsi setup") {
		t.Fatalf("%v", err)
	}
}

// A config in a newer format is not half-interpreted: the user is asked to update.
func TestLoadRejectsNewerSchemaWithAdvice(t *testing.T) {
	home := isolate(t)
	write(t, home, `{"version": 99, "role":"edge"}`)
	_, err := Load()
	fe, ok := fault.As(err)
	if !ok || fe.Kind != fault.NotConfigured || !strings.Contains(fe.Hint, "gitsi update") {
		t.Fatalf("%v", err)
	}
}

func TestLoadWithoutFileGivesDefaults(t *testing.T) {
	isolate(t)
	c, err := Load()
	if err != nil || c.Role != RoleEdge || c.Version != SchemaVersion || Exists() {
		t.Fatalf("%+v %v exists=%v", c, err, Exists())
	}
	c.Save()
	if !Exists() {
		t.Fatal("it must exist after saving")
	}
}

// Two gitsi processes saving at once (e.g. 'svc add' while another one changes the policy) must not leave a corrupt file: with a
// direct write, one could truncate while the other writes, leaving the end of one JSON glued to the other.
func TestConcurrentSavesNeverLeaveACorruptFile(t *testing.T) {
	isolate(t)
	long := Default()
	long.Role = RoleEdge
	for i := 0; i < 40; i++ {
		long.Services = append(long.Services, Service{Name: "service-with-a-long-name", RemotePort: 1000 + i, LocalPort: 20000 + i})
	}
	short := Default()
	short.Role = RoleEdge
	done := make(chan struct{})
	for _, c := range []*Config{long, short} {
		go func(c *Config) {
			for i := 0; i < 150; i++ {
				_ = c.Save()
			}
			done <- struct{}{}
		}(c)
	}
	stop := make(chan struct{})
	bad := make(chan error, 1)
	go func() { // a third process reads meanwhile
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := Load(); err != nil {
				select {
				case bad <- err:
				default:
				}
			}
		}
	}()
	<-done
	<-done
	close(stop)
	select {
	case err := <-bad:
		t.Fatalf("a reader saw a half-written configuration: %v", err)
	default:
	}
	if _, err := Load(); err != nil {
		t.Fatalf("the configuration was left corrupt: %v", err)
	}
	p, _ := Path()
	if m, _ := filepath.Glob(p + ".tmp*"); len(m) != 0 {
		t.Fatalf("no temporary files may remain: %v", m)
	}
}

// The values that reach ssh's arguments are checked again (config.json may be edited by hand), with the setup rules.
func TestHubValidate(t *testing.T) {
	ok := Hub{Host: "192.0.2.4", User: "user", Port: 2222, KeyFile: `C:\Users\u\.ssh\gitsi_ed25519`, Alias: "gitsi-hub"}
	if err := ok.Validate(); err != nil {
		t.Fatalf("a valid hub: %v", err)
	}
	for key, mutate := range map[string]func(h *Hub){
		"hub.host":          func(h *Hub) { h.Host = "-oProxyCommand" },
		"hub.user":          func(h *Hub) { h.User = "-x" },
		"hub.alias":         func(h *Hub) { h.Alias = "a b" },
		"hub.port":          func(h *Hub) { h.Port = 70000 },
		"hub.key_file":      func(h *Hub) { h.KeyFile = `C:\k'; calc` },
		"hub.prev_key_file": func(h *Hub) { h.PrevKeyFile = "k\nProxyCommand x" },
	} {
		h := ok
		mutate(&h)
		if err := h.Validate(); err == nil || !strings.HasPrefix(err.Error(), key+":") {
			t.Errorf("%s: %v", key, err)
		}
	}
	// optional values may be empty
	if err := (Hub{Host: "h", User: "u"}).Validate(); err != nil {
		t.Fatal(err)
	}
}

// Load does not reject an invalid value: that would block 'gitsi config set', the command that fixes it.
func TestLoadKeepsInvalidHubValuesForTheFix(t *testing.T) {
	home := isolate(t)
	write(t, home, `{"version":1,"role":"edge","hub":{"host":"-bad","user":"u"}}`)
	c, err := Load()
	if err != nil || c.Hub.Validate() == nil {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestHostAliasDefault(t *testing.T) {
	if (Hub{}).HostAlias() != "gitsi-hub" || (Hub{Alias: "other"}).HostAlias() != "other" {
		t.Fatal("default alias gitsi-hub; a configured one is kept")
	}
}
