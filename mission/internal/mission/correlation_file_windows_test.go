//go:build windows

package mission

import (
	"syscall"
	"testing"
)

func TestCorrelationWindowsOpenFlagsUseSynchronousProtectedHandle(t *testing.T) {
	flags := correlationWindowsOpenFlags()
	if flags&syscall.FILE_FLAG_OVERLAPPED != 0 {
		t.Fatalf("correlation input uses FILE_FLAG_OVERLAPPED: %#x", flags)
	}
	for name, required := range map[string]uint32{
		"open reparse point": syscall.FILE_FLAG_OPEN_REPARSE_POINT,
		"security SQOS":      correlationWindowsSecuritySQOSPresent,
	} {
		if flags&required == 0 {
			t.Fatalf("correlation input flags %#x omit %s", flags, name)
		}
	}
	if flags&correlationWindowsSecurityImpersonationMask != correlationWindowsSecurityAnonymous {
		t.Fatalf("correlation input flags %#x do not use anonymous security QoS", flags)
	}
}
