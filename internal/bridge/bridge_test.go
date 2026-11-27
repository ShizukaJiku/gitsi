package bridge

import (
	"strings"
	"testing"

	"github.com/ShizukaJiku/gitsi/internal/config"
)

func TestURL(t *testing.T) {
	cfg := config.Default()
	if got := URL(cfg, "C:/Users/user/repos/", "x"); got != "gitsi-hub:C:/Users/user/repos/x.git" {
		t.Fatal(got)
	}
}

func TestEnsureBareScript(t *testing.T) {
	c := EnsureBareScript("C:/Users/user/repos/x.git", "master", "user")
	for _, want := range []string{"git init --bare -b 'master' 'C:/Users/user/repos/x.git'", "icacls 'C:/Users/user/repos/x.git' /setowner 'user'", "CREATED", "EXISTS"} {
		if !strings.Contains(c, want) {
			t.Errorf("missing %q in %s", want, c)
		}
	}
}
