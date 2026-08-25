package mission

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCorrelationWindowsFileTypePolicyRejectsNonDiskHandles(t *testing.T) {
	tests := []struct {
		name     string
		fileType uint32
		want     bool
	}{
		{name: "disk", fileType: 0x0001, want: true},
		{name: "character device", fileType: 0x0002, want: false},
		{name: "pipe", fileType: 0x0003, want: false},
		{name: "remote", fileType: 0x8000, want: false},
		{name: "unknown", fileType: 0, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := correlationWindowsFileTypeAllowed(tt.fileType); got != tt.want {
				t.Fatalf("correlationWindowsFileTypeAllowed(%#x) = %v, want %v", tt.fileType, got, tt.want)
			}
		})
	}
}

func TestUnsupportedPlatformFilesystemPoliciesFailClosed(t *testing.T) {
	for _, name := range []string{"store_lock_other.go", "correlation_file_other.go"} {
		body, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			t.Fatal(err)
		}
		source := string(body)
		if !strings.Contains(source, "unsupported") {
			t.Fatalf("%s does not report its unsupported filesystem policy", name)
		}
		if name == "store_lock_other.go" && strings.Contains(source, "return nil\n") {
			t.Fatalf("%s silently disables Mission locking", name)
		}
		if name == "correlation_file_other.go" && strings.Contains(source, "os.Open(path)") {
			t.Fatalf("%s silently weakens its filesystem policy", name)
		}
	}
}
