package mission

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func canonicalSoakCanaryGitDirectory(path string) (string, error) {
	absolute, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	unresolved, err := os.Lstat(absolute)
	if err != nil || !unresolved.IsDir() || unresolved.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("soak canary Git metadata directory is unsafe")
	}
	if err := validateSoakCanaryGitMetadataPlatformComponent(absolute); err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(resolved)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("soak canary Git metadata directory is unsafe")
	}
	return filepath.Clean(resolved), nil
}

func validateSoakCanaryGitMetadataDirectory(root, path string) error {
	return validateSoakCanaryGitMetadataComponents(root, path, true)
}

func validateSoakCanaryGitMetadataRegularPath(root, path string) error {
	return validateSoakCanaryGitMetadataComponents(root, path, false)
}

func validateSoakCanaryGitMetadataComponents(root, path string, finalDirectory bool) error {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == "." || filepath.IsAbs(relative) ||
		relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return errors.New("soak canary Git metadata path escapes its canonical root")
	}
	components := strings.Split(relative, string(os.PathSeparator))
	current := root
	for index, component := range components {
		if component == "" || component == "." || component == ".." {
			return errors.New("soak canary Git metadata path is unsafe")
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		final := index == len(components)-1
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("soak canary Git metadata path contains an unsafe symlink")
		}
		if err := validateSoakCanaryGitMetadataPlatformComponent(current); err != nil {
			return err
		}
		if !final || finalDirectory {
			if !info.IsDir() || info.Mode().Type() != os.ModeDir {
				return errors.New("soak canary Git metadata directory component is unsafe")
			}
			continue
		}
		if !info.Mode().IsRegular() || info.Mode().Type() != 0 {
			return errors.New("soak canary Git metadata path must be a regular file")
		}
	}
	return nil
}

func openSoakCanaryGitMetadataRegular(root, path string) (*os.File, os.FileInfo, error) {
	if err := validateSoakCanaryGitMetadataRegularPath(root, path); err != nil {
		return nil, nil, err
	}
	file, err := openSoakCanaryGitMetadataRegularNoFollow(path)
	if err != nil {
		return nil, nil, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Type() != 0 {
		file.Close()
		return nil, nil, errors.New("soak canary Git metadata path must be a regular file")
	}
	if err := validateSoakCanaryGitMetadataRegularPath(root, path); err != nil {
		file.Close()
		return nil, nil, err
	}
	pathInfo, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, pathInfo) {
		file.Close()
		return nil, nil, errors.New("soak canary Git metadata identity changed while opening")
	}
	return file, info, nil
}

func readSoakCanaryGitMetadataFile(root, path string, limit uint64) ([]byte, error) {
	file, before, err := openSoakCanaryGitMetadataRegular(root, path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if before.Size() < 0 || uint64(before.Size()) > limit {
		return nil, fmt.Errorf("soak canary Git metadata exceeds %d bytes", limit)
	}
	size, err := checkedSoakCanaryGitInt(uint64(before.Size()))
	if err != nil {
		return nil, err
	}
	body := make([]byte, size)
	if _, err := io.ReadFull(file, body); err != nil {
		return nil, err
	}
	var extra [1]byte
	if count, readErr := file.Read(extra[:]); count != 0 || (readErr != nil && readErr != io.EOF) {
		return nil, errors.New("soak canary Git metadata changed while reading")
	}
	after, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(before, after) || before.Size() != after.Size() {
		return nil, errors.New("soak canary Git metadata identity changed while reading")
	}
	return body, nil
}
