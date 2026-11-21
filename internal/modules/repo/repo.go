// Package repo is the Git repository bridge between edge and hub.
//
// Model: the hub keeps one bare repo per project in ReposDir. The edge sees it as the "hub" remote (config.RemoteName) and
// NEVER changes its branches' upstream: origin stays the target repo.
package repo

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/ShizukaJiku/gitsi/internal/module"
)

type mod struct{}

// New returns the module.
func New() module.Module { return mod{} }

func (mod) Name() string { return "repo" }

func (mod) Commands() []module.Command {
	return []module.Command{
		{Group: "repo", Shortcut: true, Name: "share", Summary: "Share the current repo with the hub", Roles: module.Edge, NeedsHub: true, Run: share, Flags: []string{"-branch", "-name", "-no-push", "-remote", "-yes"}},
		{Group: "repo", Name: "get", Summary: "Get a repo from the bridge into a working folder", Roles: module.Both, NeedsHub: true, Run: get, Complete: completeRepos, Flags: []string{"-dest"}},
		{Group: "repo", Shortcut: true, Name: "sync", Summary: "Pull and push the current branch (no silent merges)", Roles: module.Both, Run: syncCmd, Flags: []string{"-remote"}},
		{Group: "repo", Shortcut: true, Name: "publish", Summary: "Push the current branch to origin to open the PR", Roles: module.Edge, Run: publish, Flags: []string{"-remote"}},
		{Group: "repo", Name: "list", Summary: "List the bridge's repos", Roles: module.Both, NeedsHub: true, Run: list},
		{Group: "repo", Name: "status", Summary: "What is left to send or receive (bridge and origin)", Roles: module.Both, Run: repoStatus, Flags: []string{"-fetch"}},
	}
}

// splitName splits a first positional argument (without a dash) from the rest, to accept `gitsi repo get <repo> -dest x`.
func splitName(args []string) (string, []string) {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		return args[0], args[1:]
	}
	return "", args
}

func newFlags(name string, w io.Writer) *flag.FlagSet {
	fs := module.NewFlagSet(name, w)
	return fs
}

// confirm asks y/N on stdin.
func confirm(in io.Reader, out io.Writer, question string) bool {
	if in == nil {
		return false
	}
	fmt.Fprintf(out, "%s (y/N) ", question)
	line, _ := bufio.NewReader(in).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}
