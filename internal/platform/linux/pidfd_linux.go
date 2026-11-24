package linux

import (
	"syscall"

	"golang.org/x/sys/unix"
)

func defaultPidfd() *pidfdOps {
	return &pidfdOps{
		open: func(pid int) (int, error) { return unix.PidfdOpen(pid, 0) },
		send: func(fd int, sig syscall.Signal) error {
			return unix.PidfdSendSignal(fd, sig, nil, 0)
		},
		close: unix.Close,
	}
}
