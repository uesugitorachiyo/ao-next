//go:build windows

package mission

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestWindowsRetainedArtifactPublicationUsesWriteThroughMoveWithoutReplacement(t *testing.T) {
	root, temporaryName, objectName, body := prepareWindowsRetainedArtifactPublication(t, nil)
	previous := moveRetainedArtifactWindows
	t.Cleanup(func() { moveRetainedArtifactWindows = previous })

	var source, destination string
	var flags uint32
	moveRetainedArtifactWindows = func(gotSource, gotDestination string, gotFlags uint32) error {
		source, destination, flags = gotSource, gotDestination, gotFlags
		return os.Rename(gotSource, gotDestination)
	}

	if err := publishRetainedArtifact(root, temporaryName, objectName, body); err != nil {
		t.Fatal(err)
	}
	if source != filepath.Join(root.Name(), temporaryName) {
		t.Fatalf("MoveFileExW source=%q", source)
	}
	if destination != filepath.Join(root.Name(), objectName) {
		t.Fatalf("MoveFileExW destination=%q", destination)
	}
	if flags != missionMoveFileWriteThrough {
		t.Fatalf("MoveFileExW flags=%#x want %#x", flags, missionMoveFileWriteThrough)
	}
	if flags&missionMoveFileReplaceExisting != 0 {
		t.Fatalf("MoveFileExW unexpectedly allows replacement: flags=%#x", flags)
	}
	assertWindowsRetainedArtifactPublication(t, root, temporaryName, objectName, body)
}

func TestWindowsRetainedArtifactPublicationReusesExactCollision(t *testing.T) {
	for _, collision := range []error{syscall.ERROR_ALREADY_EXISTS, syscall.ERROR_FILE_EXISTS} {
		t.Run(collision.Error(), func(t *testing.T) {
			body := []byte("exact collision bytes")
			root, temporaryName, objectName, _ := prepareWindowsRetainedArtifactPublication(t, body)
			previous := moveRetainedArtifactWindows
			t.Cleanup(func() { moveRetainedArtifactWindows = previous })
			moveRetainedArtifactWindows = func(_, _ string, flags uint32) error {
				if flags != missionMoveFileWriteThrough {
					t.Fatalf("MoveFileExW flags=%#x want %#x", flags, missionMoveFileWriteThrough)
				}
				return collision
			}

			if err := publishRetainedArtifact(root, temporaryName, objectName, body); err != nil {
				t.Fatal(err)
			}
			assertWindowsRetainedArtifactPublication(t, root, temporaryName, objectName, body)
		})
	}
}

func TestWindowsRetainedArtifactPublicationRejectsMismatchedCollision(t *testing.T) {
	body := []byte("expected")
	existing := []byte("mismatch")
	root, temporaryName, objectName, _ := prepareWindowsRetainedArtifactPublication(t, existing)
	previous := moveRetainedArtifactWindows
	t.Cleanup(func() { moveRetainedArtifactWindows = previous })
	moveRetainedArtifactWindows = func(_, _ string, _ uint32) error {
		return syscall.ERROR_ALREADY_EXISTS
	}

	if err := publishRetainedArtifact(root, temporaryName, objectName, body); err == nil {
		t.Fatal("mismatched publication collision was accepted")
	}
	got, err := os.ReadFile(filepath.Join(root.Name(), objectName))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, existing) {
		t.Fatalf("existing object changed to %q", got)
	}
}

func TestWindowsRetainedArtifactPublicationPropagatesUnexpectedMoveError(t *testing.T) {
	root, temporaryName, objectName, body := prepareWindowsRetainedArtifactPublication(t, nil)
	previous := moveRetainedArtifactWindows
	t.Cleanup(func() { moveRetainedArtifactWindows = previous })
	want := errors.New("injected MoveFileExW failure")
	moveRetainedArtifactWindows = func(_, _ string, _ uint32) error { return want }

	err := publishRetainedArtifact(root, temporaryName, objectName, body)
	if !errors.Is(err, want) {
		t.Fatalf("publication error=%v want %v", err, want)
	}
	if _, err := os.Lstat(filepath.Join(root.Name(), temporaryName)); err != nil {
		t.Fatalf("temporary removed after failed move: %v", err)
	}
}

func prepareWindowsRetainedArtifactPublication(t *testing.T, existing []byte) (retainedArtifactRoot, string, string, []byte) {
	t.Helper()
	rootPath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(rootPath, retainedArtifactDirectory), 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte("temporary retained bytes")
	temporaryName := filepath.Join(retainedArtifactDirectory, "temporary")
	objectName := filepath.Join(retainedArtifactDirectory, "object")
	if err := os.WriteFile(filepath.Join(rootPath, temporaryName), body, 0o644); err != nil {
		t.Fatal(err)
	}
	if existing != nil {
		if err := os.WriteFile(filepath.Join(rootPath, objectName), existing, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	root, err := openRetainedArtifactRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root, temporaryName, objectName, body
}

func assertWindowsRetainedArtifactPublication(t *testing.T, root retainedArtifactRoot, temporaryName, objectName string, want []byte) {
	t.Helper()
	if _, err := os.Lstat(filepath.Join(root.Name(), temporaryName)); !os.IsNotExist(err) {
		t.Fatalf("temporary still exists: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root.Name(), objectName))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("published bytes=%q want %q", got, want)
	}
}
