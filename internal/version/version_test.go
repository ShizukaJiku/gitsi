package version

import "testing"

func TestStringIncludesBuildInfoOnlyWhenSet(t *testing.T) {
	oldV, oldC, oldD := Version, Commit, Date
	defer func() { Version, Commit, Date = oldV, oldC, oldD }()

	Version, Commit, Date = "1.2.3", "", ""
	if got := String(); got != "1.2.3" {
		t.Errorf("without build data: %q", got)
	}
	Commit = "abc1234"
	if got := String(); got != "1.2.3 (commit abc1234)" {
		t.Errorf("%q", got)
	}
	Date = "2026-11-17"
	if got := String(); got != "1.2.3 (commit abc1234, built 2026-11-17)" {
		t.Errorf("%q", got)
	}
}
