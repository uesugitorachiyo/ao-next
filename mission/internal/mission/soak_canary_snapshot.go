package mission

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
)

const (
	SoakCanarySourceProvenanceSchema    = "ao.mission.soak-canary-source-provenance.v1"
	SoakCanaryRepositorySnapshotSchema  = "ao.mission.soak-canary-repository-snapshot.v1"
	soakCanarySnapshotMaximumEntries    = 200_000
	soakCanarySnapshotMaximumFileBytes  = 64 << 20
	soakCanarySnapshotMaximumTotalBytes = 2 << 30
)

type SoakCanarySourceProvenance struct {
	Schema           string `json:"schema"`
	Revision         string `json:"revision"`
	Modified         bool   `json:"modified"`
	Provider         string `json:"provider"`
	ProvenanceDigest string `json:"provenance_digest"`
}

type SoakCanaryRepositorySnapshotEntry struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Mode   uint32 `json:"mode"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

type SoakCanaryRepositorySnapshot struct {
	Schema         string                              `json:"schema"`
	Entries        []SoakCanaryRepositorySnapshotEntry `json:"entries"`
	EntryCount     int                                 `json:"entry_count"`
	RegularFiles   int                                 `json:"regular_files"`
	Symlinks       int                                 `json:"symlinks"`
	TotalBytes     int64                               `json:"total_bytes"`
	SnapshotDigest string                              `json:"snapshot_digest"`
}

type SoakCanarySourceProvenanceProvider interface {
	SourceProvenance() (SoakCanarySourceProvenance, error)
}

type SoakCanaryRepositorySnapshotter interface {
	Snapshot(repositoryRoot string) (SoakCanaryRepositorySnapshot, error)
}

type SoakCanaryGitVerifier interface {
	Verify(repositoryRoot, expectedRevision string) error
}

type BuildInfoSoakCanarySourceProvenanceProvider struct{}

type PureGoSoakCanaryRepositorySnapshotter struct{}

type InProcessSoakCanaryGitVerifier struct{}

func (BuildInfoSoakCanarySourceProvenanceProvider) SourceProvenance() (SoakCanarySourceProvenance, error) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return SoakCanarySourceProvenance{}, errors.New("soak canary Go build information is unavailable")
	}
	settings := map[string]string{}
	for _, setting := range info.Settings {
		settings[setting.Key] = setting.Value
	}
	return soakCanarySourceProvenanceFromBuildSettings(settings)
}

func soakCanarySourceProvenanceFromBuildSettings(
	settings map[string]string,
) (SoakCanarySourceProvenance, error) {
	revision := strings.TrimSpace(settings["vcs.revision"])
	modified, err := strconv.ParseBool(settings["vcs.modified"])
	if err != nil || !validSoakHexDigest(revision, 40, "") {
		return SoakCanarySourceProvenance{}, errors.New("soak canary Go build provenance is incomplete")
	}
	if modified {
		return SoakCanarySourceProvenance{}, errors.New("soak canary Go build provenance is modified")
	}
	provenance := SoakCanarySourceProvenance{
		Schema: SoakCanarySourceProvenanceSchema, Revision: revision,
		Modified: false, Provider: "go_build_info",
	}
	signSoakCanarySourceProvenance(&provenance)
	return provenance, nil
}

func (PureGoSoakCanaryRepositorySnapshotter) Snapshot(repositoryRoot string) (SoakCanaryRepositorySnapshot, error) {
	return BuildSoakCanaryRepositorySnapshot(repositoryRoot)
}

func BuildSoakCanaryRepositorySnapshot(repositoryRoot string) (SoakCanaryRepositorySnapshot, error) {
	root, err := filepath.Abs(repositoryRoot)
	if err != nil {
		return SoakCanaryRepositorySnapshot{}, err
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return SoakCanaryRepositorySnapshot{}, errors.New("soak canary snapshot root is not a regular directory")
	}
	snapshot := SoakCanaryRepositorySnapshot{
		Schema:  SoakCanaryRepositorySnapshotSchema,
		Entries: []SoakCanaryRepositorySnapshotEntry{},
	}
	if err := walkSoakCanaryRepositorySnapshot(root, "", &snapshot); err != nil {
		return SoakCanaryRepositorySnapshot{}, err
	}
	sort.Slice(snapshot.Entries, func(i, j int) bool {
		return snapshot.Entries[i].Path < snapshot.Entries[j].Path
	})
	snapshot.EntryCount = len(snapshot.Entries)
	signSoakCanaryRepositorySnapshot(&snapshot)
	return snapshot, nil
}

func walkSoakCanaryRepositorySnapshot(
	root, relativeDirectory string,
	snapshot *SoakCanaryRepositorySnapshot,
) error {
	directory := filepath.Join(root, filepath.FromSlash(relativeDirectory))
	children, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	sort.Slice(children, func(i, j int) bool { return children[i].Name() < children[j].Name() })
	for _, child := range children {
		if child.Name() == ".git" {
			continue
		}
		relative := child.Name()
		if relativeDirectory != "" {
			relative = relativeDirectory + "/" + child.Name()
		}
		path := filepath.Join(root, filepath.FromSlash(relative))
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		entry := SoakCanaryRepositorySnapshotEntry{
			Path: relative, Mode: uint32(info.Mode()), SHA256: digestBytes(nil),
		}
		switch {
		case info.Mode().IsDir():
			entry.Kind = "directory"
		case info.Mode().IsRegular():
			if info.Size() > soakCanarySnapshotMaximumFileBytes {
				return fmt.Errorf("soak canary snapshot file exceeds limit: %s", relative)
			}
			body, err := readBoundedRegularFile(path, soakCanarySnapshotMaximumFileBytes)
			if err != nil {
				return err
			}
			entry.Kind = "regular"
			entry.Bytes = int64(len(body))
			entry.SHA256 = digestBytes(body)
			snapshot.RegularFiles++
			snapshot.TotalBytes += int64(len(body))
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			entry.Kind = "symlink"
			entry.Bytes = int64(len(target))
			entry.SHA256 = digestBytes([]byte(target))
			snapshot.Symlinks++
			snapshot.TotalBytes += int64(len(target))
		default:
			return fmt.Errorf("soak canary snapshot encountered unsupported file: %s", relative)
		}
		snapshot.Entries = append(snapshot.Entries, entry)
		if len(snapshot.Entries) > soakCanarySnapshotMaximumEntries ||
			snapshot.TotalBytes > soakCanarySnapshotMaximumTotalBytes {
			return errors.New("soak canary repository snapshot exceeds bounded limits")
		}
		if entry.Kind == "directory" {
			if err := walkSoakCanaryRepositorySnapshot(root, relative, snapshot); err != nil {
				return err
			}
		}
	}
	return nil
}

func signSoakCanarySourceProvenance(provenance *SoakCanarySourceProvenance) {
	provenance.ProvenanceDigest = ""
	body, _ := json.Marshal(*provenance)
	provenance.ProvenanceDigest = digestBytes(body)
}

func signSoakCanaryRepositorySnapshot(snapshot *SoakCanaryRepositorySnapshot) {
	snapshot.SnapshotDigest = ""
	body, _ := json.Marshal(*snapshot)
	snapshot.SnapshotDigest = digestBytes(body)
}

func verifySoakCanaryRepositorySnapshot(
	request SoakCanaryRunRequest,
) (SoakCanaryRepositorySnapshot, error) {
	if request.Snapshotter == nil {
		return SoakCanaryRepositorySnapshot{}, errors.New("soak canary repository snapshotter is required")
	}
	current, err := request.Snapshotter.Snapshot(request.RepositoryRoot)
	if err != nil {
		return current, fmt.Errorf("snapshot soak canary repository: %w", err)
	}
	if !reflect.DeepEqual(current, request.RepositorySnapshot) {
		return current, fmt.Errorf(
			"soak canary repository snapshot=%s want=%s",
			current.SnapshotDigest,
			request.RepositorySnapshot.SnapshotDigest,
		)
	}
	return current, nil
}

func verifySoakCanaryGitRepository(request SoakCanaryRunRequest) error {
	if request.GitVerifier == nil {
		return errors.New("soak canary Git verifier is required")
	}
	return request.GitVerifier.Verify(request.RepositoryRoot, request.Activation.SourceHead)
}
