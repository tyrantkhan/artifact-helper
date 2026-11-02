//go:build !windows

package cli

import "os"

// terminalFile reports whether f is a terminal: a character device other than the null device, which is one
// too but answers no questions.
func terminalFile(f *os.File) bool {
	st, err := f.Stat()
	if err != nil || st.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	if null, err := os.Stat(os.DevNull); err == nil && os.SameFile(st, null) {
		return false
	}
	return true
}
