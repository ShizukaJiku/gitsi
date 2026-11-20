package fault

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// Real texts from OpenSSH for Windows and PowerShell.
func TestClassifyRealMessages(t *testing.T) {
	w := Where{Host: "192.0.2.10", KeyFile: `C:\Users\x\.ssh\k`}
	cases := []struct {
		name   string
		stderr string
		want   Kind
	}{
		{"key rejected", "user@192.0.2.10: Permission denied (publickey,password,keyboard-interactive).", Auth},
		{"password retry", "Permission denied, please try again.", Auth},
		{"timeout", "ssh: connect to host 192.0.2.10 port 22: Connection timed out", Unreachable},
		{"refused", "ssh: connect to host 192.0.2.10 port 22: Connection refused", Unreachable},
		{"no route", "ssh: connect to host 10.0.0.1 port 22: No route to host", Unreachable},
		{"network unreachable", "ssh: connect to host 10.0.0.1 port 22: Network is unreachable", Unreachable},
		{"banner", "kex_exchange_identification: Connection closed by remote host", Unreachable},
		{"windows dns", "ssh: Could not resolve hostname gitsi-hub: No such host is known.", NotConfigured},
		{"linux dns", "ssh: Could not resolve hostname x: Name or service not known", NotConfigured},
		{"host key changed", "@@@ WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED! @@@\nHost key verification failed.", HostKey},
		{"host key", "Host key verification failed.", HostKey},
		{"permissions", `Bad owner or permissions on C:\Users\x/.ssh/config`, NotConfigured},
		{"missing key", `Warning: Identity file C:\Users\x\.ssh\k not accessible: No such file or directory.`, NotConfigured},
		{"english powershell", "& : The term 'C:/Users/user/gitsi/gitsi.exe' is not recognized as the name of a cmdlet, function", HubBinary},
		{"spanish powershell", "& : El término 'C:/Users/user/gitsi/gitsi.exe' no se reconoce como nombre de un cmdlet, función", HubBinary},
	}
	for _, c := range cases {
		got := Classify(c.stderr, w)
		if got == nil || got.Kind != c.want {
			t.Errorf("%s: %q => %+v, want %v", c.name, c.stderr, got, c.want)
			continue
		}
		if got.Hint == "" || got.Err == nil {
			t.Errorf("%s: must carry a hint and the original cause: %+v", c.name, got)
		}
	}
}

func TestClassifyDoesNotInventCauses(t *testing.T) {
	for _, s := range []string{"", "   \n", "all good", "git: something odd we do not know", "exit status 1"} {
		if Classify(s, Where{}) != nil {
			t.Errorf("%q must not be classified: an invented cause would hide information", s)
		}
	}
}

// A changed host key wins over other matches: it is the case that must never be ignored.
func TestHostKeyHasPriority(t *testing.T) {
	s := "WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!\nPermission denied (publickey).\nHost key verification failed."
	if got := Classify(s, Where{}); got == nil || got.Kind != HostKey {
		t.Fatalf("%+v", got)
	}
}

func TestHintsAreConcrete(t *testing.T) {
	e := Classify("Permission denied (publickey).", Where{Host: "h", KeyFile: "KEY"})
	if !strings.Contains(e.Hint, "KEY") || !strings.Contains(e.Hint, "setup hub") {
		t.Errorf("the Auth hint must name the key and the command: %q", e.Hint)
	}
	e = Classify("Host key verification failed.", Where{Host: "192.0.2.5"})
	if !strings.Contains(e.Hint, "ssh-keygen -R 192.0.2.5") {
		t.Errorf("%q", e.Hint)
	}
}

// Exit codes are part of the contract: stable and distinct per category.
func TestExitCodesAreStableAndDistinct(t *testing.T) {
	want := map[Kind]int{Other: 1, NotConfigured: 10, Unreachable: 11, Auth: 12, HostKey: 13, HubBinary: 14, FeatureOff: 15, Integrity: 16, Timeout: 17}
	seen := map[int]Kind{}
	for k, code := range want {
		if k.ExitCode() != code {
			t.Errorf("%v: %d, want %d", k, k.ExitCode(), code)
		}
		if prev, dup := seen[code]; dup {
			t.Errorf("code %d repeated in %v and %v", code, prev, k)
		}
		seen[code] = k
	}
}

func TestAsAndWrapping(t *testing.T) {
	fe := New(Auth, "x", "y")
	wrapped := fmt.Errorf("could not run ssh: %w", fe)
	got, ok := As(wrapped)
	if !ok || got != fe {
		t.Fatal("As must see through wrapping")
	}
	if _, ok := As(errors.New("plain")); ok {
		t.Error("a plain error is not a fault")
	}
	if Format(fe) != "x\n  -> y" {
		t.Errorf("%q", Format(fe))
	}
	cause := Classify("Connection refused", Where{Host: "h"})
	if !strings.Contains(Format(cause), "[Connection refused]") {
		t.Errorf("Format must keep the original cause: %q", Format(cause))
	}
}

// ssh prints both "Identity file ... not accessible" and "Permission denied": the real cause is the missing key.
func TestMissingKeyBeatsPermissionDenied(t *testing.T) {
	stderr := strings.Join([]string{
		"Warning: Permanently added '127.0.0.1' (ED25519) to the list of known hosts.",
		"Warning: Identity file C:/does/not/exist/key not accessible: No such file or directory.",
		"user@127.0.0.1: Permission denied (publickey).",
	}, string(rune(10)))
	got := Classify(stderr, Where{KeyFile: "C:/does/not/exist/key"})
	if got == nil || got.Kind != NotConfigured || !strings.Contains(got.Msg, "key") {
		t.Fatalf("%+v", got)
	}
	if strings.Contains(Format(got), "Permanently added") {
		t.Errorf("ssh noise must not show up as the cause: %s", Format(got))
	}
}
