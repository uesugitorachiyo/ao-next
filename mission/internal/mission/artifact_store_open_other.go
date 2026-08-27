//go:build !darwin && !linux && !windows

package mission

import (
	"errors"
	"os"
)

var errRetainedArtifactUnsupported = errors.New("content-addressed retention is unsupported on this platform")

type retainedArtifactUnsupportedRoot struct{}

func openRetainedArtifactRoot(string) (retainedArtifactRoot, error) {
	return nil, errRetainedArtifactUnsupported
}

func (retainedArtifactUnsupportedRoot) Name() string { return "" }
func (retainedArtifactUnsupportedRoot) Close() error { return nil }
func (retainedArtifactUnsupportedRoot) Lstat(string) (os.FileInfo, error) {
	return nil, errRetainedArtifactUnsupported
}
func (retainedArtifactUnsupportedRoot) Mkdir(string, os.FileMode) error {
	return errRetainedArtifactUnsupported
}
func (retainedArtifactUnsupportedRoot) OpenFile(string, int, os.FileMode) (*os.File, error) {
	return nil, errRetainedArtifactUnsupported
}
func (retainedArtifactUnsupportedRoot) Link(string, string) error {
	return errRetainedArtifactUnsupported
}
func (retainedArtifactUnsupportedRoot) Remove(string) error { return errRetainedArtifactUnsupported }
func (retainedArtifactUnsupportedRoot) WriteFile(string, []byte, os.FileMode) error {
	return errRetainedArtifactUnsupported
}
func (retainedArtifactUnsupportedRoot) SyncDirectory(string) error {
	return errRetainedArtifactUnsupported
}

func openRetainedArtifactFileNoFollow(root retainedArtifactRoot, path string) (*os.File, error) {
	return nil, errRetainedArtifactUnsupported
}

func validateRetainedArtifactDirectoryPlatform(retainedArtifactRoot, string) error {
	return errRetainedArtifactUnsupported
}

func publishRetainedArtifact(retainedArtifactRoot, string, string, []byte) error {
	return errRetainedArtifactUnsupported
}

func confirmRetainedArtifactDurabilityPlatform(retainedArtifactRoot, string) error {
	return errRetainedArtifactUnsupported
}
