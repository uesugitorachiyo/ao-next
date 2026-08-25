//go:build darwin || linux

package mission

import (
	"os"
)

func replaceAtomicFile(source, destination string) error {
	return os.Rename(source, destination)
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
