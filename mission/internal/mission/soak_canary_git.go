package mission

import (
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	soakCanaryGitMaxIndexBytes     = 64 << 20
	soakCanaryGitMaxEntries        = 200_000
	soakCanaryGitMaxObjectBytes    = 64 << 20
	soakCanaryGitMaxPackIndex      = 256 << 20
	soakCanaryGitMaxPacks          = 128
	soakCanaryGitMaxDeltaDepth     = 64
	soakCanaryGitMaxReferenceLen   = 4 << 10
	soakCanaryGitTotalObjectBudget = 16 << 20
)

type soakCanaryGitLayout struct {
	root      string
	gitDir    string
	commonDir string
}

type soakCanaryGitOID [sha1.Size]byte

type soakCanaryGitIndexEntry struct {
	path string
	mode uint32
	oid  soakCanaryGitOID
}

type soakCanaryGitTreeEntry struct {
	mode uint32
	oid  soakCanaryGitOID
}

type soakCanaryGitObjectStore struct {
	commonDir string
	budget    *soakCanaryGitBudget
}

type soakCanaryGitObject struct {
	kind string
	data []byte
}

type soakCanaryGitBudget struct {
	remaining uint64
}

func newSoakCanaryGitBudget(limit uint64) *soakCanaryGitBudget {
	return &soakCanaryGitBudget{remaining: limit}
}

func (budget *soakCanaryGitBudget) reserve(amount uint64) error {
	if budget == nil || amount > budget.remaining {
		return errors.New("soak canary Git cumulative object budget exceeded")
	}
	budget.remaining -= amount
	return nil
}

func checkedSoakCanaryGitAdd(left, right uint64) (uint64, error) {
	if right > math.MaxUint64-left {
		return 0, errors.New("soak canary Git integer addition overflow")
	}
	return left + right, nil
}

func checkedSoakCanaryGitMultiply(left, right uint64) (uint64, error) {
	if left != 0 && right > math.MaxUint64/left {
		return 0, errors.New("soak canary Git integer multiplication overflow")
	}
	return left * right, nil
}

func checkedSoakCanaryGitInt(value uint64) (int, error) {
	if value > uint64(^uint(0)>>1) {
		return 0, errors.New("soak canary Git value exceeds platform int")
	}
	return int(value), nil
}

func checkedSoakCanaryGitInt64(value uint64) (int64, error) {
	if value > math.MaxInt64 {
		return 0, errors.New("soak canary Git value exceeds int64")
	}
	return int64(value), nil
}

func checkedSoakCanaryGitSubtractInt64(left, right int64) (int64, error) {
	if left < 0 || right < 0 || right > left {
		return 0, errors.New("soak canary Git integer subtraction underflow")
	}
	return left - right, nil
}

func checkedSoakCanaryGitAddInt64(left, right int64) (int64, error) {
	if left < 0 || right < 0 || right > math.MaxInt64-left {
		return 0, errors.New("soak canary Git integer addition overflow")
	}
	return left + right, nil
}

func checkedSoakCanaryGitSliceBounds(total, start, length uint64) (int, int, error) {
	if start > total || length > total-start {
		return 0, 0, errors.New("soak canary Git slice bounds are invalid")
	}
	end := start + length
	startInt, err := checkedSoakCanaryGitInt(start)
	if err != nil {
		return 0, 0, err
	}
	endInt, err := checkedSoakCanaryGitInt(end)
	if err != nil {
		return 0, 0, err
	}
	return startInt, endInt, nil
}

func checkedSoakCanaryGitTableEntryBounds(
	total, start, index, width uint64,
) (int, int, error) {
	entryOffset, err := checkedSoakCanaryGitMultiply(index, width)
	if err != nil {
		return 0, 0, err
	}
	entryStart, err := checkedSoakCanaryGitAdd(start, entryOffset)
	if err != nil {
		return 0, 0, err
	}
	return checkedSoakCanaryGitSliceBounds(total, entryStart, width)
}

func (InProcessSoakCanaryGitVerifier) Verify(repositoryRoot, expectedRevision string) error {
	layout, err := resolveSoakCanaryGitLayout(repositoryRoot)
	if err != nil {
		return err
	}
	expected, err := parseSoakCanaryGitOID(expectedRevision)
	if err != nil {
		return errors.New("soak canary approved Git revision is invalid")
	}
	head, err := resolveSoakCanaryGitHEAD(layout)
	if err != nil {
		return err
	}
	if head != expected {
		return fmt.Errorf("soak canary Git HEAD=%s want=%s", head, expected)
	}
	index, err := loadSoakCanaryGitIndex(layout)
	if err != nil {
		return err
	}
	store := soakCanaryGitObjectStore{
		commonDir: layout.commonDir,
		budget:    newSoakCanaryGitBudget(soakCanaryGitTotalObjectBudget),
	}
	tree, err := loadSoakCanaryGitHEADTree(store, head)
	if err != nil {
		return err
	}
	if err := compareSoakCanaryGitIndexToHEAD(index, tree); err != nil {
		return err
	}
	fileMode, err := soakCanaryGitCoreFileMode(layout)
	if err != nil {
		return err
	}
	if err := verifySoakCanaryGitWorktree(layout.root, index, fileMode); err != nil {
		return err
	}
	return rejectSoakCanaryGitUntracked(layout.root, index)
}

func resolveSoakCanaryGitLayout(repositoryRoot string) (soakCanaryGitLayout, error) {
	root, err := canonicalSoakCanaryGitDirectory(repositoryRoot)
	if err != nil {
		return soakCanaryGitLayout{}, errors.New("soak canary Git root is not a regular directory")
	}
	dotGit := filepath.Join(root, ".git")
	dotInfo, err := os.Lstat(dotGit)
	if err != nil {
		return soakCanaryGitLayout{}, errors.New("soak canary .git metadata is missing")
	}
	var gitDir string
	switch {
	case dotInfo.IsDir() && dotInfo.Mode()&os.ModeSymlink == 0:
		if err := validateSoakCanaryGitMetadataDirectory(root, dotGit); err != nil {
			return soakCanaryGitLayout{}, err
		}
		gitDir = dotGit
	case dotInfo.Mode().IsRegular() && dotInfo.Mode()&os.ModeSymlink == 0:
		body, err := readSoakCanaryGitMetadataFile(
			root,
			dotGit,
			soakCanaryGitMaxReferenceLen,
		)
		if err != nil {
			return soakCanaryGitLayout{}, err
		}
		line := strings.TrimSpace(string(body))
		if !strings.HasPrefix(line, "gitdir: ") || strings.Contains(line, "\n") {
			return soakCanaryGitLayout{}, errors.New("soak canary Git file is malformed")
		}
		gitDir = strings.TrimSpace(strings.TrimPrefix(line, "gitdir: "))
		if !filepath.IsAbs(gitDir) {
			gitDir = filepath.Join(root, gitDir)
		}
	default:
		return soakCanaryGitLayout{}, errors.New("soak canary .git metadata is unsupported")
	}
	gitDir, err = cleanSoakCanaryGitDirectory(gitDir)
	if err != nil {
		return soakCanaryGitLayout{}, err
	}
	commonDir := gitDir
	commonPath := filepath.Join(gitDir, "commondir")
	if info, statErr := os.Lstat(commonPath); statErr == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return soakCanaryGitLayout{}, errors.New("soak canary Git commondir is unsafe")
		}
		body, err := readSoakCanaryGitMetadataFile(
			gitDir,
			commonPath,
			soakCanaryGitMaxReferenceLen,
		)
		if err != nil {
			return soakCanaryGitLayout{}, err
		}
		value := strings.TrimSpace(string(body))
		if value == "" || strings.Contains(value, "\n") {
			return soakCanaryGitLayout{}, errors.New("soak canary Git commondir is malformed")
		}
		if !filepath.IsAbs(value) {
			value = filepath.Join(gitDir, value)
		}
		commonDir, err = cleanSoakCanaryGitDirectory(value)
		if err != nil {
			return soakCanaryGitLayout{}, err
		}
	} else if !os.IsNotExist(statErr) {
		return soakCanaryGitLayout{}, statErr
	}
	return soakCanaryGitLayout{root: root, gitDir: gitDir, commonDir: commonDir}, nil
}

func cleanSoakCanaryGitDirectory(path string) (string, error) {
	return canonicalSoakCanaryGitDirectory(path)
}

func resolveSoakCanaryGitHEAD(layout soakCanaryGitLayout) (soakCanaryGitOID, error) {
	body, err := readSoakCanaryGitMetadataFile(
		layout.gitDir,
		filepath.Join(layout.gitDir, "HEAD"),
		soakCanaryGitMaxReferenceLen,
	)
	if err != nil {
		return soakCanaryGitOID{}, fmt.Errorf("read soak canary Git HEAD: %w", err)
	}
	value := strings.TrimSpace(string(body))
	if strings.HasPrefix(value, "ref: ") {
		return resolveSoakCanaryGitReference(
			layout,
			strings.TrimSpace(strings.TrimPrefix(value, "ref: ")),
			0,
		)
	}
	oid, err := parseSoakCanaryGitOID(value)
	if err != nil {
		return soakCanaryGitOID{}, errors.New("soak canary detached Git HEAD is invalid")
	}
	return oid, nil
}

func resolveSoakCanaryGitReference(
	layout soakCanaryGitLayout,
	name string,
	depth int,
) (soakCanaryGitOID, error) {
	if depth >= 8 || !validSoakCanaryGitReferenceName(name) {
		return soakCanaryGitOID{}, errors.New("soak canary Git reference is invalid")
	}
	for _, directory := range []string{layout.gitDir, layout.commonDir} {
		path := filepath.Join(directory, filepath.FromSlash(name))
		body, err := readSoakCanaryGitMetadataFile(
			directory,
			path,
			soakCanaryGitMaxReferenceLen,
		)
		if err == nil {
			value := strings.TrimSpace(string(body))
			if strings.HasPrefix(value, "ref: ") {
				return resolveSoakCanaryGitReference(
					layout,
					strings.TrimSpace(strings.TrimPrefix(value, "ref: ")),
					depth+1,
				)
			}
			oid, parseErr := parseSoakCanaryGitOID(value)
			if parseErr != nil {
				return soakCanaryGitOID{}, errors.New("soak canary loose Git reference is invalid")
			}
			return oid, nil
		}
		if !os.IsNotExist(err) {
			return soakCanaryGitOID{}, err
		}
		if directory == layout.commonDir {
			break
		}
	}
	body, err := readSoakCanaryGitMetadataFile(
		layout.commonDir,
		filepath.Join(layout.commonDir, "packed-refs"),
		soakCanaryGitMaxIndexBytes,
	)
	if err != nil {
		return soakCanaryGitOID{}, fmt.Errorf("resolve soak canary Git reference %s: %w", name, err)
	}
	for _, line := range strings.Split(string(body), "\n") {
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "^") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return soakCanaryGitOID{}, errors.New("soak canary packed Git references are malformed")
		}
		if fields[1] == name {
			oid, parseErr := parseSoakCanaryGitOID(fields[0])
			if parseErr != nil {
				return soakCanaryGitOID{}, errors.New("soak canary packed Git reference is invalid")
			}
			return oid, nil
		}
	}
	return soakCanaryGitOID{}, fmt.Errorf("soak canary Git reference %s is missing", name)
}

func validSoakCanaryGitReferenceName(name string) bool {
	return strings.HasPrefix(name, "refs/") &&
		name == filepath.ToSlash(filepath.Clean(filepath.FromSlash(name))) &&
		!strings.Contains(name, "..") &&
		!strings.ContainsAny(name, "\x00\r\n\\")
}

func loadSoakCanaryGitIndex(layout soakCanaryGitLayout) ([]soakCanaryGitIndexEntry, error) {
	body, err := readSoakCanaryGitMetadataFile(
		layout.gitDir,
		filepath.Join(layout.gitDir, "index"),
		soakCanaryGitMaxIndexBytes,
	)
	if err != nil {
		return nil, fmt.Errorf("read soak canary Git index: %w", err)
	}
	if len(body) < 12+sha1.Size || !bytes.Equal(body[:4], []byte("DIRC")) {
		return nil, errors.New("soak canary Git index header is corrupt")
	}
	version := binary.BigEndian.Uint32(body[4:8])
	if version != 2 && version != 3 {
		return nil, fmt.Errorf("soak canary Git index version %d is unsupported", version)
	}
	total := uint64(len(body))
	checksumStart := total - sha1.Size
	checksumStartInt, checksumEndInt, err := checkedSoakCanaryGitSliceBounds(
		total,
		checksumStart,
		sha1.Size,
	)
	if err != nil {
		return nil, err
	}
	checksum := sha1.Sum(body[:checksumStartInt])
	if !bytes.Equal(checksum[:], body[checksumStartInt:checksumEndInt]) {
		return nil, errors.New("soak canary Git index checksum mismatch")
	}
	countValue := uint64(binary.BigEndian.Uint32(body[8:12]))
	if countValue > soakCanaryGitMaxEntries {
		return nil, errors.New("soak canary Git index entry count exceeds limit")
	}
	count, err := checkedSoakCanaryGitInt(countValue)
	if err != nil {
		return nil, err
	}
	entries := make([]soakCanaryGitIndexEntry, 0, count)
	offset := uint64(12)
	previousPath := ""
	for index := uint64(0); index < countValue; index++ {
		entryStart := offset
		entryLeft, entryRight, boundsErr := checkedSoakCanaryGitSliceBounds(
			checksumStart,
			offset,
			62,
		)
		if boundsErr != nil {
			return nil, errors.New("soak canary Git index entry is truncated")
		}
		entryBody := body[entryLeft:entryRight]
		mode := binary.BigEndian.Uint32(entryBody[24:28])
		var oid soakCanaryGitOID
		copy(oid[:], entryBody[40:60])
		flags := binary.BigEndian.Uint16(entryBody[60:62])
		offset, err = checkedSoakCanaryGitAdd(offset, 62)
		if err != nil {
			return nil, err
		}
		if flags&0x4000 != 0 {
			return nil, errors.New("soak canary Git index extended entries are unsupported")
		}
		if flags&0x3000 != 0 {
			return nil, errors.New("soak canary Git index contains an unmerged stage")
		}
		nameLeft, nameRight, boundsErr := checkedSoakCanaryGitSliceBounds(
			checksumStart,
			offset,
			checksumStart-offset,
		)
		if boundsErr != nil {
			return nil, errors.New("soak canary Git index path bounds are invalid")
		}
		nameEnd := bytes.IndexByte(body[nameLeft:nameRight], 0)
		if nameEnd < 0 {
			return nil, errors.New("soak canary Git index path is unterminated")
		}
		nameLength := uint64(nameEnd)
		pathLeft, pathRight, boundsErr := checkedSoakCanaryGitSliceBounds(
			checksumStart,
			offset,
			nameLength,
		)
		if boundsErr != nil {
			return nil, errors.New("soak canary Git index path bounds are invalid")
		}
		pathBytes := body[pathLeft:pathRight]
		if flags&0x0fff != 0x0fff && int(flags&0x0fff) != len(pathBytes) {
			return nil, errors.New("soak canary Git index path length is inconsistent")
		}
		path := string(pathBytes)
		if !validSoakCanaryGitPath(path) || (previousPath != "" && path <= previousPath) {
			return nil, errors.New("soak canary Git index path is unsafe or unsorted")
		}
		if !validSoakCanaryGitFileMode(mode) {
			return nil, fmt.Errorf("soak canary Git index mode %o is unsupported", mode)
		}
		previousPath = path
		offset, err = checkedSoakCanaryGitAdd(offset, nameLength)
		if err != nil {
			return nil, err
		}
		offset, err = checkedSoakCanaryGitAdd(offset, 1)
		if err != nil {
			return nil, err
		}
		entryLength := offset - entryStart
		paddedLength, err := checkedSoakCanaryGitAdd(entryLength, 7)
		if err != nil {
			return nil, err
		}
		paddedLength &^= 7
		paddedEnd, err := checkedSoakCanaryGitAdd(entryStart, paddedLength)
		if err != nil || paddedEnd > checksumStart {
			return nil, errors.New("soak canary Git index padding is truncated")
		}
		paddingLeft, paddingRight, boundsErr := checkedSoakCanaryGitSliceBounds(
			checksumStart,
			offset,
			paddedEnd-offset,
		)
		if boundsErr != nil {
			return nil, errors.New("soak canary Git index padding is truncated")
		}
		for _, padding := range body[paddingLeft:paddingRight] {
			if padding != 0 {
				return nil, errors.New("soak canary Git index padding is corrupt")
			}
		}
		offset = paddedEnd
		entries = append(entries, soakCanaryGitIndexEntry{path: path, mode: mode, oid: oid})
	}
	seenTree := false
	for offset < checksumStart {
		headerLeft, headerRight, boundsErr := checkedSoakCanaryGitSliceBounds(
			checksumStart,
			offset,
			8,
		)
		if boundsErr != nil {
			return nil, errors.New("soak canary Git index extension is truncated")
		}
		header := body[headerLeft:headerRight]
		signature := string(header[:4])
		size := uint64(binary.BigEndian.Uint32(header[4:8]))
		offset, err = checkedSoakCanaryGitAdd(offset, 8)
		if err != nil || offset > checksumStart || size > checksumStart-offset {
			return nil, errors.New("soak canary Git index extension size is invalid")
		}
		if signature != "TREE" || seenTree {
			return nil, fmt.Errorf("soak canary Git index extension %q is unsupported", signature)
		}
		seenTree = true
		offset, err = checkedSoakCanaryGitAdd(offset, size)
		if err != nil {
			return nil, err
		}
	}
	return entries, nil
}

func validSoakCanaryGitPath(path string) bool {
	if path == "" || strings.HasPrefix(path, "/") || strings.HasSuffix(path, "/") ||
		strings.ContainsAny(path, "\x00\\") || !utf8.ValidString(path) {
		return false
	}
	for _, component := range strings.Split(path, "/") {
		if component == "" || component == "." || component == ".." || component == ".git" {
			return false
		}
	}
	return filepath.ToSlash(filepath.Clean(filepath.FromSlash(path))) == path
}

func validSoakCanaryGitFileMode(mode uint32) bool {
	return mode == 0o100644 || mode == 0o100755 || mode == 0o120000
}

func loadSoakCanaryGitHEADTree(
	store soakCanaryGitObjectStore,
	head soakCanaryGitOID,
) (map[string]soakCanaryGitTreeEntry, error) {
	commit, err := store.object(head, 0)
	if err != nil {
		return nil, fmt.Errorf("read soak canary Git HEAD commit: %w", err)
	}
	if commit.kind != "commit" {
		return nil, errors.New("soak canary Git HEAD does not identify a commit")
	}
	lineEnd := bytes.IndexByte(commit.data, '\n')
	if lineEnd < 0 || !bytes.HasPrefix(commit.data[:lineEnd], []byte("tree ")) {
		return nil, errors.New("soak canary Git HEAD commit has no tree")
	}
	treeOID, err := parseSoakCanaryGitOID(string(commit.data[len("tree "):lineEnd]))
	if err != nil {
		return nil, errors.New("soak canary Git HEAD tree is invalid")
	}
	entries := map[string]soakCanaryGitTreeEntry{}
	if err := loadSoakCanaryGitTree(store, treeOID, "", entries, 0); err != nil {
		return nil, err
	}
	return entries, nil
}

func loadSoakCanaryGitTree(
	store soakCanaryGitObjectStore,
	oid soakCanaryGitOID,
	prefix string,
	entries map[string]soakCanaryGitTreeEntry,
	depth int,
) error {
	if depth >= soakCanaryGitMaxDeltaDepth || len(entries) > soakCanaryGitMaxEntries {
		return errors.New("soak canary Git tree exceeds bounded limits")
	}
	object, err := store.object(oid, 0)
	if err != nil {
		return fmt.Errorf("read soak canary Git tree %s: %w", oid, err)
	}
	if object.kind != "tree" {
		return errors.New("soak canary Git tree object has wrong type")
	}
	offset := 0
	for offset < len(object.data) {
		space := bytes.IndexByte(object.data[offset:], ' ')
		if space <= 0 {
			return errors.New("soak canary Git tree mode is malformed")
		}
		space += offset
		modeValue, err := strconv.ParseUint(string(object.data[offset:space]), 8, 32)
		if err != nil {
			return errors.New("soak canary Git tree mode is invalid")
		}
		nameStart := space + 1
		nameEnd := bytes.IndexByte(object.data[nameStart:], 0)
		if nameEnd <= 0 {
			return errors.New("soak canary Git tree name is malformed")
		}
		nameEnd += nameStart
		if nameEnd+1+sha1.Size > len(object.data) {
			return errors.New("soak canary Git tree object ID is truncated")
		}
		name := string(object.data[nameStart:nameEnd])
		if strings.Contains(name, "/") || !validSoakCanaryGitPath(name) {
			return errors.New("soak canary Git tree name is unsafe")
		}
		var child soakCanaryGitOID
		copy(child[:], object.data[nameEnd+1:nameEnd+1+sha1.Size])
		path := name
		if prefix != "" {
			path = prefix + "/" + name
		}
		mode := uint32(modeValue)
		switch mode {
		case 0o40000:
			if err := loadSoakCanaryGitTree(store, child, path, entries, depth+1); err != nil {
				return err
			}
		case 0o100644, 0o100755, 0o120000:
			if _, exists := entries[path]; exists {
				return errors.New("soak canary Git tree contains a duplicate path")
			}
			entries[path] = soakCanaryGitTreeEntry{mode: mode, oid: child}
		default:
			return fmt.Errorf("soak canary Git tree mode %o is unsupported", mode)
		}
		offset = nameEnd + 1 + sha1.Size
		if len(entries) > soakCanaryGitMaxEntries {
			return errors.New("soak canary Git tree entry count exceeds limit")
		}
	}
	return nil
}

func compareSoakCanaryGitIndexToHEAD(
	index []soakCanaryGitIndexEntry,
	tree map[string]soakCanaryGitTreeEntry,
) error {
	if len(index) != len(tree) {
		return errors.New("soak canary Git index differs from HEAD")
	}
	for _, entry := range index {
		head, exists := tree[entry.path]
		if !exists || entry.mode != head.mode || entry.oid != head.oid {
			return fmt.Errorf("soak canary Git index differs from HEAD at %s", entry.path)
		}
	}
	return nil
}

func verifySoakCanaryGitWorktree(
	root string,
	index []soakCanaryGitIndexEntry,
	checkExecutableMode bool,
) error {
	for _, entry := range index {
		path := filepath.Join(root, filepath.FromSlash(entry.path))
		info, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("soak canary Git worktree path %s is missing", entry.path)
		}
		var oid soakCanaryGitOID
		switch entry.mode {
		case 0o100644, 0o100755:
			if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("soak canary Git worktree mode mismatch at %s", entry.path)
			}
			if checkExecutableMode && runtime.GOOS != "windows" {
				executable := info.Mode().Perm()&0o111 != 0
				if executable != (entry.mode == 0o100755) {
					return fmt.Errorf("soak canary Git executable mode mismatch at %s", entry.path)
				}
			}
			oid, err = hashSoakCanaryGitRegularBlob(path, info.Size())
		case 0o120000:
			if info.Mode()&os.ModeSymlink == 0 {
				return fmt.Errorf("soak canary Git symlink mode mismatch at %s", entry.path)
			}
			target, readErr := os.Readlink(path)
			if readErr != nil {
				err = readErr
			} else {
				oid = hashSoakCanaryGitBlob([]byte(target))
			}
		}
		if err != nil {
			return fmt.Errorf("hash soak canary Git worktree path %s: %w", entry.path, err)
		}
		if oid != entry.oid {
			return fmt.Errorf("soak canary Git worktree content mismatch at %s", entry.path)
		}
	}
	return nil
}

func rejectSoakCanaryGitUntracked(root string, index []soakCanaryGitIndexEntry) error {
	tracked := make(map[string]bool, len(index))
	seen := make(map[string]bool, len(index))
	for _, entry := range index {
		tracked[entry.path] = true
	}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		relative = filepath.ToSlash(relative)
		if relative == ".git" {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
			return fmt.Errorf("soak canary Git worktree contains unsupported path %s", relative)
		}
		if !tracked[relative] {
			return fmt.Errorf("soak canary Git worktree contains untracked path %s", relative)
		}
		seen[relative] = true
		return nil
	})
	if err != nil {
		return err
	}
	if len(seen) != len(tracked) {
		return errors.New("soak canary Git worktree changed while verifying tracked paths")
	}
	return nil
}

func soakCanaryGitCoreFileMode(layout soakCanaryGitLayout) (bool, error) {
	fileMode := runtime.GOOS != "windows"
	for _, path := range []string{
		filepath.Join(layout.commonDir, "config"),
		filepath.Join(layout.gitDir, "config.worktree"),
	} {
		root := layout.commonDir
		if strings.HasPrefix(path, layout.gitDir+string(os.PathSeparator)) {
			root = layout.gitDir
		}
		body, err := readSoakCanaryGitMetadataFile(root, path, 1<<20)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return false, err
		}
		section := ""
		for _, rawLine := range strings.Split(string(body), "\n") {
			line := strings.TrimSpace(rawLine)
			if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
				continue
			}
			if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
				section = strings.ToLower(strings.TrimSpace(line[1 : len(line)-1]))
				continue
			}
			if section != "core" {
				continue
			}
			key, value, found := strings.Cut(line, "=")
			if !found || !strings.EqualFold(strings.TrimSpace(key), "filemode") {
				continue
			}
			parsed, parseErr := strconv.ParseBool(strings.TrimSpace(value))
			if parseErr != nil {
				return false, errors.New("soak canary Git core.filemode is malformed")
			}
			fileMode = parsed
		}
	}
	return fileMode, nil
}

func hashSoakCanaryGitRegularBlob(path string, size int64) (soakCanaryGitOID, error) {
	if size < 0 || size > soakCanarySnapshotMaximumFileBytes {
		return soakCanaryGitOID{}, errors.New("soak canary Git worktree file exceeds limit")
	}
	file, err := os.Open(path)
	if err != nil {
		return soakCanaryGitOID{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != size {
		return soakCanaryGitOID{}, errors.New("soak canary Git worktree file changed while hashing")
	}
	hasher := sha1.New()
	_, _ = fmt.Fprintf(hasher, "blob %d\x00", size)
	written, err := io.CopyN(hasher, file, size)
	if err != nil || written != size {
		return soakCanaryGitOID{}, errors.New("soak canary Git worktree file is truncated")
	}
	var extra [1]byte
	if count, readErr := file.Read(extra[:]); count != 0 || (readErr != nil && readErr != io.EOF) {
		return soakCanaryGitOID{}, errors.New("soak canary Git worktree file changed while hashing")
	}
	var oid soakCanaryGitOID
	copy(oid[:], hasher.Sum(nil))
	return oid, nil
}

func hashSoakCanaryGitBlob(body []byte) soakCanaryGitOID {
	hasher := sha1.New()
	_, _ = fmt.Fprintf(hasher, "blob %d\x00", len(body))
	_, _ = hasher.Write(body)
	var oid soakCanaryGitOID
	copy(oid[:], hasher.Sum(nil))
	return oid
}

func parseSoakCanaryGitOID(value string) (soakCanaryGitOID, error) {
	var oid soakCanaryGitOID
	if len(value) != hex.EncodedLen(len(oid)) {
		return oid, errors.New("Git object ID has wrong length")
	}
	body, err := hex.DecodeString(value)
	if err != nil {
		return oid, err
	}
	copy(oid[:], body)
	return oid, nil
}

func (oid soakCanaryGitOID) String() string {
	return hex.EncodeToString(oid[:])
}

func (store soakCanaryGitObjectStore) object(
	oid soakCanaryGitOID,
	depth int,
) (soakCanaryGitObject, error) {
	if depth >= soakCanaryGitMaxDeltaDepth {
		return soakCanaryGitObject{}, errors.New("soak canary Git object delta depth exceeds limit")
	}
	object, err := store.looseObject(oid)
	if err == nil {
		return object, nil
	}
	if !os.IsNotExist(err) {
		return soakCanaryGitObject{}, err
	}
	object, err = store.packedObject(oid, depth)
	if err != nil {
		return soakCanaryGitObject{}, err
	}
	if hashSoakCanaryGitObject(object.kind, object.data) != oid {
		return soakCanaryGitObject{}, errors.New("soak canary packed Git object hash mismatch")
	}
	return object, nil
}

func (store soakCanaryGitObjectStore) looseObject(
	oid soakCanaryGitOID,
) (soakCanaryGitObject, error) {
	value := oid.String()
	path := filepath.Join(store.commonDir, "objects", value[:2], value[2:])
	file, _, err := openSoakCanaryGitMetadataRegular(store.commonDir, path)
	if err != nil {
		return soakCanaryGitObject{}, err
	}
	defer file.Close()
	reader, err := zlib.NewReader(file)
	if err != nil {
		return soakCanaryGitObject{}, errors.New("soak canary loose Git object is corrupt")
	}
	defer reader.Close()
	var header [256]byte
	headerLength := 0
	for headerLength < len(header) {
		if _, err := io.ReadFull(reader, header[headerLength:headerLength+1]); err != nil {
			return soakCanaryGitObject{}, errors.New("soak canary loose Git object header is corrupt")
		}
		if header[headerLength] == 0 {
			break
		}
		headerLength++
	}
	if headerLength == 0 || headerLength >= len(header) {
		return soakCanaryGitObject{}, errors.New("soak canary loose Git object header is corrupt")
	}
	fields := strings.Fields(string(header[:headerLength]))
	if len(fields) != 2 {
		return soakCanaryGitObject{}, errors.New("soak canary loose Git object header is invalid")
	}
	size, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil || size > soakCanaryGitMaxObjectBytes {
		return soakCanaryGitObject{}, errors.New("soak canary loose Git object size is invalid")
	}
	if err := store.budget.reserve(size); err != nil {
		return soakCanaryGitObject{}, err
	}
	sizeInt, err := checkedSoakCanaryGitInt(size)
	if err != nil {
		return soakCanaryGitObject{}, err
	}
	data := make([]byte, sizeInt)
	if _, err := io.ReadFull(reader, data); err != nil {
		return soakCanaryGitObject{}, errors.New("soak canary loose Git object is truncated")
	}
	var extra [1]byte
	if count, readErr := reader.Read(extra[:]); count != 0 || (readErr != nil && readErr != io.EOF) {
		return soakCanaryGitObject{}, errors.New("soak canary loose Git object size is inconsistent")
	}
	object := soakCanaryGitObject{kind: fields[0], data: data}
	if hashSoakCanaryGitObject(object.kind, object.data) != oid {
		return soakCanaryGitObject{}, errors.New("soak canary loose Git object hash mismatch")
	}
	return object, nil
}

func (store soakCanaryGitObjectStore) packedObject(
	oid soakCanaryGitOID,
	depth int,
) (soakCanaryGitObject, error) {
	packDirectory := filepath.Join(store.commonDir, "objects", "pack")
	if err := validateSoakCanaryGitMetadataDirectory(store.commonDir, packDirectory); err != nil {
		return soakCanaryGitObject{}, err
	}
	children, err := os.ReadDir(packDirectory)
	if err != nil {
		return soakCanaryGitObject{}, fmt.Errorf("read soak canary Git pack directory: %w", err)
	}
	var indexes []string
	for _, child := range children {
		if !child.Type().IsRegular() || !strings.HasSuffix(child.Name(), ".idx") {
			continue
		}
		indexes = append(indexes, filepath.Join(packDirectory, child.Name()))
	}
	sort.Strings(indexes)
	if len(indexes) > soakCanaryGitMaxPacks {
		return soakCanaryGitObject{}, errors.New("soak canary Git pack count exceeds limit")
	}
	for _, indexPath := range indexes {
		packPath, offset, found, err := findSoakCanaryGitPackedOffset(indexPath, oid)
		if err != nil {
			return soakCanaryGitObject{}, err
		}
		if found {
			return store.packObjectAt(packPath, offset, depth, map[int64]bool{})
		}
	}
	return soakCanaryGitObject{}, fmt.Errorf("soak canary Git object %s is missing", oid)
}

func findSoakCanaryGitPackedOffset(
	indexPath string,
	oid soakCanaryGitOID,
) (string, int64, bool, error) {
	commonDir := filepath.Dir(filepath.Dir(filepath.Dir(indexPath)))
	body, err := readSoakCanaryGitMetadataFile(
		commonDir,
		indexPath,
		soakCanaryGitMaxPackIndex,
	)
	if err != nil {
		return "", 0, false, err
	}
	total := uint64(len(body))
	if total < 8+256*4+40 ||
		!bytes.Equal(body[:4], []byte{0xff, 0x74, 0x4f, 0x63}) ||
		binary.BigEndian.Uint32(body[4:8]) != 2 {
		return "", 0, false, errors.New("soak canary Git pack index version is unsupported")
	}
	checksumStart := total - sha1.Size
	checksumStartInt, checksumEndInt, err := checkedSoakCanaryGitSliceBounds(
		total,
		checksumStart,
		sha1.Size,
	)
	if err != nil {
		return "", 0, false, err
	}
	indexChecksum := sha1.Sum(body[:checksumStartInt])
	if !bytes.Equal(indexChecksum[:], body[checksumStartInt:checksumEndInt]) {
		return "", 0, false, errors.New("soak canary Git pack index checksum mismatch")
	}
	fanout := body[8 : 8+256*4]
	for index := 1; index < 256; index++ {
		if binary.BigEndian.Uint32(fanout[index*4:(index+1)*4]) <
			binary.BigEndian.Uint32(fanout[(index-1)*4:index*4]) {
			return "", 0, false, errors.New("soak canary Git pack index fanout is corrupt")
		}
	}
	countValue := uint64(binary.BigEndian.Uint32(fanout[255*4 : 256*4]))
	if countValue > soakCanaryGitMaxEntries*10 {
		return "", 0, false, errors.New("soak canary Git pack index entry count exceeds limit")
	}
	namesStart := uint64(8 + 256*4)
	namesBytes, err := checkedSoakCanaryGitMultiply(countValue, sha1.Size)
	if err != nil {
		return "", 0, false, err
	}
	crcStart, err := checkedSoakCanaryGitAdd(namesStart, namesBytes)
	if err != nil {
		return "", 0, false, err
	}
	crcBytes, err := checkedSoakCanaryGitMultiply(countValue, 4)
	if err != nil {
		return "", 0, false, err
	}
	offsetsStart, err := checkedSoakCanaryGitAdd(crcStart, crcBytes)
	if err != nil {
		return "", 0, false, err
	}
	offsetsBytes, err := checkedSoakCanaryGitMultiply(countValue, 4)
	if err != nil {
		return "", 0, false, err
	}
	largeStart, err := checkedSoakCanaryGitAdd(offsetsStart, offsetsBytes)
	if err != nil {
		return "", 0, false, err
	}
	trailerStart := total - 2*sha1.Size
	if largeStart > trailerStart {
		return "", 0, false, errors.New("soak canary Git pack index is truncated")
	}
	for index := uint64(1); index < countValue; index++ {
		previousLeft, previousRight, boundsErr := checkedSoakCanaryGitTableEntryBounds(
			total,
			namesStart,
			index-1,
			sha1.Size,
		)
		if boundsErr != nil {
			return "", 0, false, boundsErr
		}
		currentLeft, currentRight, boundsErr := checkedSoakCanaryGitTableEntryBounds(
			total,
			namesStart,
			index,
			sha1.Size,
		)
		if boundsErr != nil {
			return "", 0, false, boundsErr
		}
		previous := body[previousLeft:previousRight]
		current := body[currentLeft:currentRight]
		if bytes.Compare(previous, current) >= 0 {
			return "", 0, false, errors.New("soak canary Git pack index names are unsorted")
		}
	}
	leftIndex, rightIndex := uint64(0), countValue
	for leftIndex < rightIndex {
		middle := leftIndex + (rightIndex-leftIndex)/2
		left, right, boundsErr := checkedSoakCanaryGitTableEntryBounds(
			total,
			namesStart,
			middle,
			sha1.Size,
		)
		if boundsErr != nil {
			return "", 0, false, boundsErr
		}
		if bytes.Compare(body[left:right], oid[:]) < 0 {
			leftIndex = middle + 1
		} else {
			rightIndex = middle
		}
	}
	if leftIndex >= countValue {
		return "", 0, false, nil
	}
	index := leftIndex
	nameLeft, nameRight, err := checkedSoakCanaryGitTableEntryBounds(
		total,
		namesStart,
		index,
		sha1.Size,
	)
	if err != nil {
		return "", 0, false, err
	}
	if !bytes.Equal(body[nameLeft:nameRight], oid[:]) {
		return "", 0, false, nil
	}
	offsetLeft, offsetRight, err := checkedSoakCanaryGitTableEntryBounds(
		total,
		offsetsStart,
		index,
		4,
	)
	if err != nil {
		return "", 0, false, err
	}
	value := binary.BigEndian.Uint32(body[offsetLeft:offsetRight])
	var offset int64
	if value&0x80000000 == 0 {
		offset = int64(value)
	} else {
		largeOffset, multiplyErr := checkedSoakCanaryGitMultiply(
			uint64(value&0x7fffffff),
			8,
		)
		if multiplyErr != nil {
			return "", 0, false, multiplyErr
		}
		position, addErr := checkedSoakCanaryGitAdd(largeStart, largeOffset)
		if addErr != nil || position > trailerStart || 8 > trailerStart-position {
			return "", 0, false, errors.New("soak canary Git pack large offset is invalid")
		}
		left, right, boundsErr := checkedSoakCanaryGitSliceBounds(total, position, 8)
		if boundsErr != nil {
			return "", 0, false, boundsErr
		}
		offset, err = checkedSoakCanaryGitInt64(binary.BigEndian.Uint64(body[left:right]))
		if err != nil {
			return "", 0, false, err
		}
	}
	packPath := strings.TrimSuffix(indexPath, ".idx") + ".pack"
	pack, _, err := openSoakCanaryGitMetadataRegular(commonDir, packPath)
	if err != nil {
		return "", 0, false, err
	}
	defer pack.Close()
	info, err := pack.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 12+sha1.Size {
		return "", 0, false, errors.New("soak canary Git pack is unsafe")
	}
	packEnd, err := checkedSoakCanaryGitSubtractInt64(info.Size(), sha1.Size)
	if err != nil {
		return "", 0, false, err
	}
	var header [12]byte
	if _, err := pack.ReadAt(header[:], 0); err != nil ||
		!bytes.Equal(header[:4], []byte("PACK")) ||
		(binary.BigEndian.Uint32(header[4:8]) != 2 &&
			binary.BigEndian.Uint32(header[4:8]) != 3) {
		return "", 0, false, errors.New("soak canary Git pack header is invalid")
	}
	var trailer [sha1.Size]byte
	if _, err := pack.ReadAt(trailer[:], packEnd); err != nil {
		return "", 0, false, err
	}
	packChecksumLeft, packChecksumRight, err := checkedSoakCanaryGitSliceBounds(
		total,
		trailerStart,
		sha1.Size,
	)
	if err != nil {
		return "", 0, false, err
	}
	if !bytes.Equal(trailer[:], body[packChecksumLeft:packChecksumRight]) {
		return "", 0, false, errors.New("soak canary Git pack checksum binding mismatch")
	}
	if offset < 12 || offset >= packEnd {
		return "", 0, false, errors.New("soak canary Git pack offset is invalid")
	}
	return packPath, offset, true, nil
}

func (store soakCanaryGitObjectStore) packObjectAt(
	packPath string,
	objectOffset int64,
	depth int,
	seen map[int64]bool,
) (soakCanaryGitObject, error) {
	if depth >= soakCanaryGitMaxDeltaDepth || seen[objectOffset] {
		return soakCanaryGitObject{}, errors.New("soak canary Git pack delta is cyclic or too deep")
	}
	seen[objectOffset] = true
	defer delete(seen, objectOffset)
	file, _, err := openSoakCanaryGitMetadataRegular(store.commonDir, packPath)
	if err != nil {
		return soakCanaryGitObject{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return soakCanaryGitObject{}, err
	}
	packEnd, err := checkedSoakCanaryGitSubtractInt64(info.Size(), sha1.Size)
	if err != nil || objectOffset < 12 || objectOffset >= packEnd {
		return soakCanaryGitObject{}, errors.New("soak canary Git pack object offset is invalid")
	}
	position := objectOffset
	first, err := readSoakCanaryGitByteAt(file, &position)
	if err != nil {
		return soakCanaryGitObject{}, err
	}
	objectType := (first >> 4) & 7
	size := uint64(first & 0x0f)
	shift := uint(4)
	for first&0x80 != 0 {
		first, err = readSoakCanaryGitByteAt(file, &position)
		if err != nil {
			return soakCanaryGitObject{}, err
		}
		if shift >= 64 {
			return soakCanaryGitObject{}, errors.New("soak canary Git pack object size exceeds limit")
		}
		part := uint64(first & 0x7f)
		if part > math.MaxUint64>>shift {
			return soakCanaryGitObject{}, errors.New("soak canary Git pack object size overflows")
		}
		size |= part << shift
		shift += 7
	}
	if size > soakCanaryGitMaxObjectBytes {
		return soakCanaryGitObject{}, errors.New("soak canary Git pack object size exceeds limit")
	}
	var baseOffset int64
	var baseOID soakCanaryGitOID
	switch objectType {
	case 6:
		value, err := readSoakCanaryGitByteAt(file, &position)
		if err != nil {
			return soakCanaryGitObject{}, err
		}
		distance := uint64(value & 0x7f)
		for value&0x80 != 0 {
			value, err = readSoakCanaryGitByteAt(file, &position)
			if err != nil {
				return soakCanaryGitObject{}, err
			}
			incremented, addErr := checkedSoakCanaryGitAdd(distance, 1)
			if addErr != nil || incremented > math.MaxUint64>>7 {
				return soakCanaryGitObject{}, errors.New("soak canary Git pack delta offset overflows")
			}
			distance = (incremented << 7) | uint64(value&0x7f)
		}
		distanceInt64, convertErr := checkedSoakCanaryGitInt64(distance)
		if convertErr != nil {
			return soakCanaryGitObject{}, convertErr
		}
		baseOffset, err = checkedSoakCanaryGitSubtractInt64(objectOffset, distanceInt64)
		if baseOffset < 12 {
			return soakCanaryGitObject{}, errors.New("soak canary Git pack delta base offset is invalid")
		}
	case 7:
		if position > packEnd || int64(sha1.Size) > packEnd-position {
			return soakCanaryGitObject{}, errors.New("soak canary Git pack ref delta base is truncated")
		}
		if _, err := file.ReadAt(baseOID[:], position); err != nil {
			return soakCanaryGitObject{}, err
		}
		position, err = checkedSoakCanaryGitAddInt64(position, sha1.Size)
		if err != nil {
			return soakCanaryGitObject{}, err
		}
	case 1, 2, 3, 4:
	default:
		return soakCanaryGitObject{}, errors.New("soak canary Git pack object type is unsupported")
	}
	if position > packEnd {
		return soakCanaryGitObject{}, errors.New("soak canary Git pack object data offset is invalid")
	}
	sectionLength := packEnd - position
	reader, err := zlib.NewReader(io.NewSectionReader(
		file,
		position,
		sectionLength,
	))
	if err != nil {
		return soakCanaryGitObject{}, errors.New("soak canary Git pack object compression is corrupt")
	}
	if err := store.budget.reserve(size); err != nil {
		reader.Close()
		return soakCanaryGitObject{}, err
	}
	sizeInt, err := checkedSoakCanaryGitInt(size)
	if err != nil {
		reader.Close()
		return soakCanaryGitObject{}, err
	}
	data := make([]byte, sizeInt)
	_, readErr := io.ReadFull(reader, data)
	var extra [1]byte
	extraCount, extraErr := reader.Read(extra[:])
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil || extraCount != 0 ||
		(extraErr != nil && extraErr != io.EOF) {
		return soakCanaryGitObject{}, errors.New("soak canary Git pack object data is corrupt")
	}
	switch objectType {
	case 1, 2, 3, 4:
		kinds := map[byte]string{1: "commit", 2: "tree", 3: "blob", 4: "tag"}
		return soakCanaryGitObject{kind: kinds[objectType], data: data}, nil
	case 6, 7:
		var base soakCanaryGitObject
		if objectType == 6 {
			base, err = store.packObjectAt(packPath, baseOffset, depth+1, seen)
		} else {
			base, err = store.object(baseOID, depth+1)
		}
		if err != nil {
			return soakCanaryGitObject{}, err
		}
		result, err := applySoakCanaryGitDelta(base.data, data, store.budget)
		if err != nil {
			return soakCanaryGitObject{}, err
		}
		return soakCanaryGitObject{kind: base.kind, data: result}, nil
	}
	return soakCanaryGitObject{}, errors.New("unreachable soak canary Git pack object type")
}

func readSoakCanaryGitByteAt(file *os.File, offset *int64) (byte, error) {
	var body [1]byte
	if _, err := file.ReadAt(body[:], *offset); err != nil {
		return 0, err
	}
	next, err := checkedSoakCanaryGitAddInt64(*offset, 1)
	if err != nil {
		return 0, err
	}
	*offset = next
	return body[0], nil
}

func applySoakCanaryGitDelta(
	base, delta []byte,
	budget *soakCanaryGitBudget,
) ([]byte, error) {
	baseSize, offset, err := readSoakCanaryGitDeltaSize(delta, 0)
	if err != nil || baseSize != uint64(len(base)) {
		return nil, errors.New("soak canary Git delta base size is invalid")
	}
	resultSize, offset, err := readSoakCanaryGitDeltaSize(delta, offset)
	if err != nil || resultSize > soakCanaryGitMaxObjectBytes {
		return nil, errors.New("soak canary Git delta result size is invalid")
	}
	if err := budget.reserve(resultSize); err != nil {
		return nil, err
	}
	resultCapacity, err := checkedSoakCanaryGitInt(resultSize)
	if err != nil {
		return nil, err
	}
	result := make([]byte, 0, resultCapacity)
	deltaLength := uint64(len(delta))
	for offset < deltaLength {
		command := delta[offset]
		offset++
		if command&0x80 == 0 {
			count := uint64(command)
			start, end, boundsErr := checkedSoakCanaryGitSliceBounds(deltaLength, offset, count)
			if count == 0 || boundsErr != nil || count > resultSize ||
				uint64(len(result)) > resultSize-count {
				return nil, errors.New("soak canary Git delta literal is invalid")
			}
			result = append(result, delta[start:end]...)
			offset, err = checkedSoakCanaryGitAdd(offset, count)
			if err != nil {
				return nil, err
			}
			continue
		}
		var copyOffset uint64
		var copySize uint64
		for bit := byte(0); bit < 4; bit++ {
			if command&(1<<bit) != 0 {
				if offset >= deltaLength {
					return nil, errors.New("soak canary Git delta copy offset is truncated")
				}
				copyOffset |= uint64(delta[offset]) << (8 * bit)
				offset++
			}
		}
		for bit := byte(0); bit < 3; bit++ {
			if command&(1<<(4+bit)) != 0 {
				if offset >= deltaLength {
					return nil, errors.New("soak canary Git delta copy size is truncated")
				}
				copySize |= uint64(delta[offset]) << (8 * bit)
				offset++
			}
		}
		if copySize == 0 {
			copySize = 0x10000
		}
		start, end, boundsErr := checkedSoakCanaryGitSliceBounds(
			uint64(len(base)),
			copyOffset,
			copySize,
		)
		if boundsErr != nil || copySize > resultSize ||
			uint64(len(result)) > resultSize-copySize {
			return nil, errors.New("soak canary Git delta copy is out of bounds")
		}
		result = append(result, base[start:end]...)
	}
	if uint64(len(result)) != resultSize {
		return nil, errors.New("soak canary Git delta result length mismatch")
	}
	return result, nil
}

func readSoakCanaryGitDeltaSize(body []byte, offset uint64) (uint64, uint64, error) {
	var value uint64
	var shift uint
	length := uint64(len(body))
	for {
		if offset >= length || shift >= 64 {
			return 0, offset, errors.New("soak canary Git delta size is truncated")
		}
		current := body[offset]
		offset++
		part := uint64(current & 0x7f)
		if part > math.MaxUint64>>shift {
			return 0, offset, errors.New("soak canary Git delta size overflows")
		}
		value |= part << shift
		if current&0x80 == 0 {
			return value, offset, nil
		}
		shift += 7
	}
}

func hashSoakCanaryGitObject(kind string, data []byte) soakCanaryGitOID {
	hasher := sha1.New()
	_, _ = fmt.Fprintf(hasher, "%s %d\x00", kind, len(data))
	_, _ = hasher.Write(data)
	var oid soakCanaryGitOID
	copy(oid[:], hasher.Sum(nil))
	return oid
}
