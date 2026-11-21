package module

import (
	"strings"
	"testing"
)

func TestSuggest(t *testing.T) {
	cmds := []string{"status", "share", "sync", "send", "take", "publish", "repo", "svc", "job", "setup", "check", "config", "run"}
	for in, want := range map[string]string{
		"stauts":  "status", // swapped neighbours: one edit
		"statsu":  "status",
		"tak":     "take",
		"tkae":    "take",
		"pubish":  "publish",
		"confg":   "config",
		"chek":    "check",
		"STAUTS":  "status", // case does not matter
		"xyzzy":   "",       // nothing close
		"ab":      "",       // too short to guess: 2 edits would match almost anything
		"status":  "",       // an exact name is not a typo
		"setupp":  "setup",
		"reop":    "repo",
		"qwertyu": "",
	} {
		if got := strings.Join(Suggest(in, cmds), " "); got != want {
			t.Errorf("Suggest(%q) = %q, want %q", in, got, want)
		}
	}
	// equally close candidates are all proposed, sorted, whatever their order: guessing one would mislead
	for _, c := range [][]string{{"list", "last", "keep", "dir"}, {"keep", "last", "dir", "list"}} {
		if got := strings.Join(Suggest("lst", c), " "); got != "last list" {
			t.Errorf("ties: %q", got)
		}
	}
	// a closer candidate wins over farther ones; repeated candidates appear once
	if got := strings.Join(Suggest("lisst", []string{"list", "last", "list"}), " "); got != "list" {
		t.Errorf("%q", got)
	}
}

func TestDidYouMean(t *testing.T) {
	if got := DidYouMean("-", []string{"last", "list"}); got != "did you mean -last or -list?" {
		t.Error(got)
	}
	if got := DidYouMean("gitsi ", []string{"status"}); got != "did you mean gitsi status?" {
		t.Error(got)
	}
	if DidYouMean("-", nil) != "" {
		t.Error("nothing to propose: empty")
	}
}

func TestDistance(t *testing.T) {
	for _, c := range []struct {
		a, b string
		d    int
	}{{"", "", 0}, {"a", "", 1}, {"abc", "abc", 0}, {"abc", "acb", 1}, {"kitten", "sitting", 3}, {"stauts", "status", 1}} {
		if got := distance(c.a, c.b); got != c.d {
			t.Errorf("distance(%q, %q) = %d, want %d", c.a, c.b, got, c.d)
		}
	}
}
