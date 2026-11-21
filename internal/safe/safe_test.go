package safe

import (
	"strings"
	"testing"
)

func TestName(t *testing.T) {
	for _, ok := range []string{"my-service-api", "repo_1", "a.b"} {
		if err := Name("n", ok); err != nil {
			t.Errorf("%q must be valid: %v", ok, err)
		}
	}
	// a leading dash would turn the ssh target ("-oX@host") into an option
	for _, bad := range []string{"", "a b", "x';calc", "..", ".", "a/b", "a$b", "a\"b", "-x", "-oProxyCommand", "--"} {
		if Name("n", bad) == nil {
			t.Errorf("%q must be invalid", bad)
		}
	}
}

func TestHost(t *testing.T) {
	for _, ok := range []string{"192.168.1.7", "hub-pc", "hub.lan", "fe80::1", "::1", "my_host"} {
		if err := Host(ok); err != nil {
			t.Errorf("%q must be valid: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "-oProxyCommand", "-x", "a b", "a;b", "a'b", "a=b", strings.Repeat("h", 254)} {
		if Host(bad) == nil {
			t.Errorf("%q must be invalid", bad)
		}
	}
}

func TestBranchAndPath(t *testing.T) {
	if Branch("feature/x-1") != nil || Branch("a..b") == nil || Branch("a;b") == nil {
		t.Error("wrong branch validation")
	}
	// 8.3 short names (C:/Users/RUNNER~1) are common in TEMP and profile paths: the tilde is literal inside the path
	for _, ok := range []string{"C:/Users/user/repos", "C:/Users/RUNNER~1/AppData/Local/Temp/x", "C:/PROGRA~1/gitsi"} {
		if err := Path("p", ok); err != nil {
			t.Errorf("valid path rejected: %v", err)
		}
	}
	// a leading tilde is PowerShell's home folder, not a literal path
	for _, bad := range []string{"C:/a'b", "C:/a;b", "C:/../x", "C:/a$b", "~/x", "~"} {
		if Path("p", bad) == nil {
			t.Errorf("%q must be invalid", bad)
		}
	}
}

func TestFileName(t *testing.T) {
	for _, ok := range []string{"report.pdf", "my folder", "text", "a-b_c.tar"} {
		if err := FileName(ok); err != nil {
			t.Errorf("%q should be valid: %v", ok, err)
		}
	}
	for _, bad := range []string{"", ".", "..", "a/b", `a\b`, "con", "NUL.txt", "a:b", "x*", "end.", "end ", "a\nb", strings.Repeat("x", 121)} {
		if FileName(bad) == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
}
