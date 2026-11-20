package module

import (
	"flag"
	"io"
	"reflect"
	"testing"
)

func TestParseInterleaved(t *testing.T) {
	for _, tc := range []struct {
		args []string
		pos  []string
		name string
		yes  bool
	}{
		{[]string{"a", "-name", "x", "b"}, []string{"a", "b"}, "x", false},
		{[]string{"-name", "x", "a"}, []string{"a"}, "x", false},
		{[]string{"a", "-yes"}, []string{"a"}, "", true},
		{[]string{"-yes", "--", "-odd"}, []string{"-odd"}, "", true},
		{nil, nil, "", false},
	} {
		fs := flag.NewFlagSet("t", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		name := fs.String("name", "", "")
		yes := fs.Bool("yes", false, "")
		pos, err := ParseInterleaved(fs, tc.args)
		if err != nil || !reflect.DeepEqual(pos, tc.pos) || *name != tc.name || *yes != tc.yes {
			t.Errorf("%v => %v %q %v %v", tc.args, pos, *name, *yes, err)
		}
	}
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if _, err := ParseInterleaved(fs, []string{"-unknown"}); err == nil {
		t.Error("an unknown flag must fail")
	}
}
