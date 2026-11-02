//go:build windows

package cli

import (
	"os"
	"syscall"
)

// terminalFile reports whether f is a console. Go reports every console handle as the null device there, so
// asking for the console mode, which fails for NUL, files and pipes, is what tells them apart.
func terminalFile(f *os.File) bool {
	var mode uint32
	return syscall.GetConsoleMode(syscall.Handle(f.Fd()), &mode) == nil
}
