package mission

import (
	"context"
	"crypto/sha1"
	"encoding/binary"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSoakCanaryActivationRejectsUncleanOrMismatchedGitRepository(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *soakCanaryTestFixture)
	}{
		{
			name: "tracked modification",
			mutate: func(t *testing.T, fixture *soakCanaryTestFixture) {
				mustWriteSoakCanaryTestFile(
					t,
					filepath.Join(fixture.request.RepositoryRoot, "tracked.txt"),
					[]byte("modified\n"),
				)
			},
		},
		{
			name: "staged modification",
			mutate: func(t *testing.T, fixture *soakCanaryTestFixture) {
				mustWriteSoakCanaryTestFile(
					t,
					filepath.Join(fixture.request.RepositoryRoot, "tracked.txt"),
					[]byte("staged\n"),
				)
				runSoakCanaryTestGit(t, fixture.request.RepositoryRoot, "add", "tracked.txt")
			},
		},
		{
			name: "untracked file",
			mutate: func(t *testing.T, fixture *soakCanaryTestFixture) {
				mustWriteSoakCanaryTestFile(
					t,
					filepath.Join(fixture.request.RepositoryRoot, "untracked.txt"),
					[]byte("untracked\n"),
				)
			},
		},
		{
			name: "mismatched HEAD",
			mutate: func(t *testing.T, fixture *soakCanaryTestFixture) {
				fixture.request.Activation.SourceHead = strings.Repeat("f", 40)
				signSoakCanaryActivation(&fixture.request.Activation)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := validSoakCanaryFixture(t)
			head := initializeSoakCanaryGitRepository(t, fixture.request.RepositoryRoot)
			snapshot, err := BuildSoakCanaryRepositorySnapshot(fixture.request.RepositoryRoot)
			if err != nil {
				t.Fatal(err)
			}
			fixture.request.RepositorySnapshot = snapshot
			rebindSoakCanarySource(t, &fixture, head)
			fixture.request.GitVerifier = InProcessSoakCanaryGitVerifier{}

			clean := ValidateSoakCanaryActivation(fixture.request)
			if !clean.ActivationAllowed {
				t.Fatalf("clean Git repository rejected: %+v", clean)
			}

			test.mutate(t, &fixture)
			validation := ValidateSoakCanaryActivation(fixture.request)
			if validation.ActivationAllowed ||
				!containsSoakConflict(validation.ConflictCodes, "repository_git_state_mismatch") {
				t.Fatalf("unsafe Git state accepted: %+v", validation)
			}
		})
	}
}

func TestSoakCanaryGitVerifierRejectsUnsupportedOrCorruptIndex(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, []byte) []byte
		match  string
	}{
		{
			name: "unsupported version",
			mutate: func(t *testing.T, body []byte) []byte {
				t.Helper()
				binary.BigEndian.PutUint32(body[4:8], 4)
				return body
			},
			match: "version",
		},
		{
			name: "corrupt checksum",
			mutate: func(t *testing.T, body []byte) []byte {
				t.Helper()
				body[len(body)-1] ^= 0xff
				return body
			},
			match: "checksum",
		},
		{
			name: "unsupported extension",
			mutate: func(t *testing.T, body []byte) []byte {
				t.Helper()
				checksumStart := len(body) - sha1.Size
				extended := append([]byte(nil), body[:checksumStart]...)
				extended = append(extended, []byte("ZZZZ")...)
				extended = binary.BigEndian.AppendUint32(extended, 0)
				checksum := sha1.Sum(extended)
				return append(extended, checksum[:]...)
			},
			match: "extension",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			head := initializeSoakCanaryGitRepository(t, root)
			indexPath := filepath.Join(root, ".git", "index")
			body, err := os.ReadFile(indexPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(indexPath, test.mutate(t, body), 0o600); err != nil {
				t.Fatal(err)
			}
			err = (InProcessSoakCanaryGitVerifier{}).Verify(root, head)
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), test.match) {
				t.Fatalf("unsafe index accepted or wrong error: %v", err)
			}
		})
	}
}

func TestSoakCanaryGitVerifierAcceptsVersionThreeIndex(t *testing.T) {
	root := t.TempDir()
	head := initializeSoakCanaryGitRepository(t, root)
	indexPath := filepath.Join(root, ".git", "index")
	body, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	binary.BigEndian.PutUint32(body[4:8], 3)
	checksum := sha1.Sum(body[:len(body)-sha1.Size])
	copy(body[len(body)-sha1.Size:], checksum[:])
	if err := os.WriteFile(indexPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := (InProcessSoakCanaryGitVerifier{}).Verify(root, head); err != nil {
		t.Fatalf("valid Git index v3 rejected: %v", err)
	}
}

func TestSoakCanaryGitVerifierSupportsGitFile(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "worktree")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	head := initializeSoakCanaryGitRepository(t, root)
	gitDirectory := filepath.Join(parent, "metadata")
	if err := os.Rename(filepath.Join(root, ".git"), gitDirectory); err != nil {
		t.Fatal(err)
	}
	mustWriteSoakCanaryTestFile(
		t,
		filepath.Join(root, ".git"),
		[]byte("gitdir: "+gitDirectory+"\n"),
	)

	if err := (InProcessSoakCanaryGitVerifier{}).Verify(root, head); err != nil {
		t.Fatalf("valid Git file repository rejected: %v", err)
	}
}

func TestSoakCanaryGitVerifierSupportsLinkedWorktree(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "primary")
	linked := filepath.Join(parent, "linked")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	head := initializeSoakCanaryGitRepository(t, root)
	runSoakCanaryTestGit(t, root, "worktree", "add", "--detach", linked, "HEAD")

	if err := (InProcessSoakCanaryGitVerifier{}).Verify(linked, head); err != nil {
		t.Fatalf("valid linked Git worktree rejected: %v", err)
	}
}

func TestSoakCanaryGitVerifierSupportsPackedRefsAndObjects(t *testing.T) {
	root := t.TempDir()
	head := initializeSoakCanaryGitRepository(t, root)
	runSoakCanaryTestGit(t, root, "pack-refs", "--all", "--prune")
	runSoakCanaryTestGit(t, root, "gc", "--prune=now")

	if err := (InProcessSoakCanaryGitVerifier{}).Verify(root, head); err != nil {
		t.Fatalf("valid packed Git repository rejected: %v", err)
	}
}

func TestSoakCanaryGitVerifierRejectsUnsafeMetadataComponents(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, string, string)
	}{
		{
			name: "intermediate reference symlink",
			mutate: func(t *testing.T, root, _ string) {
				refs := filepath.Join(root, ".git", "refs")
				outside := filepath.Join(t.TempDir(), "refs")
				if err := os.Rename(refs, outside); err != nil {
					t.Fatal(err)
				}
				createTestSymlink(t, outside, refs)
			},
		},
		{
			name: "final index symlink",
			mutate: func(t *testing.T, root, _ string) {
				index := filepath.Join(root, ".git", "index")
				outside := filepath.Join(t.TempDir(), "index")
				if err := os.Rename(index, outside); err != nil {
					t.Fatal(err)
				}
				createTestSymlink(t, outside, index)
			},
		},
		{
			name: "loose object symlink",
			mutate: func(t *testing.T, root, head string) {
				object := filepath.Join(root, ".git", "objects", head[:2], head[2:])
				outside := filepath.Join(t.TempDir(), "object")
				if err := os.Rename(object, outside); err != nil {
					t.Fatal(err)
				}
				createTestSymlink(t, outside, object)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			head := initializeSoakCanaryGitRepository(t, root)
			test.mutate(t, root, head)
			err := (InProcessSoakCanaryGitVerifier{}).Verify(root, head)
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), "unsafe") {
				t.Fatalf("unsafe metadata accepted or wrong error: %v", err)
			}
		})
	}
}

func TestSoakCanaryGitVerifierRejectsPackedObjectSymlink(t *testing.T) {
	root := t.TempDir()
	head := initializeSoakCanaryGitRepository(t, root)
	runSoakCanaryTestGit(t, root, "gc", "--prune=now")
	packs, err := filepath.Glob(filepath.Join(root, ".git", "objects", "pack", "*.pack"))
	if err != nil || len(packs) != 1 {
		t.Fatalf("locate packed Git object: packs=%v err=%v", packs, err)
	}
	outside := filepath.Join(t.TempDir(), "object.pack")
	if err := os.Rename(packs[0], outside); err != nil {
		t.Fatal(err)
	}
	createTestSymlink(t, outside, packs[0])
	err = (InProcessSoakCanaryGitVerifier{}).Verify(root, head)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "unsafe") {
		t.Fatalf("unsafe packed-object symlink accepted or wrong error: %v", err)
	}
}

func TestSoakCanaryGitVerifierRejectsRepositoryRootSymlink(t *testing.T) {
	root := t.TempDir()
	head := initializeSoakCanaryGitRepository(t, root)
	link := filepath.Join(t.TempDir(), "repository")
	createTestSymlink(t, root, link)
	err := (InProcessSoakCanaryGitVerifier{}).Verify(link, head)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "root") {
		t.Fatalf("repository-root symlink accepted or wrong error: %v", err)
	}
}

func TestSoakCanaryGitObjectBudgetIsCumulativeAcrossDeltas(t *testing.T) {
	base := []byte("abcdefgh")
	delta := []byte{
		8,
		8,
		0x90, 8,
	}
	budget := newSoakCanaryGitBudget(15)
	if _, err := applySoakCanaryGitDelta(base, delta, budget); err != nil {
		t.Fatalf("first bounded delta failed: %v", err)
	}
	if _, err := applySoakCanaryGitDelta(base, delta, budget); err == nil ||
		!strings.Contains(strings.ToLower(err.Error()), "budget") {
		t.Fatalf("cumulative delta budget exhaustion accepted: %v", err)
	}
}

func TestSoakCanaryGitCheckedArithmeticRejectsMalformedBounds(t *testing.T) {
	if _, err := checkedSoakCanaryGitAdd(^uint64(0), 1); err == nil {
		t.Fatal("uint64 addition overflow accepted")
	}
	if _, err := checkedSoakCanaryGitMultiply(^uint64(0), 2); err == nil {
		t.Fatal("uint64 multiplication overflow accepted")
	}
	if _, _, err := checkedSoakCanaryGitSliceBounds(16, 15, 2); err == nil {
		t.Fatal("slice end overflow accepted")
	}
	if _, err := checkedSoakCanaryGitInt(^uint64(0)); err == nil {
		t.Fatal("platform int overflow accepted")
	}
	if _, err := checkedSoakCanaryGitSubtractInt64(12, 13); err == nil {
		t.Fatal("negative pack subtraction accepted")
	}
	if _, err := checkedSoakCanaryGitAddInt64(int64(^uint64(0)>>1), 1); err == nil {
		t.Fatal("int64 addition overflow accepted")
	}
}

func TestSoakCanaryRunningCheckpointFailureReapsChildAndRestartFailsClosed(t *testing.T) {
	requireTestSymlinkCapability(t)
	fixture := validSoakCanaryFixture(t)
	clock := &soakCanaryFakeClock{now: time.Date(2026, 7, 29, 21, 0, 0, 0, time.UTC)}
	process := &soakCanaryReapTrackingProcess{
		clock: clock,
		result: SoakCanaryProcessResult{
			ExitCode: 0, ElapsedMS: 5,
			Stdout: soakCanaryPassOutput(
				fixture.request.Catalog.Commands[0].TestName,
				fixture.request.Catalog.Commands[0].EffectiveRepeatCount,
			),
		},
	}
	var reservedCheckpoint []byte
	checkpointTarget := t.TempDir()
	fixture.request.Clock = clock
	fixture.request.Executor = &soakCanaryCheckpointFailureExecutor{
		process: process,
		beforeReturn: func() {
			var err error
			reservedCheckpoint, err = os.ReadFile(fixture.request.CheckpointPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(fixture.request.CheckpointPath); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(checkpointTarget, fixture.request.CheckpointPath); err != nil {
				t.Errorf("create checkpoint symlink: %v", err)
			}
		},
	}

	summary, err := RunSoakCanary(context.Background(), fixture.request)
	if err == nil || !containsSoakConflict(summary.ConflictCodes, "checkpoint_write_failed") {
		t.Fatalf("running checkpoint failure error=%v summary=%+v", err, summary)
	}
	if !process.waited || !process.canceledBeforeWait {
		t.Fatalf(
			"RunSoakCanary did not cancel then synchronously reap: waited=%t canceled_before_wait=%t",
			process.waited,
			process.canceledBeforeWait,
		)
	}
	if summary.ChildProcessLaunches != 1 || len(summary.Attempts) != 1 ||
		!summary.Attempts[0].ChildProcessLaunched ||
		summary.Attempts[0].ExecutionState != "completed" ||
		summary.Attempts[0].OutcomeClass != "running_checkpoint_write_failed_reaped" {
		t.Fatalf("observed child truth was lost: %+v", summary)
	}

	if err := os.Remove(fixture.request.CheckpointPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.request.CheckpointPath, reservedCheckpoint, 0o600); err != nil {
		t.Fatal(err)
	}
	restartExecutor := &soakCanaryFakeExecutor{clock: clock}
	fixture.request.Executor = restartExecutor
	restart, restartErr := RunSoakCanary(context.Background(), fixture.request)
	if restartErr == nil ||
		!containsSoakConflict(restart.ConflictCodes, "indeterminate_scale_launch") {
		t.Fatalf("restart escaped durable reservation: error=%v summary=%+v", restartErr, restart)
	}
	if restartExecutor.starts != 0 {
		t.Fatalf("restart launched duplicate/retry child: %d", restartExecutor.starts)
	}
}

func initializeSoakCanaryGitRepository(t *testing.T, root string) string {
	t.Helper()
	runSoakCanaryTestGit(t, root, "init")
	mustWriteSoakCanaryTestFile(t, filepath.Join(root, "tracked.txt"), []byte("tracked\n"))
	runSoakCanaryTestGit(t, root, "add", ".")
	runSoakCanaryTestGit(
		t,
		root,
		"-c", "user.name=AO Mission Test",
		"-c", "user.email=ao-mission@example.invalid",
		"commit", "-m", "fixture",
	)
	return strings.TrimSpace(runSoakCanaryTestGit(t, root, "rev-parse", "HEAD"))
}

func runSoakCanaryTestGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	commandArgs := append([]string{"-C", root}, args...)
	command := exec.Command("git", commandArgs...)
	command.Env = []string{
		"HOME=" + t.TempDir(),
		"PATH=" + os.Getenv("PATH"),
		"LC_ALL=C",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_DATE=2023-11-14T22:13:20Z",
		"GIT_COMMITTER_DATE=2023-11-14T22:13:20Z",
	}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return string(output)
}

type soakCanaryCheckpointFailureExecutor struct {
	process      SoakCanaryProcess
	beforeReturn func()
}

func (executor *soakCanaryCheckpointFailureExecutor) Start(
	ctx context.Context,
	_ SoakCanaryExecRequest,
) (SoakCanaryProcess, error) {
	if executor.process == nil {
		return nil, errors.New("missing reap-tracking process")
	}
	if process, ok := executor.process.(*soakCanaryReapTrackingProcess); ok {
		process.ctx = ctx
	}
	if executor.beforeReturn != nil {
		executor.beforeReturn()
	}
	return executor.process, nil
}

type soakCanaryReapTrackingProcess struct {
	clock              *soakCanaryFakeClock
	result             SoakCanaryProcessResult
	waited             bool
	ctx                context.Context
	canceledBeforeWait bool
}

func (*soakCanaryReapTrackingProcess) PID() int { return 43_001 }

func (process *soakCanaryReapTrackingProcess) Wait() SoakCanaryProcessResult {
	process.waited = true
	if process.ctx != nil {
		select {
		case <-process.ctx.Done():
			process.canceledBeforeWait = true
		default:
		}
	}
	if process.clock != nil {
		process.clock.advance(time.Duration(process.result.ElapsedMS) * time.Millisecond)
	}
	return process.result
}
