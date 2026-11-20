package gitx

import (
	"context"
	"errors"
	"testing"
)

func fake(out string, err error) Runner {
	return func(context.Context, string, ...string) (string, string, error) { return out, "", err }
}

func TestCurrentBranch(t *testing.T) {
	g := &Git{Run: fake("master\n", nil)}
	if b := g.CurrentBranch(context.Background()); b != "master" {
		t.Fatalf("branch = %q", b)
	}
	// repo without commits or detached HEAD: empty output -> main
	g = &Git{Run: fake("", nil)}
	if b := g.CurrentBranch(context.Background()); b != "main" {
		t.Fatalf("default branch = %q", b)
	}
	g = &Git{Run: fake("", errors.New("boom"))}
	if b := g.CurrentBranch(context.Background()); b != "main" {
		t.Fatalf("branch on error = %q", b)
	}
}

func TestHasRemote(t *testing.T) {
	if !(&Git{Run: fake("url", nil)}).HasRemote(context.Background(), "hub") {
		t.Fatal("it must exist")
	}
	if (&Git{Run: fake("", errors.New("no such remote"))}).HasRemote(context.Background(), "hub") {
		t.Fatal("it must not exist")
	}
}
