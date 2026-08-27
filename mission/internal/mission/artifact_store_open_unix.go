//go:build darwin || linux

package mission

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

type retainedArtifactUnixRoot struct {
	fd   int
	name string
}

func openRetainedArtifactRoot(name string) (retainedArtifactRoot, error) {
	fd, err := syscall.Open(name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	return &retainedArtifactUnixRoot{fd: fd, name: name}, nil
}

func (r *retainedArtifactUnixRoot) Name() string { return r.name }

func (r *retainedArtifactUnixRoot) Close() error {
	if r.fd < 0 {
		return nil
	}
	err := syscall.Close(r.fd)
	r.fd = -1
	return err
}

func (r *retainedArtifactUnixRoot) Lstat(path string) (os.FileInfo, error) {
	file, err := r.openPath(path, syscall.O_RDONLY|syscall.O_NONBLOCK)
	if err != nil {
		return nil, err
	}
	info, statErr := file.Stat()
	closeErr := file.Close()
	if statErr != nil {
		return nil, statErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return info, nil
}

func (r *retainedArtifactUnixRoot) Mkdir(path string, perm os.FileMode) error {
	fd, name, closeErr, err := r.parent(path)
	if err != nil {
		return err
	}
	defer closeErr()
	return retainedUnixMkdirat(fd, name, uint32(perm.Perm()))
}

func (r *retainedArtifactUnixRoot) OpenFile(path string, flags int, perm os.FileMode) (*os.File, error) {
	return r.openPath(path, flags|syscall.O_NONBLOCK, uint32(perm.Perm()))
}

func (r *retainedArtifactUnixRoot) Link(oldPath, newPath string) error {
	oldFD, oldName, closeOld, err := r.parent(oldPath)
	if err != nil {
		return err
	}
	defer closeOld()
	newFD, newName, closeNew, err := r.parent(newPath)
	if err != nil {
		return err
	}
	defer closeNew()
	return retainedUnixLinkat(oldFD, oldName, newFD, newName)
}

func (r *retainedArtifactUnixRoot) Remove(path string) error {
	fd, name, closeErr, err := r.parent(path)
	if err != nil {
		return err
	}
	defer closeErr()
	return retainedUnixUnlinkat(fd, name)
}

func (r *retainedArtifactUnixRoot) WriteFile(path string, body []byte, perm os.FileMode) error {
	file, err := r.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := file.Write(body); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func (r *retainedArtifactUnixRoot) SyncDirectory(path string) error {
	file, err := r.openPath(path, syscall.O_RDONLY|syscall.O_DIRECTORY)
	if err != nil {
		return err
	}
	err = file.Sync()
	closeErr := file.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func (r *retainedArtifactUnixRoot) openPath(path string, flags int, modes ...uint32) (*os.File, error) {
	fd, name, closeErr, err := r.parent(path)
	if err != nil {
		return nil, err
	}
	defer closeErr()
	mode := uint32(0)
	if len(modes) > 0 {
		mode = modes[0]
	}
	opened, err := retainedUnixOpenat(fd, name, flags|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, mode)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(opened), filepath.Join(r.name, path))
	if file == nil {
		_ = syscall.Close(opened)
		return nil, errors.New("open retained artifact")
	}
	return file, nil
}

func (r *retainedArtifactUnixRoot) parent(path string) (int, string, func(), error) {
	parts, err := retainedArtifactPathParts(path)
	if err != nil {
		return 0, "", func() {}, err
	}
	fd := r.fd
	closeFD := func() {}
	for _, part := range parts[:len(parts)-1] {
		next, err := retainedUnixOpenat(fd, part, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
		if err != nil {
			closeFD()
			return 0, "", func() {}, err
		}
		closeFD()
		fd = next
		closeFD = func(fd int) func() {
			return func() { _ = syscall.Close(fd) }
		}(fd)
	}
	return fd, parts[len(parts)-1], closeFD, nil
}

func retainedArtifactPathParts(path string) ([]string, error) {
	clean := filepath.Clean(path)
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("retained artifact path escapes root: %q", path)
	}
	parts := strings.Split(clean, string(filepath.Separator))
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return nil, fmt.Errorf("invalid retained artifact path: %q", path)
		}
	}
	return parts, nil
}

func openRetainedArtifactFileNoFollow(root retainedArtifactRoot, path string) (*os.File, error) {
	return root.OpenFile(path, os.O_RDONLY, 0)
}

func validateRetainedArtifactDirectoryPlatform(retainedArtifactRoot, string) error {
	return nil
}

func publishRetainedArtifact(root retainedArtifactRoot, temporaryName, objectName string, expected []byte) error {
	if err := root.Link(temporaryName, objectName); err != nil {
		if _, statErr := root.Lstat(objectName); statErr != nil {
			return fmt.Errorf("publish retained artifact: %w", err)
		}
		if err := verifyRetainedArtifact(root, objectName, expected); err != nil {
			return err
		}
	} else if err := verifyRetainedArtifact(root, objectName, expected); err != nil {
		return err
	}
	if err := root.Remove(temporaryName); err != nil {
		return fmt.Errorf("remove temporary retained artifact: %w", err)
	}
	return nil
}

func confirmRetainedArtifactDurabilityPlatform(root retainedArtifactRoot, path string) error {
	return root.SyncDirectory(path)
}
