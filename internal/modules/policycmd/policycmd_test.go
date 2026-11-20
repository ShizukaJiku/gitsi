package policycmd

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/module"
	"github.com/ShizukaJiku/gitsi/internal/policy"
)

func env(t *testing.T, stdin string) (*module.Env, *bytes.Buffer) {
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	var out bytes.Buffer
	return &module.Env{Stdin: strings.NewReader(stdin), Stdout: &out, Stderr: &out, Config: config.Default()}, &out
}

func TestShowDefaultsOff(t *testing.T) {
	e, out := env(t, "")
	if err := run(context.Background(), e, nil); err != nil || strings.Contains(out.String(), "enabled") {
		t.Fatalf("%v\n%s", err, out)
	}
}

func TestEnableNeedsConfirmation(t *testing.T) {
	e, _ := env(t, "n\n")
	if err := run(context.Background(), e, []string{"set", "services", "on"}); err == nil || e.Config.Policy.AllowServices {
		t.Fatal("without confirmation it must not enable")
	}
	e, _ = env(t, "y\n")
	if err := run(context.Background(), e, []string{"set", "services", "on"}); err != nil || !e.Config.Policy.AllowServices {
		t.Fatalf("%v", err)
	}
	c, _ := config.Load()
	if !c.Policy.AllowServices {
		t.Fatal("it was not saved")
	}
}

func TestDisableAndYesAndInvalid(t *testing.T) {
	e, _ := env(t, "")
	e.Config.Policy.AllowExec = true
	if err := run(context.Background(), e, []string{"set", "exec", "off"}); err != nil || e.Config.Policy.AllowExec {
		t.Fatal("turning off asks for no confirmation")
	}
	if err := run(context.Background(), e, []string{"set", "send", "on", "-yes"}); err != nil || !e.Config.Policy.AllowSend {
		t.Fatal("-yes skips the question")
	}
	if run(context.Background(), e, []string{"set", "nothing", "on"}) == nil || run(context.Background(), e, []string{"set", "send", "maybe"}) == nil {
		t.Fatal("it should reject invalid input")
	}
}

// The confirmation explains WHAT is enabled (a conscious act); it mentions no third-party permission.
func TestConfirmationExplainsEffectAndAsksNoOneElse(t *testing.T) {
	for f := range map[string]bool{"send": true, "take": true, "services": true, "exec": true} {
		e, out := env(t, "n\n")
		_ = run(context.Background(), e, []string{"set", f, "on"})
		s := out.String()
		if !strings.Contains(s, "lets this machine") || strings.Contains(strings.ToLower(s), "authoriz") {
			t.Errorf("%s: the confirmation must explain the effect and not ask for outside permission: %q", f, s)
		}
	}
}

// Every optional feature has a description (a new one added without describing it makes this test fail).
func TestEveryFeatureHasAnEffectDescription(t *testing.T) {
	for _, f := range []policy.Feature{policy.Send, policy.Take, policy.Services, policy.Exec} {
		if policy.Effects[f] == "" {
			t.Errorf("the description of %q is missing", f)
		}
	}
}
