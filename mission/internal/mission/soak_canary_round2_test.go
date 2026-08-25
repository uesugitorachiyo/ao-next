package mission

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSoakCanaryRepositorySnapshotIncludesWorktreeAndExcludesGit(t *testing.T) {
	root := t.TempDir()
	mustWriteSoakCanaryTestFile(t, filepath.Join(root, "tracked.txt"), []byte("tracked\n"))
	mustWriteSoakCanaryTestFile(t, filepath.Join(root, ".git", "index"), []byte("ignored\n"))
	outside := filepath.Join(t.TempDir(), "outside.txt")
	mustWriteSoakCanaryTestFile(t, outside, []byte("outside-content-must-not-be-read\n"))
	createTestSymlink(t, outside, filepath.Join(root, "link"))

	first, err := BuildSoakCanaryRepositorySnapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildSoakCanaryRepositorySnapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) || first.SnapshotDigest == "" {
		t.Fatalf("snapshot is not deterministic:\nfirst=%+v\nsecond=%+v", first, second)
	}
	entries := map[string]SoakCanaryRepositorySnapshotEntry{}
	for _, entry := range first.Entries {
		entries[entry.Path] = entry
		if entry.Path == ".git" || strings.HasPrefix(entry.Path, ".git/") {
			t.Fatalf(".git content entered snapshot: %+v", first.Entries)
		}
	}
	for _, path := range []string{"tracked.txt", "link"} {
		if _, exists := entries[path]; !exists {
			t.Fatalf("snapshot omitted %s: %+v", path, first.Entries)
		}
	}
	if entries["link"].Kind != "symlink" ||
		entries["link"].SHA256 != digestBytes([]byte(outside)) ||
		entries["link"].SHA256 == digestBytes([]byte("outside-content-must-not-be-read\n")) {
		t.Fatalf("symlink was followed or not bound by target text: %+v", entries["link"])
	}

	mustWriteSoakCanaryTestFile(t, filepath.Join(root, "tracked.txt"), []byte("changed\n"))
	changed, err := BuildSoakCanaryRepositorySnapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	if changed.SnapshotDigest == first.SnapshotDigest {
		t.Fatal("worktree content change did not alter snapshot digest")
	}
}

func TestSoakCanaryActivationBindsSourceProvenanceAndRepositorySnapshot(t *testing.T) {
	fixture := validSoakCanaryFixture(t)
	validation := ValidateSoakCanaryActivation(fixture.request)
	if !validation.ActivationAllowed {
		t.Fatalf("valid activation failed: %+v", validation)
	}
	if fixture.request.Activation.SourceProvenanceDigest != fixture.request.SourceProvenance.ProvenanceDigest ||
		fixture.request.Activation.RepositorySnapshotDigest != fixture.request.RepositorySnapshot.SnapshotDigest {
		t.Fatalf("activation omitted provenance/snapshot binding: activation=%+v request=%+v",
			fixture.request.Activation, fixture.request)
	}
	fixture.request.RepositorySnapshot.Entries[0].SHA256 = "sha256:" + strings.Repeat("0", 64)
	signSoakCanaryRepositorySnapshot(&fixture.request.RepositorySnapshot)
	validation = ValidateSoakCanaryActivation(fixture.request)
	if !containsSoakConflict(validation.ConflictCodes, "repository_snapshot_digest_mismatch") {
		t.Fatalf("altered snapshot accepted: %+v", validation)
	}
}

func TestSoakCanaryBuildInfoProvenanceRequiresExactUnmodifiedRevision(t *testing.T) {
	settings := map[string]string{
		"vcs.revision": strings.Repeat("a", 40),
		"vcs.modified": "false",
	}
	provenance, err := soakCanarySourceProvenanceFromBuildSettings(settings)
	if err != nil {
		t.Fatal(err)
	}
	if provenance.Revision != settings["vcs.revision"] || provenance.Modified ||
		provenance.Provider != "go_build_info" || provenance.ProvenanceDigest == "" {
		t.Fatalf("build provenance is incomplete: %+v", provenance)
	}

	settings["vcs.modified"] = "true"
	if _, err := soakCanarySourceProvenanceFromBuildSettings(settings); err == nil ||
		!strings.Contains(err.Error(), "modified") {
		t.Fatalf("modified build provenance accepted: %v", err)
	}
	delete(settings, "vcs.revision")
	if _, err := soakCanarySourceProvenanceFromBuildSettings(settings); err == nil {
		t.Fatal("missing build revision accepted")
	}
}

func TestSoakCanaryOperationalBoundaryContainsNoGitSubprocess(t *testing.T) {
	for _, name := range []string{
		"cli_soak_canary.go",
		"soak_canary_git.go",
		"soak_canary_snapshot.go",
		"soak_canary_executor.go",
	} {
		body, err := os.ReadFile(filepath.Join(name))
		if err != nil {
			t.Fatal(err)
		}
		source := string(body)
		if strings.Contains(source, `"os/exec"`) && name != "soak_canary_executor.go" {
			t.Fatalf("%s imports os/exec", name)
		}
		if strings.Contains(strings.ToLower(source), `"git"`) ||
			strings.Contains(source, "exec.Command(") {
			t.Fatalf("%s contains a forbidden external verifier subprocess", name)
		}
	}
}

func TestSoakCanaryRestartBlocksNonDesignatedStartFailure(t *testing.T) {
	fixture := validSoakCanaryFixture(t)
	clock := &soakCanaryFakeClock{now: time.Date(2026, 7, 29, 21, 0, 0, 0, time.UTC)}
	fixture.request.Clock = clock
	fixture.request.Executor = &soakCanaryFakeExecutor{clock: clock, failAtStart: 3}

	first, err := RunSoakCanary(context.Background(), fixture.request)
	if err == nil || !containsSoakConflict(first.ConflictCodes, "child_process_start_failed") {
		t.Fatalf("start failure error=%v summary=%+v", err, first)
	}

	restartExecutor := &soakCanaryFakeExecutor{clock: clock}
	fixture.request.Executor = restartExecutor
	second, err := RunSoakCanary(context.Background(), fixture.request)
	if err == nil || !containsSoakConflict(second.ConflictCodes, "failed_attempt_terminal") {
		t.Fatalf("restart error=%v summary=%+v", err, second)
	}
	if restartExecutor.starts != 0 {
		t.Fatalf("restart relaunched a non-designated start failure: %d", restartExecutor.starts)
	}
}

func TestSoakCanaryRestartBlocksNonDesignatedPrelaunchFailure(t *testing.T) {
	fixture := validSoakCanaryFixture(t)
	clock := &soakCanaryFakeClock{now: time.Date(2026, 7, 29, 21, 0, 0, 0, time.UTC)}
	fixture.request.Clock = clock
	fixture.request.Executor = &soakCanaryFakeExecutor{clock: clock}
	fixture.request.Snapshotter = &soakCanaryRecordingRepositorySnapshotter{failAtCall: 6}

	first, err := RunSoakCanary(context.Background(), fixture.request)
	if err == nil || !containsSoakConflict(first.ConflictCodes, "repository_snapshot_mismatch") {
		t.Fatalf("prelaunch failure error=%v summary=%+v", err, first)
	}

	restartExecutor := &soakCanaryFakeExecutor{clock: clock}
	fixture.request.Executor = restartExecutor
	fixture.request.Snapshotter = PureGoSoakCanaryRepositorySnapshotter{}
	second, err := RunSoakCanary(context.Background(), fixture.request)
	if err == nil || !containsSoakConflict(second.ConflictCodes, "failed_attempt_terminal") {
		t.Fatalf("restart error=%v summary=%+v", err, second)
	}
	if restartExecutor.starts != 0 {
		t.Fatalf("restart relaunched a non-designated prelaunch failure: %d", restartExecutor.starts)
	}
}

func TestSoakCanaryTotalAttemptElapsedIncludesPostRunVerification(t *testing.T) {
	fixture := validSoakCanaryFixture(t)
	clock := &soakCanaryFakeClock{now: time.Date(2026, 7, 29, 21, 0, 0, 0, time.UTC)}
	fixture.request.Clock = clock
	fixture.request.Executor = &soakCanaryFakeExecutor{clock: clock}
	fixture.request.Snapshotter = &soakCanaryTimedRepositorySnapshotter{
		clock: clock, advanceAtCall: 3, advanceBy: 20 * time.Millisecond,
	}

	summary, err := RunSoakCanary(context.Background(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	first := summary.Attempts[0]
	if first.ChildElapsedMS != 5 || first.TotalAttemptElapsedMS < 25 {
		t.Fatalf("post-run work was omitted from total duration: %+v", first)
	}
}

func TestSoakCanaryPostChildEvidenceFailurePersistsCompletedAttempt(t *testing.T) {
	requireTestSymlinkCapability(t)
	fixture := validSoakCanaryFixture(t)
	clock := &soakCanaryFakeClock{now: time.Date(2026, 7, 29, 21, 0, 0, 0, time.UTC)}
	symlinkTarget := t.TempDir()
	fixture.request.Clock = clock
	fixture.request.Executor = &soakCanaryCallbackExecutor{
		clock: clock,
		beforeReturn: func() {
			if err := os.Symlink(symlinkTarget, filepath.Join(fixture.request.EvidenceRoot, "nodes")); err != nil {
				t.Errorf("create evidence symlink: %v", err)
			}
		},
	}

	summary, err := RunSoakCanary(context.Background(), fixture.request)
	if err == nil || !containsSoakConflict(summary.ConflictCodes, "stdout_evidence_write_failed") {
		t.Fatalf("output failure error=%v summary=%+v", err, summary)
	}
	if len(summary.Attempts) != 1 ||
		summary.Attempts[0].ExecutionState != "completed" ||
		summary.Attempts[0].OutcomeClass != "stdout_evidence_write_failed" ||
		!summary.Attempts[0].ChildProcessLaunched {
		t.Fatalf("post-child failure was not durably completed: %+v", summary)
	}
	checkpoint, loadErr := LoadSoakCanaryCheckpoint(fixture.request.CheckpointPath)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if checkpoint.Attempts[0].ExecutionState != "completed" {
		t.Fatalf("checkpoint retained a running child: %+v", checkpoint.Attempts[0])
	}
}

func TestSoakCanaryWaitPersistsHeartbeatWithoutResettingPhase(t *testing.T) {
	fixture := validSoakCanaryFixture(t)
	clock := &soakCanaryFakeClock{now: time.Date(2026, 7, 29, 21, 0, 0, 0, time.UTC)}
	fixture.request.Clock = clock
	fixture.request.HeartbeatInterval = 2 * time.Millisecond
	fixture.request.Executor = &soakCanaryBlockingExecutor{
		wait: 15 * time.Millisecond,
		result: SoakCanaryProcessResult{
			ExitCode:  1,
			ElapsedMS: 15,
			Stdout:    soakCanaryPassOutput(fixture.request.Catalog.Commands[0].TestName, 1),
		},
	}

	if _, err := RunSoakCanary(context.Background(), fixture.request); err == nil {
		t.Fatal("failing heartbeat child unexpectedly passed")
	}
	checkpoint, loadErr := LoadSoakCanaryCheckpoint(fixture.request.CheckpointPath)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	heartbeats := 0
	for _, event := range checkpoint.Events {
		if event.Event == "heartbeat" {
			heartbeats++
		}
	}
	if heartbeats == 0 || checkpoint.PhaseStartUTC != fixture.request.Activation.PhaseStartUTC ||
		checkpoint.Attempts[0].ExecutionState != "completed" {
		t.Fatalf("heartbeat checkpoint truth is incomplete: heartbeats=%d checkpoint=%+v", heartbeats, checkpoint)
	}
}

func TestSoakCanaryRetryAllowanceUsesFinalTotalAttemptElapsed(t *testing.T) {
	fixture := validSoakCanaryFixture(t)
	clock := &soakCanaryFakeClock{now: time.Date(2026, 7, 29, 21, 0, 0, 0, time.UTC)}
	fixture.request.Clock = clock
	fixture.request.Executor = &soakCanaryFakeExecutor{clock: clock}
	retryPartition := fixture.request.Activation.Partitions[1]
	fixture.request.Snapshotter = &soakCanaryTimedRepositorySnapshotter{
		clock: clock, advanceAtCall: 5,
		advanceBy: time.Duration(retryPartition.RetryAllowanceMS+1) * time.Millisecond,
	}

	summary, err := RunSoakCanary(context.Background(), fixture.request)
	if err == nil || !containsSoakConflict(summary.ConflictCodes, "retry_allowance_exceeded") {
		t.Fatalf("retry post-work overrun error=%v summary=%+v", err, summary)
	}
	if summary.ChildProcessLaunches != 2 {
		t.Fatalf("retry overrun launch truth=%d", summary.ChildProcessLaunches)
	}
}

func TestSoakCanaryPublicJSONPrintsCompletedOnlyAfterTerminalPersistence(t *testing.T) {
	fixture := validSoakCanaryFixture(t)
	args := soakCanaryCLIArgs(t, &fixture)
	clock := &soakCanaryFakeClock{now: time.Date(2026, 7, 29, 21, 0, 0, 0, time.UTC)}
	dependencies := soakCanaryCLIDependencies{
		provenanceProvider: soakCanaryStaticProvenanceProvider{fixture.request.SourceProvenance},
		snapshotter:        PureGoSoakCanaryRepositorySnapshotter{},
		gitVerifier:        soakCanaryCleanGitVerifier{},
		executor:           &soakCanaryFakeExecutor{clock: clock},
		clock:              clock,
		persistCompletion:  PersistSoakCanaryCompletion,
	}
	var stdout strings.Builder
	if err := runSoakCanaryCLIWithDependencies(args, &stdout, dependencies); err != nil {
		t.Fatal(err)
	}
	var summary SoakCanarySummary
	if err := json.Unmarshal([]byte(stdout.String()), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Status != "completed" {
		t.Fatalf("public JSON status=%q", summary.Status)
	}
	if _, err := os.Stat(filepath.Join(fixture.request.EvidenceRoot, "run-summary.json")); err != nil {
		t.Fatalf("completed JSON preceded final persistence: %v", err)
	}
	if _, err := os.Stat(filepath.Join(
		fixture.request.EvidenceRoot,
		"terminal-surface-ledger.json",
	)); err != nil {
		t.Fatalf("completed JSON preceded terminal surface agreement: %v", err)
	}
}

func TestSoakCanaryPublicJSONPersistenceFailureIsNonterminal(t *testing.T) {
	fixture := validSoakCanaryFixture(t)
	args := soakCanaryCLIArgs(t, &fixture)
	clock := &soakCanaryFakeClock{now: time.Date(2026, 7, 29, 21, 0, 0, 0, time.UTC)}
	dependencies := soakCanaryCLIDependencies{
		provenanceProvider: soakCanaryStaticProvenanceProvider{fixture.request.SourceProvenance},
		snapshotter:        PureGoSoakCanaryRepositorySnapshotter{},
		gitVerifier:        soakCanaryCleanGitVerifier{},
		executor:           &soakCanaryFakeExecutor{clock: clock},
		clock:              clock,
		persistCompletion: func(string, SoakCanarySummary) error {
			return errors.New("injected terminal persistence failure")
		},
	}
	var stdout strings.Builder
	err := runSoakCanaryCLIWithDependencies(args, &stdout, dependencies)
	if err == nil || !strings.Contains(err.Error(), "terminal persistence failure") {
		t.Fatalf("persistence error=%v output=%s", err, stdout.String())
	}
	var summary SoakCanarySummary
	if decodeErr := json.Unmarshal([]byte(stdout.String()), &summary); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if summary.Status != "terminal_reconciliation_pending" ||
		summary.TerminalIndexDigest != "" {
		t.Fatalf("persistence failure emitted completed truth: %+v", summary)
	}
	if _, statErr := os.Stat(
		filepath.Join(fixture.request.EvidenceRoot, "run-summary.json"),
	); !os.IsNotExist(statErr) {
		t.Fatalf("final summary exists after persistence failure: %v", statErr)
	}
}

func TestSoakCanaryEvidenceRejectsResignedFalseGoEventClaims(t *testing.T) {
	fixture := validSoakCanaryFixture(t)
	clock := &soakCanaryFakeClock{now: time.Date(2026, 7, 29, 21, 0, 0, 0, time.UTC)}
	fixture.request.Clock = clock
	fixture.request.Executor = &soakCanaryFakeExecutor{clock: clock}
	if _, err := RunSoakCanary(context.Background(), fixture.request); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := LoadSoakCanaryCheckpoint(fixture.request.CheckpointPath)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint.Attempts[0].GoTestEvents.TotalEvents++
	signSoakCanaryAttempt(&checkpoint.Attempts[0])
	signSoakCanaryCheckpoint(&checkpoint)

	err = VerifySoakCanaryEvidence(fixture.request, checkpoint)
	if err == nil || !strings.Contains(err.Error(), "Go test event counts") {
		t.Fatalf("re-signed false event claims accepted: %v", err)
	}
}

func mustWriteSoakCanaryTestFile(t *testing.T, path string, body []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
}

type soakCanaryTimedRepositorySnapshotter struct {
	clock         *soakCanaryFakeClock
	calls         int
	advanceAtCall int
	advanceBy     time.Duration
}

func (snapshotter *soakCanaryTimedRepositorySnapshotter) Snapshot(
	root string,
) (SoakCanaryRepositorySnapshot, error) {
	snapshotter.calls++
	snapshot, err := BuildSoakCanaryRepositorySnapshot(root)
	if snapshotter.calls == snapshotter.advanceAtCall {
		snapshotter.clock.advance(snapshotter.advanceBy)
	}
	return snapshot, err
}

type soakCanaryCallbackExecutor struct {
	clock        *soakCanaryFakeClock
	beforeReturn func()
}

func (executor *soakCanaryCallbackExecutor) Start(
	_ context.Context,
	request SoakCanaryExecRequest,
) (SoakCanaryProcess, error) {
	return &soakCanaryCallbackProcess{
		clock: executor.clock, beforeReturn: executor.beforeReturn,
		result: SoakCanaryProcessResult{
			ExitCode: 0, ElapsedMS: 5,
			Stdout: soakCanaryPassOutput(request.ExpectedTestName, request.ExpectedPassCount),
		},
	}, nil
}

type soakCanaryCallbackProcess struct {
	clock        *soakCanaryFakeClock
	beforeReturn func()
	result       SoakCanaryProcessResult
}

func (*soakCanaryCallbackProcess) PID() int { return 41_001 }

func (process *soakCanaryCallbackProcess) Wait() SoakCanaryProcessResult {
	if process.clock != nil {
		process.clock.advance(time.Duration(process.result.ElapsedMS) * time.Millisecond)
	}
	if process.beforeReturn != nil {
		process.beforeReturn()
	}
	return process.result
}

type soakCanaryBlockingExecutor struct {
	wait   time.Duration
	result SoakCanaryProcessResult
}

func (executor *soakCanaryBlockingExecutor) Start(
	_ context.Context,
	_ SoakCanaryExecRequest,
) (SoakCanaryProcess, error) {
	return &soakCanaryBlockingProcess{wait: executor.wait, result: executor.result}, nil
}

type soakCanaryBlockingProcess struct {
	wait   time.Duration
	result SoakCanaryProcessResult
}

func (*soakCanaryBlockingProcess) PID() int { return 42_001 }

func (process *soakCanaryBlockingProcess) Wait() SoakCanaryProcessResult {
	time.Sleep(process.wait)
	return process.result
}

func soakCanaryPassOutput(testName string, repeats int) []byte {
	var lines []string
	for index := 0; index < repeats; index++ {
		body, _ := json.Marshal(map[string]any{
			"Action": "pass", "Package": "./internal/mission", "Test": testName,
		})
		lines = append(lines, string(body))
	}
	return []byte(strings.Join(lines, "\n"))
}

func soakCanaryCLIArgs(t *testing.T, fixture *soakCanaryTestFixture) []string {
	t.Helper()
	inputRoot := t.TempDir()
	planPath := writeSoakCanaryJSON(t, inputRoot, "plan.json", fixture.request.PlanInput)
	planBody, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatal(err)
	}
	fixture.request.PlanFixtureSHA256 = digestBytes(planBody)
	fixture.request.Activation.PlanFixtureSHA256 = fixture.request.PlanFixtureSHA256
	signSoakCanaryActivation(&fixture.request.Activation)
	return []string{
		"--plan", planPath,
		"--authority", writeSoakCanaryJSON(t, inputRoot, "authority.json", fixture.request.Authority),
		"--catalog", writeSoakCanaryJSON(t, inputRoot, "catalog.json", fixture.request.Catalog),
		"--activation", writeSoakCanaryJSON(t, inputRoot, "activation.json", fixture.request.Activation),
		"--checkpoint", fixture.request.CheckpointPath,
		"--evidence-root", fixture.request.EvidenceRoot,
		"--repository-root", fixture.request.RepositoryRoot,
		"--json",
	}
}
