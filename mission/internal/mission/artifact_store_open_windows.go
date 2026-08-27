//go:build windows

package mission

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

var moveRetainedArtifactWindows = moveFileEx

type retainedArtifactWindowsRoot struct{ name string }

func openRetainedArtifactRoot(name string) (retainedArtifactRoot, error) {
	info, err := os.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("retained artifact root must be a regular non-symlink directory")
	}
	root := &retainedArtifactWindowsRoot{name: name}
	if err := root.validatePath("."); err != nil {
		return nil, err
	}
	return root, nil
}

func (r *retainedArtifactWindowsRoot) Name() string { return r.name }
func (r *retainedArtifactWindowsRoot) Close() error { return nil }

func (r *retainedArtifactWindowsRoot) Lstat(path string) (os.FileInfo, error) {
	if err := r.validatePath(filepath.Dir(path)); err != nil {
		return nil, err
	}
	return os.Lstat(filepath.Join(r.name, path))
}

func (r *retainedArtifactWindowsRoot) Mkdir(path string, perm os.FileMode) error {
	if err := r.validatePath(filepath.Dir(path)); err != nil {
		return err
	}
	if err := os.Mkdir(filepath.Join(r.name, path), perm); err != nil {
		return err
	}
	return r.validatePath(path)
}

func (r *retainedArtifactWindowsRoot) OpenFile(path string, flags int, perm os.FileMode) (*os.File, error) {
	if err := r.validatePath(filepath.Dir(path)); err != nil {
		return nil, err
	}
	file, err := openRetainedWindowsFile(filepath.Join(r.name, path), flags, perm)
	if err != nil {
		return nil, err
	}
	if err := r.validatePath(filepath.Dir(path)); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func (r *retainedArtifactWindowsRoot) Link(oldPath, newPath string) error {
	if err := r.validatePath(filepath.Dir(oldPath)); err != nil {
		return err
	}
	if err := r.validatePath(filepath.Dir(newPath)); err != nil {
		return err
	}
	if err := os.Link(filepath.Join(r.name, oldPath), filepath.Join(r.name, newPath)); err != nil {
		return err
	}
	return r.validatePath(filepath.Dir(newPath))
}

func (r *retainedArtifactWindowsRoot) Remove(path string) error {
	if err := r.validatePath(filepath.Dir(path)); err != nil {
		return err
	}
	return os.Remove(filepath.Join(r.name, path))
}

func (r *retainedArtifactWindowsRoot) WriteFile(path string, body []byte, perm os.FileMode) error {
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

func (r *retainedArtifactWindowsRoot) SyncDirectory(path string) error {
	return r.validatePath(path)
}

func (r *retainedArtifactWindowsRoot) validatePath(path string) error {
	if path == "." {
		return nil
	}
	clean := filepath.Clean(path)
	if filepath.IsAbs(clean) || clean == ".." || len(clean) >= 3 && clean[:3] == ".."+string(filepath.Separator) {
		return fmt.Errorf("retained artifact path escapes root: %q", path)
	}
	for _, component := range splitWindowsRelativePath(clean) {
		componentPath := filepath.Join(r.name, component)
		info, err := os.Lstat(componentPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			if component != clean {
				return errors.New("retained artifact directory is not a regular non-reparse directory")
			}
		}
		pointer, err := syscall.UTF16PtrFromString(componentPath)
		if err != nil {
			return err
		}
		attributes, err := syscall.GetFileAttributes(pointer)
		if err != nil {
			return err
		}
		if attributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			return errors.New("retained artifact directory contains a reparse point")
		}
	}
	return nil
}

func splitWindowsRelativePath(path string) []string {
	parts := strings.Split(path, string(filepath.Separator))
	for i := range parts {
		parts[i] = filepath.Join(parts[:i+1]...)
	}
	return parts
}

func openRetainedArtifactFileNoFollow(root retainedArtifactRoot, path string) (*os.File, error) {
	return root.OpenFile(path, os.O_RDONLY, 0)
}

func validateRetainedArtifactDirectoryPlatform(root retainedArtifactRoot, path string) error {
	return root.(*retainedArtifactWindowsRoot).validatePath(path)
}

func publishRetainedArtifact(root retainedArtifactRoot, temporaryName, objectName string, expected []byte) error {
	windowsRoot := root.(*retainedArtifactWindowsRoot)
	if err := windowsRoot.validatePath(filepath.Dir(temporaryName)); err != nil {
		return err
	}
	if err := windowsRoot.validatePath(filepath.Dir(objectName)); err != nil {
		return err
	}
	err := moveRetainedArtifactWindows(
		filepath.Join(root.Name(), temporaryName),
		filepath.Join(root.Name(), objectName),
		missionMoveFileWriteThrough,
	)
	if err != nil && !errors.Is(err, syscall.ERROR_ALREADY_EXISTS) && !errors.Is(err, syscall.ERROR_FILE_EXISTS) {
		return fmt.Errorf("publish retained artifact: %w", err)
	}
	if err := verifyRetainedArtifact(root, objectName, expected); err != nil {
		return err
	}
	if err != nil {
		if err := root.Remove(temporaryName); err != nil {
			return fmt.Errorf("remove temporary retained artifact: %w", err)
		}
	}
	return nil
}

func confirmRetainedArtifactDurabilityPlatform(root retainedArtifactRoot, path string) error {
	return root.(*retainedArtifactWindowsRoot).validatePath(path)
}

func openRetainedWindowsFile(path string, flags int, perm os.FileMode) (*os.File, error) {
	pointer, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	desiredAccess := uint32(syscall.GENERIC_READ)
	if flags&(os.O_WRONLY|os.O_RDWR) != 0 {
		desiredAccess = syscall.GENERIC_WRITE
	}
	if flags&os.O_RDWR != 0 {
		desiredAccess |= syscall.GENERIC_READ
	}
	creation := uint32(syscall.OPEN_EXISTING)
	switch {
	case flags&os.O_CREATE != 0 && flags&os.O_EXCL != 0:
		creation = syscall.CREATE_NEW
	case flags&os.O_CREATE != 0 && flags&os.O_TRUNC != 0:
		creation = syscall.CREATE_ALWAYS
	case flags&os.O_TRUNC != 0:
		creation = syscall.TRUNCATE_EXISTING
	}
	handle, err := syscall.CreateFile(pointer, desiredAccess,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil, creation, syscall.FILE_ATTRIBUTE_NORMAL|syscall.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	var info syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(handle, &info); err != nil {
		_ = syscall.CloseHandle(handle)
		return nil, err
	}
	if info.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0 || info.FileAttributes&syscall.FILE_ATTRIBUTE_DIRECTORY != 0 {
		_ = syscall.CloseHandle(handle)
		return nil, errors.New("retained artifact is a reparse point or directory")
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = syscall.CloseHandle(handle)
		return nil, errors.New("open retained artifact")
	}
	return file, nil
}
