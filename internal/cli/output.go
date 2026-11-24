package cli

import (
	"io"
	"os"
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

// isTerminal reports whether w is an *os.File attached to a terminal.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return isTerminalFd(f.Fd())
}
