//go:build !linux

package linux

import (
	"errors"
	"syscall"
)

// errNoPidfd reports a host without pidfd; signalling fails closed. It is a
// sentinel and is never reassigned.
var errNoPidfd = errors.New("pidfd is only available on Linux")

func defaultPidfd() *pidfdOps {
	return &pidfdOps{
		open:  func(int) (int, error) { return -1, errNoPidfd },
		send:  func(int, syscall.Signal) error { return errNoPidfd },
		close: func(int) error { return nil },
	}
}
