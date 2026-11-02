//go:build !windows

package cli

import (
	"os"
	"testing"
)

// Telling a terminal from other character devices takes system calls that differ by OS, so terminalFile does
// not try: any character device but the null device counts. /dev/zero stands in for a terminal here, since
// CI has none.
func TestACharacterDeviceOtherThanNullCountsAsATerminal(t *testing.T) {
	f, err := os.Open("/dev/zero")
	if err != nil {
		t.Skip("no /dev/zero:", err)
	}
	defer f.Close()
	if !terminalFile(f) {
		t.Fatal("/dev/zero did not count as a terminal")
	}
}
