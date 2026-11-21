package app

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/module"
)

var flagLine = regexp.MustCompile(`(?m)^\s+-([A-Za-z][A-Za-z0-9-]*)`)

// helpFlags runs a command with -h in an isolated environment (temporary profile, temporary folder, no network) and returns its
// options. ok is false when the command does not reach its FlagSet (it has no options or fails earlier in this environment).
func helpFlags(t *testing.T, c module.Command) (flags []string, ok bool) {
	flags, _, ok = helpOutput(t, c)
	return flags, ok
}

// helpOutput is helpFlags plus the usage text the FlagSet printed. It goes through helpUsage, the very code 'gitsi help <cmd>'
// uses: offline, so a command that needs the hub (or its layout) before parsing its options fails here.
func helpOutput(t *testing.T, c module.Command) (flags []string, usage string, ok bool) {
	t.Helper()
	role := config.RoleEdge
	if !c.Roles.Has(config.RoleEdge) {
		role = config.RoleHub
	}
	cfg := config.Default()
	cfg.Role = role
	cfg.Hub.Host, cfg.Hub.User = "hub.invalid", "nobody"
	usage, ok = helpUsage(c, cfg, strings.NewReader(""))
	if !ok {
		return nil, "", false
	}
	for _, m := range flagLine.FindAllStringSubmatch(usage, -1) {
		flags = append(flags, "-"+m[1])
	}
	sort.Strings(flags)
	return flags, usage, true
}

// Every command builds its options with module.NewFlagSet: a plain flag.NewFlagSet would dump the whole usage on a typo and give
// no suggestion.
func TestCommandsUseModuleNewFlagSet(t *testing.T) {
	root := filepath.Join("..", "modules")
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if strings.Contains(string(b), "flag.NewFlagSet(") {
			t.Errorf("%s: use module.NewFlagSet(name, env.Stderr)", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// An option documents its default once: a non-empty flag default next to a "(default ...)" in its text prints both, the second
// one escaped and machine-specific (`(default Downloads\gitsi) (default "C:\\Users\\...")`).
func TestHelpShowsEachDefaultOnce(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, m := range Modules() {
		for _, c := range m.Commands() {
			_, usage, ok := helpOutput(t, c)
			if !ok {
				continue
			}
			for _, l := range strings.Split(usage, "\n") {
				if strings.Count(l, "(default") > 1 {
					t.Errorf("%s: the default is shown twice: %s", strings.TrimSpace(c.Group+" "+c.Name), strings.TrimSpace(l))
				}
			}
		}
	}
}

// The options each command declares for tab completion are those of its FlagSet: none missing, none extra. A command that
// declares options must be able to show them with -h in an environment without a network.
func TestDeclaredFlagsMatchTheFlagSet(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, m := range Modules() {
		for _, c := range m.Commands() {
			name := strings.TrimSpace(c.Group + " " + c.Name)
			got, ok := helpFlags(t, c)
			want := append([]string(nil), c.Flags...)
			sort.Strings(want)
			switch {
			case !ok && len(want) > 0:
				t.Errorf("%s declares %v but -h does not reach its FlagSet", name, want)
			case ok && strings.Join(got, " ") != strings.Join(want, " "):
				t.Errorf("%s: the FlagSet has %v and Flags declares %v", name, got, want)
			}
		}
	}
}
