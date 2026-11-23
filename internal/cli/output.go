package cli

import (
	"io"
	"os"
	"strconv"
	"strings"
)

// OutputMode is the front end a run uses (SPC-001 "Output").
type OutputMode string

// Output modes.
const (
	ModeJSON  OutputMode = "json"
	ModePlain OutputMode = "plain"
	ModeTUI   OutputMode = "tui"
)

const (
	envPlain   = "WSKIT_PLAIN"
	envTerm    = "TERM"
	termDumb   = "dumb"
	envPlainOn = "1"
)

// outputFlags are the two flags that choose the output mode.
type outputFlags struct {
	json  bool
	plain bool
}

// resolveOutputMode applies SPC-001's precedence: --json first, then --plain
// or WSKIT_PLAIN=1, then the terminal UI only when standard output and
// standard error are both terminals and TERM is not dumb, else plain.
func resolveOutputMode(f outputFlags, getenv func(string) string, outTTY, errTTY bool) OutputMode {
	switch {
	case f.json:
		return ModeJSON
	case f.plain || getenv(envPlain) == envPlainOn:
		return ModePlain
	case outTTY && errTTY && getenv(envTerm) != termDumb:
		return ModeTUI
	default:
		return ModePlain
	}
}

// peekOutputFlags reads --json and --plain from args before cobra parses
// them, because the front end (fang styling or none) is chosen before the
// command runs. Parsing stops at "--", as pflag's does.
func peekOutputFlags(args []string) outputFlags {
	var f outputFlags
	for _, a := range args {
		if a == "--" {
			break
		}
		if !strings.HasPrefix(a, "--") {
			continue
		}
		name, value, hasValue := strings.Cut(strings.TrimPrefix(a, "--"), "=")
		on := true
		if hasValue {
			b, err := strconv.ParseBool(value)
			if err != nil {
				continue // pflag reports the bad value as a usage error
			}
			on = b
		}
		switch name {
		case flagJSON:
			f.json = on
		case flagPlain:
			f.plain = on
		}
	}
	return f
}

// isTerminal reports whether w is an *os.File attached to a terminal.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return isTerminalFd(f.Fd())
}
