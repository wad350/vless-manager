package main

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// run executes the given command and returns combined stdout+stderr as a string.
func run(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return string(out), err
}

// readUint64File reads a small text file (sysfs/procfs) and returns its
// content parsed as uint64. Used for counter files like /sys/class/net/*/statistics/*_bytes.
func readUint64File(path string) (uint64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
}
