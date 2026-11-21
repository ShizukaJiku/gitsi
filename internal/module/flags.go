package module

import (
	"flag"
	"fmt"
	"io"
	"strings"
)

// NewFlagSet is the FlagSet of every command: errors are returned (ContinueOnError) and messages go to w (the command's stderr).
// Asked for help (-h), it prints the usage as usual. On a mistake (an unknown option, a missing or invalid value) it does not
// dump the whole usage after the flag package's message: it proposes the closest option and says how to list them.
func NewFlagSet(name string, w io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	rec := &lastWrite{w: w}
	fs.SetOutput(rec)
	fs.Usage = func() {
		// The flag package writes its error message right before calling Usage; a help request (-h) writes nothing first.
		msg := strings.TrimSpace(rec.last)
		rec.last = ""
		if msg == "" {
			fmt.Fprintf(w, "Usage of %s:\n", name)
			fs.PrintDefaults() // through rec: its last line must not look like an error on a later Usage
			rec.last = ""
			return
		}
		if bad, ok := strings.CutPrefix(msg, "flag provided but not defined: -"); ok {
			var names []string
			fs.VisitAll(func(f *flag.Flag) { names = append(names, f.Name) })
			if s := DidYouMean("-", Suggest(strings.TrimLeft(bad, "-"), names)); s != "" {
				fmt.Fprintln(w, "  "+s)
			}
		}
		fmt.Fprintf(w, "  gitsi %s -h lists its options\n", name)
	}
	return fs
}

// lastWrite passes writes through and remembers the last one: the error message the flag package wrote before calling Usage.
type lastWrite struct {
	w    io.Writer
	last string
}

func (l *lastWrite) Write(p []byte) (int, error) {
	l.last = string(p)
	return l.w.Write(p)
}

// usagePrefixes are the starts of the flag package's parse errors. Its message is already printed (by the FlagSet), so whoever
// gets the error only sets the exit code.
var usagePrefixes = []string{
	"flag provided but not defined: ", "flag needs an argument: ", "invalid value ", "invalid boolean value ",
	"invalid boolean flag ", "bad flag syntax: ",
}

// IsUsageError reports whether err is the flag package's parse error (wrong usage: exit code 2, already explained).
func IsUsageError(err error) bool {
	if err == nil {
		return false
	}
	for _, p := range usagePrefixes {
		if strings.HasPrefix(err.Error(), p) {
			return true
		}
	}
	return false
}
