//go:build darwin || linux

package mission

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestRetainArtifactRejectsLeafSymlinkInstalledAtOpen(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	body := []byte("open must not follow replacement symlink")
	objectPath, _, err := store.retainArtifact(body)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "outside.fifo")
	if err := syscall.Mkfifo(target, 0o600); err != nil {
		t.Fatal(err)
	}

	previous := openRetainedArtifactFile
	defer func() { openRetainedArtifactFile = previous }()
	openRetainedArtifactFile = func(root retainedArtifactRoot, path string) (*os.File, error) {
		absolutePath := filepath.Join(root.Name(), path)
		if err := os.Remove(absolutePath); err != nil {
			return nil, err
		}
		if err := os.Symlink(target, absolutePath); err != nil {
			return nil, err
		}
		return openRetainedArtifactFileNoFollow(root, path)
	}

	result := make(chan error, 1)
	go func() {
		_, _, retainErr := store.retainArtifact(body)
		result <- retainErr
	}()
	select {
	case retainErr := <-result:
		if retainErr == nil {
			t.Fatal("replacement symlink was accepted")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("retention blocked while opening replacement symlink target")
	}
	info, err := os.Lstat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeNamedPipe == 0 {
		t.Fatalf("target mode=%s want FIFO", info.Mode())
	}
	info, err = os.Lstat(objectPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("object mode=%s want symlink after injected replacement", info.Mode())
	}
}
