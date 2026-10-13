//go:build android || darwin || dragonfly || freebsd || illumos || ios || linux || netbsd || openbsd || solaris

package main

import (
	"os"

	"golang.org/x/sys/unix"
)

func lockStaging(file *os.File) (func(), error) {
	for {
		err := unix.Flock(int(file.Fd()), unix.LOCK_EX)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return nil, err
		}
		return func() { _ = unix.Flock(int(file.Fd()), unix.LOCK_UN) }, nil
	}
}
