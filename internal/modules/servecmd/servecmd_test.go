package servecmd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"runtime"
	"strings"
	"testing"

	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/module"
	"github.com/ShizukaJiku/gitsi/internal/proto"
)

func TestServeStdioWritesOnlyFramesToStdout(t *testing.T) {
	var in bytes.Buffer
	proto.WriteJSON(proto.NewArmorWriter(&in), proto.KindHello, proto.Hello{Version: proto.Version, Agent: "edge"})
	var out, errb bytes.Buffer
	env := &module.Env{Stdin: &in, Stdout: &out, Stderr: &errb, Config: config.Default()}

	m := mod{}
	if err := m.run(context.Background(), env, []string{"--stdio"}); err != nil {
		t.Fatal(err)
	}
	// everything on stdout must read as frames: a single Hello from the hub
	r := proto.NewArmorReader(&out)
	f, err := proto.ReadFrame(r)
	if err != nil || f.Kind != proto.KindHello {
		t.Fatalf("%+v %v", f, err)
	}
	if _, err := proto.ReadFrame(r); err != io.EOF {
		t.Fatalf("stdout has bytes that are not frames: %v", err)
	}
}

func TestServeRequiresStdioAndIsHubOnlyAndHidden(t *testing.T) {
	env := &module.Env{Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: io.Discard, Config: config.Default()}
	m := mod{}
	if err := m.run(context.Background(), env, nil); err == nil {
		t.Fatal("without --stdio it must fail: there is no network mode")
	}
	c := m.Commands()[0]
	if c.Roles != module.Both || !c.Hidden {
		t.Fatalf("serve hidden and available on both roles: %+v", c)
	}
}

// `serve` announces the layout the hub decides (its configuration), not one asked for from the edge.
func TestServeAnnouncesTheHubLayout(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the hub's profile is a Windows one (USERPROFILE); the hub only runs on Windows")
	}
	t.Setenv("USERPROFILE", `C:\Users\hubuser`)
	for name, tc := range map[string]struct {
		cfg   func(*config.Config)
		repos string
		home  string
	}{
		"default":            {func(c *config.Config) {}, "C:/Users/hubuser/gitsi/repos", "C:/Users/hubuser/gitsi"},
		"own repos_dir":      {func(c *config.Config) { c.ReposDir = "D:/my/repos" }, "D:/my/repos", "C:/Users/hubuser/gitsi"},
		"home and repos_dir": {func(c *config.Config) { c.Home, c.ReposDir = "E:/g", "E:/r" }, "E:/r", "E:/g"},
	} {
		var in bytes.Buffer
		proto.WriteJSON(proto.NewArmorWriter(&in), proto.KindHello, proto.Hello{Version: proto.Version, Agent: "edge"})
		var out, errb bytes.Buffer
		cfg := config.Default()
		tc.cfg(cfg)
		env := &module.Env{Stdin: &in, Stdout: &out, Stderr: &errb, Config: cfg}
		m := mod{}
		if err := m.run(context.Background(), env, []string{"--stdio"}); err != nil {
			t.Fatal(err)
		}
		f, err := proto.ReadFrame(proto.NewArmorReader(&out))
		if err != nil {
			t.Fatal(err)
		}
		var h proto.Hello
		json.Unmarshal(f.Payload, &h)
		if h.Info["layout.repos"] != tc.repos || h.Info["layout.home"] != tc.home || h.Info["layout.binary"] != tc.home+"/gitsi.exe" ||
			h.Info["layout.mailbox"] != tc.home+"/mailbox" || h.Info["layout.jobs"] != tc.home+"/jobs" || h.Info["version"] == "" {
			t.Errorf("%s: %+v", name, h.Info)
		}
	}
}
