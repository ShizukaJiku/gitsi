package app

import (
	"errors"
	"os"
)

// outputMode holds the global output flags. They are only recognized BEFORE the command (gitsi -v check): after it they belong
// to each command, so they never clash with its own flags.
type outputMode struct {
	verbose, quiet, json bool
}

// parseGlobal splits the global flags off the start of the command line. GITSI_VERBOSE and GITSI_QUIET are read too.
func parseGlobal(args []string) ([]string, outputMode, error) {
	m := outputMode{verbose: os.Getenv("GITSI_VERBOSE") == "1", quiet: os.Getenv("GITSI_QUIET") == "1"}
	for len(args) > 0 {
		switch args[0] {
		case "-v", "--verbose":
			m.verbose = true
		case "-q", "--quiet":
			m.quiet = true
		case "--json":
			m.json = true
		default:
			return args, m, check(m)
		}
		args = args[1:]
	}
	return args, m, check(m)
}

func check(m outputMode) error {
	if m.verbose && m.quiet {
		return errors.New("-v and -q do not combine: choose detail or silence")
	}
	return nil
}
