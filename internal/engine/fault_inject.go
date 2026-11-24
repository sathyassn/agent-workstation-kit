//go:build faultinject

package engine

import (
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
)

// EnvFaultKillAt selects the kill point in a faultinject build: "N" kills at
// the Nth point reached, "POINT" at the first time POINT is reached, and
// "POINT:N" at its Nth time.
const EnvFaultKillAt = "WSKIT_FAULT_KILL_AT"

// defaultFault returns a hook that SIGKILLs this process at the point named
// by WSKIT_FAULT_KILL_AT. SIGKILL runs no deferred code, so what survives is
// exactly what was durable.
func defaultFault() FaultFunc {
	spec := os.Getenv(EnvFaultKillAt)
	if spec == "" {
		return nil
	}
	name, nth := parseKillAt(spec)
	var total, named atomic.Int64
	return func(p Point) {
		hit := total.Add(1) == nth && name == ""
		if name != "" && Point(name) == p {
			hit = named.Add(1) == nth
		}
		if hit {
			_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
			select {} // wait for the signal
		}
	}
}

func parseKillAt(spec string) (string, int64) {
	if n, err := strconv.ParseInt(spec, 10, 64); err == nil {
		return "", n
	}
	name, count, found := strings.Cut(spec, ":")
	if !found {
		return name, 1
	}
	n, err := strconv.ParseInt(count, 10, 64)
	if err != nil || n < 1 {
		return name, 1
	}
	return name, n
}
