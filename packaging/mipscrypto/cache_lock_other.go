//go:build !(android || darwin || dragonfly || freebsd || illumos || ios || linux || netbsd || openbsd || solaris || windows)

package main

import (
	"fmt"
	"os"
	"runtime"
)

func lockStaging(*os.File) (func(), error) {
	return nil, fmt.Errorf("MIPS crypto staging lock is unsupported on %s", runtime.GOOS)
}
