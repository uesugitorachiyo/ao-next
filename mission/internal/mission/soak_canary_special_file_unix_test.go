//go:build !windows

package mission

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestSoakCanaryStrictInputTransportRejectsFIFOAndDevice(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "authority.fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		path string
	}{
		{name: "fifo", path: fifo},
		{name: "device", path: os.DevNull},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := LoadSoakCanaryAuthority(test.path); err == nil ||
				!strings.Contains(err.Error(), "regular") {
				t.Fatalf("special input error=%v", err)
			}
		})
	}
}
