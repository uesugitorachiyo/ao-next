package mission

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
)

const retainedArtifactDirectory = "artifacts/sha256"

type retainedArtifactRoot interface {
	Name() string
	Close() error
	Lstat(string) (os.FileInfo, error)
	Mkdir(string, os.FileMode) error
	OpenFile(string, int, os.FileMode) (*os.File, error)
	Link(string, string) error
	Remove(string) error
	WriteFile(string, []byte, os.FileMode) error
	SyncDirectory(string) error
}

var (
	openRetainedArtifactFile          = openRetainedArtifactFileNoFollow
	confirmRetainedArtifactDurability = confirmRetainedArtifactDurabilityPlatform
	beforeRetainedArtifactCreate      = func(retainedArtifactRoot, string) error { return nil }
	beforeRetainedArtifactPublish     = func(retainedArtifactRoot, string, string) error { return nil }
	retainedArtifactTempSequence      atomic.Uint64
)

func (s Store) retainArtifact(body []byte) (string, string, error) {
	digest := digestBytes(body)
	if err := os.MkdirAll(s.Root, 0o755); err != nil {
		return "", "", fmt.Errorf("create artifact store root: %w", err)
	}
	root, err := openRetainedArtifactRoot(s.Root)
	if err != nil {
		return "", "", fmt.Errorf("open artifact store root: %w", err)
	}
	defer root.Close()

	objectName := filepath.Join(retainedArtifactDirectory, strings.TrimPrefix(digest, "sha256:"))
	objectPath := filepath.Join(s.Root, objectName)
	if err := ensureRetainedArtifactDirectory(root); err != nil {
		return "", "", err
	}

	if _, err := root.Lstat(objectName); err == nil {
		if err := verifyRetainedArtifact(root, objectName, body); err != nil {
			return "", "", err
		}
		if err := confirmRetainedArtifactDurability(root, retainedArtifactDirectory); err != nil {
			return "", "", fmt.Errorf("confirm retained artifact durability: %w", err)
		}
		return objectPath, digest, nil
	} else if !os.IsNotExist(err) {
		return "", "", fmt.Errorf("inspect retained artifact: %w", err)
	}

	if err := beforeRetainedArtifactCreate(root, retainedArtifactDirectory); err != nil {
		return "", "", fmt.Errorf("prepare retained artifact creation: %w", err)
	}
	temporary, temporaryName, err := createRetainedArtifactTemporary(root)
	if err != nil {
		return "", "", err
	}
	temporaryLive := true
	defer func() {
		if temporaryLive {
			_ = root.Remove(temporaryName)
		}
	}()

	if _, err := temporary.Write(body); err != nil {
		_ = temporary.Close()
		return "", "", fmt.Errorf("write temporary retained artifact: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return "", "", fmt.Errorf("sync temporary retained artifact: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return "", "", fmt.Errorf("close temporary retained artifact: %w", err)
	}

	if err := beforeRetainedArtifactPublish(root, temporaryName, objectName); err != nil {
		return "", "", fmt.Errorf("prepare retained artifact publication: %w", err)
	}
	if err := publishRetainedArtifact(root, temporaryName, objectName, body); err != nil {
		return "", "", err
	}
	temporaryLive = false
	if err := confirmRetainedArtifactDurability(root, retainedArtifactDirectory); err != nil {
		return "", "", fmt.Errorf("confirm retained artifact durability: %w", err)
	}
	return objectPath, digest, nil
}

func createRetainedArtifactTemporary(root retainedArtifactRoot) (*os.File, string, error) {
	for attempt := 0; attempt < 16; attempt++ {
		name := filepath.Join(retainedArtifactDirectory, fmt.Sprintf(".artifact-%d-%d", os.Getpid(), retainedArtifactTempSequence.Add(1)))
		file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err == nil {
			return file, name, nil
		}
		if !os.IsExist(err) {
			return nil, "", fmt.Errorf("create temporary retained artifact: %w", err)
		}
	}
	return nil, "", fmt.Errorf("create temporary retained artifact: too many name collisions")
}

func verifyRetainedArtifact(root retainedArtifactRoot, path string, expected []byte) error {
	pathInfo, err := root.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect retained artifact: %w", err)
	}
	if !pathInfo.Mode().IsRegular() || pathInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("retained artifact must be a regular non-symlink file")
	}
	if pathInfo.Size() != int64(len(expected)) {
		return fmt.Errorf("retained artifact size mismatch")
	}

	file, err := openRetainedArtifactFile(root, path)
	if err != nil {
		return fmt.Errorf("open retained artifact: %w", err)
	}
	openedInfo, statErr := file.Stat()
	if statErr != nil {
		_ = file.Close()
		return fmt.Errorf("stat retained artifact: %w", statErr)
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(pathInfo, openedInfo) {
		_ = file.Close()
		return fmt.Errorf("retained artifact changed while opening")
	}

	actual, readErr := io.ReadAll(io.LimitReader(file, int64(len(expected))))
	if readErr == nil {
		var extra [1]byte
		n, extraErr := file.Read(extra[:])
		if extraErr != nil && extraErr != io.EOF {
			readErr = extraErr
		}
		if n > 0 {
			actual = append(actual, extra[:n]...)
		}
	}
	closeErr := file.Close()
	if readErr != nil {
		return fmt.Errorf("read retained artifact: %w", readErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close retained artifact: %w", closeErr)
	}

	afterInfo, err := root.Lstat(path)
	if err != nil {
		return fmt.Errorf("reinspect retained artifact: %w", err)
	}
	if !afterInfo.Mode().IsRegular() || !os.SameFile(openedInfo, afterInfo) {
		return fmt.Errorf("retained artifact changed while reading")
	}
	if !bytes.Equal(actual, expected) {
		return fmt.Errorf("retained artifact bytes mismatch")
	}
	return nil
}

func ensureRetainedArtifactDirectory(root retainedArtifactRoot) error {
	for _, path := range []string{"artifacts", retainedArtifactDirectory} {
		info, err := root.Lstat(path)
		if os.IsNotExist(err) {
			if err := root.Mkdir(path, 0o755); err != nil && !os.IsExist(err) {
				return fmt.Errorf("create retained artifact directory %q: %w", path, err)
			}
			info, err = root.Lstat(path)
		}
		if err != nil {
			return fmt.Errorf("inspect retained artifact directory %q: %w", path, err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("retained artifact directory %q must be a regular non-symlink directory", path)
		}
		if err := validateRetainedArtifactDirectoryPlatform(root, path); err != nil {
			return fmt.Errorf("validate retained artifact directory %q: %w", path, err)
		}
	}
	return nil
}
