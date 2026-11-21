package module

import (
	"bytes"
	"errors"
	"flag"
	"strings"
	"testing"
)

func newTake(w *bytes.Buffer) *flag.FlagSet {
	fs := NewFlagSet("take", w)
	fs.Bool("list", false, "only list")
	fs.Bool("keep", false, "keep it")
	fs.String("dir", "", "target folder")
	fs.Int("tail", 0, "last N lines")
	return fs
}

// A mistyped option gets one suggestion and the way to list the options, not the whole usage; -h still gets the usage.
func TestFlagSetSuggestsInsteadOfDumpingTheUsage(t *testing.T) {
	var w bytes.Buffer
	_, err := ParseInterleaved(newTake(&w), []string{"-lst"})
	out := w.String()
	if !IsUsageError(err) || !strings.Contains(out, "flag provided but not defined: -lst") || !strings.Contains(out, "did you mean -list?") &&
		!strings.Contains(out, "or -list?") ||
		!strings.Contains(out, "gitsi take -h lists its options") || strings.Contains(out, "Usage of") || strings.Contains(out, "keep it") {
		t.Fatalf("%v\n%s", err, out)
	}

	// --long spelling, too
	w.Reset()
	_, err = ParseInterleaved(newTake(&w), []string{"--kep"})
	if !IsUsageError(err) || !strings.Contains(w.String(), "did you mean -keep?") {
		t.Fatalf("%v\n%s", err, w.String())
	}

	// nothing close: no guess, still the hint
	w.Reset()
	_, err = ParseInterleaved(newTake(&w), []string{"-zzzzzz"})
	if !IsUsageError(err) || strings.Contains(w.String(), "did you mean") || !strings.Contains(w.String(), "-h lists its options") {
		t.Fatalf("%v\n%s", err, w.String())
	}

	// a wrong value or a missing one: the flag package's message and the hint, not the usage
	for _, args := range [][]string{{"-tail", "abc"}, {"-dir"}} {
		w.Reset()
		_, err = ParseInterleaved(newTake(&w), args)
		if !IsUsageError(err) || strings.Contains(w.String(), "Usage of") || !strings.Contains(w.String(), "-h lists its options") {
			t.Fatalf("%v: %v\n%s", args, err, w.String())
		}
	}

	// -h: the full usage
	w.Reset()
	_, err = ParseInterleaved(newTake(&w), []string{"-h"})
	if !errors.Is(err, flag.ErrHelp) || !strings.Contains(w.String(), "Usage of take:") || !strings.Contains(w.String(), "keep it") ||
		strings.Contains(w.String(), "did you mean") {
		t.Fatalf("%v\n%s", err, w.String())
	}
}

// The same FlagSet asked for help and then mistyped (ParseInterleaved parses several times) keeps telling them apart.
func TestFlagSetTellsHelpFromErrorsAcrossParses(t *testing.T) {
	var w bytes.Buffer
	fs := newTake(&w)
	_ = fs.Parse([]string{"-h"})
	w.Reset()
	_ = fs.Parse([]string{"-lst"})
	if strings.Contains(w.String(), "Usage of") || !strings.Contains(w.String(), "did you mean") {
		t.Fatalf("%s", w.String())
	}
	w.Reset()
	_ = fs.Parse([]string{"-h"})
	if !strings.Contains(w.String(), "Usage of take:") {
		t.Fatalf("help after an error: %s", w.String())
	}
}

func TestIsUsageError(t *testing.T) {
	if IsUsageError(nil) || IsUsageError(errors.New("could not connect to the hub")) || IsUsageError(flag.ErrHelp) {
		t.Fatal("only the flag package's parse errors")
	}
}
