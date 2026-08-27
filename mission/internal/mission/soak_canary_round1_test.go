package mission

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSoakCanaryCrashAfterScaleReservationBlocksRestart(t *testing.T) {
	fixture := validSoakCanaryFixture(t)
	clock := &soakCanaryFakeClock{now: time.Date(2026, 7, 29, 21, 0, 0, 0, time.UTC)}
	executor := &soakCanaryFakeExecutor{clock: clock}
	fixture.request.Clock = clock
	fixture.request.Executor = executor
	fixture.request.AfterLaunchReservation = func(attempt SoakCanaryAttempt) error {
		if attempt.Classification != "scale" || attempt.ExecutionState != "reserved" {
			t.Fatalf("unexpected reservation: %+v", attempt)
		}
		return errors.New("injected crash after reservation")
	}

	summary, err := RunSoakCanary(context.Background(), fixture.request)
	if err == nil || !strings.Contains(err.Error(), "injected crash after reservation") {
		t.Fatalf("crash error=%v summary=%+v", err, summary)
	}
	if executor.starts != 0 || summary.TotalAttempts != 1 ||
		summary.ChildProcessLaunches != 0 || summary.LocalTestExecutionPerformed {
		t.Fatalf("reservation crash truth is wrong: starts=%d summary=%+v", executor.starts, summary)
	}
	checkpoint, err := LoadSoakCanaryCheckpoint(fixture.request.CheckpointPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(checkpoint.Attempts) != 1 ||
		checkpoint.Attempts[0].ExecutionState != "reserved" ||
		checkpoint.Attempts[0].Classification != "scale" {
		t.Fatalf("scale reservation was not durable: %+v", checkpoint)
	}

	fixture.request.AfterLaunchReservation = nil
	restartExecutor := &soakCanaryFakeExecutor{clock: clock}
	fixture.request.Executor = restartExecutor
	summary, err = RunSoakCanary(context.Background(), fixture.request)
	if err == nil || !containsSoakConflict(summary.ConflictCodes, "indeterminate_scale_launch") {
		t.Fatalf("restart error=%v summary=%+v", err, summary)
	}
	if restartExecutor.starts != 0 {
		t.Fatalf("restart relaunched indeterminate scale process: %d", restartExecutor.starts)
	}
}

func TestSoakCanaryCompletedReplayUsesPersistedCompletionTimestamp(t *testing.T) {
	fixture := validSoakCanaryFixture(t)
	clock := &soakCanaryStepClock{now: time.Date(2026, 7, 29, 21, 0, 0, 0, time.UTC)}
	executor := &soakCanaryStaticExecutor{}
	fixture.request.Clock = clock
	fixture.request.Executor = executor

	first, err := RunSoakCanary(context.Background(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := LoadSoakCanaryCheckpoint(fixture.request.CheckpointPath)
	if err != nil {
		t.Fatal(err)
	}
	if first.CompletedAtUTC != checkpoint.CompletedAtUTC {
		t.Fatalf("summary completion=%s checkpoint=%s", first.CompletedAtUTC, checkpoint.CompletedAtUTC)
	}

	clock.now = clock.now.Add(10 * time.Minute)
	second, err := RunSoakCanary(context.Background(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	if first.SummaryDigest != second.SummaryDigest ||
		!reflect.DeepEqual(first, second) {
		t.Fatalf("completed replay drifted:\nfirst=%+v\nsecond=%+v", first, second)
	}
}

func TestSoakCanaryResignedSemanticAttemptTamperingFailsClosed(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*SoakCanaryAttempt)
	}{
		{name: "source head", mutate: func(attempt *SoakCanaryAttempt) {
			attempt.SourceHead = strings.Repeat("b", 40)
		}},
		{name: "source provenance", mutate: func(attempt *SoakCanaryAttempt) {
			attempt.SourceProvenanceDigest = "sha256:" + strings.Repeat("0", 64)
		}},
		{name: "repository snapshot before", mutate: func(attempt *SoakCanaryAttempt) {
			attempt.RepositorySnapshotBeforeDigest = "sha256:" + strings.Repeat("0", 64)
		}},
		{name: "repository snapshot after", mutate: func(attempt *SoakCanaryAttempt) {
			attempt.RepositorySnapshotAfterDigest = "sha256:" + strings.Repeat("0", 64)
		}},
		{name: "argv digest", mutate: func(attempt *SoakCanaryAttempt) {
			attempt.CommandArgvDigest = "sha256:" + strings.Repeat("0", 64)
		}},
		{name: "repeat count", mutate: func(attempt *SoakCanaryAttempt) {
			attempt.EffectiveRepeatCount++
		}},
		{name: "classification", mutate: func(attempt *SoakCanaryAttempt) {
			attempt.Classification = "regular"
		}},
		{name: "scale dimension", mutate: func(attempt *SoakCanaryAttempt) {
			attempt.ScaleDimension = &SoakScaleDimension{Unit: "records", Value: 9_999}
		}},
		{name: "attempt number", mutate: func(attempt *SoakCanaryAttempt) {
			attempt.AttemptNumber = 2
		}},
		{name: "launch truth", mutate: func(attempt *SoakCanaryAttempt) {
			attempt.ChildProcessLaunched = false
			attempt.ChildPID = 0
		}},
		{name: "pass events", mutate: func(attempt *SoakCanaryAttempt) {
			attempt.GoTestEvents.MatchingPasses++
		}},
		{name: "checkpoint sequence", mutate: func(attempt *SoakCanaryAttempt) {
			attempt.CheckpointAfterSequence++
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
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
			test.mutate(&checkpoint.Attempts[0])
			resignSoakCanaryCheckpointEvents(&checkpoint)
			if err := writeSoakCanaryCheckpoint(fixture.request.CheckpointPath, checkpoint); err != nil {
				t.Fatal(err)
			}

			replayExecutor := &soakCanaryFakeExecutor{clock: clock}
			fixture.request.Executor = replayExecutor
			summary, err := RunSoakCanary(context.Background(), fixture.request)
			if err == nil || !containsSoakConflict(summary.ConflictCodes, "checkpoint_semantic_mismatch") {
				t.Fatalf("tamper accepted: error=%v summary=%+v", err, summary)
			}
			if replayExecutor.starts != 0 {
				t.Fatalf("tampered checkpoint reached executor: %d", replayExecutor.starts)
			}
		})
	}
}

func resignSoakCanaryCheckpointEvents(checkpoint *SoakCanaryCheckpoint) {
	for index := range checkpoint.Attempts {
		signSoakCanaryAttempt(&checkpoint.Attempts[index])
	}
	attempts := map[string]SoakCanaryAttempt{}
	for _, attempt := range checkpoint.Attempts {
		attempts[attempt.NodeID+"#"+strconv.Itoa(attempt.AttemptNumber)] = attempt
	}
	prior := ""
	for index := range checkpoint.Events {
		event := &checkpoint.Events[index]
		attempt := attempts[event.NodeID+"#"+strconv.Itoa(event.AttemptNumber)]
		event.AttemptSnapshotDigest = soakCanaryAttemptSnapshotDigest(
			attempt,
			event.Event,
			event.Sequence,
		)
		event.PriorEventDigest = prior
		signSoakCanaryCheckpointEvent(event)
		prior = event.EventDigest
	}
	signSoakCanaryCheckpoint(checkpoint)
}

func TestSoakCanaryResignedCheckpointLinkTamperingFailsClosed(t *testing.T) {
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
	checkpoint.PriorCheckpointDigest = "sha256:" + strings.Repeat("0", 64)
	signSoakCanaryCheckpoint(&checkpoint)
	if err := writeSoakCanaryCheckpoint(fixture.request.CheckpointPath, checkpoint); err != nil {
		t.Fatal(err)
	}
	executor := &soakCanaryFakeExecutor{}
	fixture.request.Executor = executor
	summary, err := RunSoakCanary(context.Background(), fixture.request)
	if err == nil || !containsSoakConflict(summary.ConflictCodes, "checkpoint_semantic_mismatch") {
		t.Fatalf("checkpoint-link tamper accepted: error=%v summary=%+v", err, summary)
	}
	if executor.starts != 0 {
		t.Fatalf("checkpoint-link tamper reached executor: %d", executor.starts)
	}
}

func TestSoakCanaryPlannerUnplannedTestFailsBeforeLaunch(t *testing.T) {
	fixture := validSoakCanaryFixture(t)
	const oldID = "regular-objective-pending-blueprint"
	const newID = "planner-unplanned-test"
	for index := range fixture.request.PlanInput.TestCatalog {
		if fixture.request.PlanInput.TestCatalog[index].ID == oldID {
			fixture.request.PlanInput.TestCatalog[index].ID = newID
		}
	}
	for index := range fixture.request.PlanInput.DurationHistory {
		if fixture.request.PlanInput.DurationHistory[index].TestID == oldID {
			fixture.request.PlanInput.DurationHistory[index].TestID = newID
		}
	}
	for index := range fixture.request.PlanInput.Partitions {
		for testIndex := range fixture.request.PlanInput.Partitions[index].TestIDs {
			if fixture.request.PlanInput.Partitions[index].TestIDs[testIndex] == oldID {
				fixture.request.PlanInput.Partitions[index].TestIDs[testIndex] = newID
			}
		}
	}
	rebuildSoakCanaryPlanBinding(t, &fixture)
	executor := &soakCanaryFakeExecutor{}
	fixture.request.Executor = executor
	summary, err := RunSoakCanary(context.Background(), fixture.request)
	if err == nil || !containsSoakConflict(summary.ConflictCodes, "activation_catalog_bijection_mismatch") {
		t.Fatalf("unplanned planner test accepted: error=%v summary=%+v", err, summary)
	}
	if executor.starts != 0 {
		t.Fatalf("unplanned planner test reached executor: %d", executor.starts)
	}
}

func TestSoakCanaryRegularBeforeScaleFailsBeforeLaunch(t *testing.T) {
	fixture := validSoakCanaryFixture(t)
	partitions := fixture.request.PlanInput.Partitions
	budgets := fixture.request.PlanInput.PartitionBudgets
	fixture.request.PlanInput.Partitions = append(
		[]SoakPartitionRequest{partitions[1]},
		append([]SoakPartitionRequest{partitions[0]}, partitions[2:]...)...,
	)
	fixture.request.PlanInput.PartitionBudgets = append(
		[]SoakPartitionBudget{budgets[1]},
		append([]SoakPartitionBudget{budgets[0]}, budgets[2:]...)...,
	)
	rebuildSoakCanaryPlanBinding(t, &fixture)
	executor := &soakCanaryFakeExecutor{}
	fixture.request.Executor = executor
	summary, err := RunSoakCanary(context.Background(), fixture.request)
	if err == nil || !containsSoakConflict(summary.ConflictCodes, "scale_partition_not_first") {
		t.Fatalf("regular-first plan accepted: error=%v summary=%+v", err, summary)
	}
	if executor.starts != 0 {
		t.Fatalf("regular-first plan reached executor: %d", executor.starts)
	}
}

func TestSoakCanaryLongChildCannotPassHardWall(t *testing.T) {
	fixture := validSoakCanaryFixture(t)
	clock := &soakCanaryFakeClock{now: time.Date(2026, 7, 29, 21, 0, 0, 0, time.UTC)}
	executor := &soakCanaryResultExecutor{
		clock: clock,
		result: SoakCanaryProcessResult{
			ExitCode: 0, ElapsedMS: fixture.request.Authority.HardWallMS + 1,
		},
	}
	fixture.request.Clock = clock
	fixture.request.Executor = executor

	summary, err := RunSoakCanary(context.Background(), fixture.request)
	if err == nil || !containsSoakConflict(summary.ConflictCodes, "hard_wall_reached") {
		t.Fatalf("long child passed hard wall: error=%v summary=%+v", err, summary)
	}
	if executor.starts != 1 || summary.ChildProcessLaunches != 1 ||
		!summary.LocalTestExecutionPerformed ||
		summary.Attempts[0].ChildElapsedMS != fixture.request.Authority.HardWallMS+1 {
		t.Fatalf("hard-wall accounting is false: starts=%d summary=%+v", executor.starts, summary)
	}
}

func TestSoakCanaryExecutionLimitsFailClosed(t *testing.T) {
	base := SoakCanaryLimitInput{
		ChildElapsedMS: 5, TotalAttemptElapsedMS: 5,
		NodeElapsedMS: 5, AggregateElapsedMS: 5, PhaseElapsedMS: 5,
		PerAttemptTimeoutMS: 10, TotalNodeTimeoutMS: 20,
		NodeBudgetMS: 30, RetryAllowanceMS: 10,
		LeaseMaximumMS: 40, HardWallMS: 50,
	}
	tests := []struct {
		name string
		code string
		edit func(*SoakCanaryLimitInput)
	}{
		{"per attempt", "per_attempt_timeout_exceeded", func(v *SoakCanaryLimitInput) { v.ChildElapsedMS = 11 }},
		{"total node", "total_node_timeout_exceeded", func(v *SoakCanaryLimitInput) { v.NodeElapsedMS = 21 }},
		{"node budget", "node_budget_exceeded", func(v *SoakCanaryLimitInput) { v.NodeElapsedMS = 31 }},
		{"retry allowance", "retry_allowance_exceeded", func(v *SoakCanaryLimitInput) {
			v.IsRetry = true
			v.TotalAttemptElapsedMS = 11
		}},
		{"lease", "lease_maximum_exceeded", func(v *SoakCanaryLimitInput) { v.PhaseElapsedMS = 41 }},
		{"hard wall", "hard_wall_reached", func(v *SoakCanaryLimitInput) { v.PhaseElapsedMS = 51 }},
		{"aggregate", "aggregate_duration_exceeded", func(v *SoakCanaryLimitInput) {
			v.AggregateElapsedMS = 41
			v.AggregateLimitMS = 40
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := base
			test.edit(&value)
			conflicts := evaluateSoakCanaryLimits(value)
			if !soakCanaryStringPresent(conflicts, test.code) {
				t.Fatalf("conflicts=%v want=%s", conflicts, test.code)
			}
		})
	}
}

func TestSoakCanaryRuntimeEnvironmentIsCampaignOwnedAndHostIndependent(t *testing.T) {
	fixture := validSoakCanaryFixture(t)
	t.Setenv("HOME", "/hostile/home")
	t.Setenv("TMPDIR", "/hostile/tmp")
	t.Setenv("GOCACHE", "/hostile/gocache")
	t.Setenv("GOMODCACHE", "/hostile/gomodcache")
	t.Setenv("GOENV", "/hostile/goenv")
	t.Setenv("CGO_ENABLED", "0")
	t.Setenv("PATH", "/hostile/path")
	clock := &soakCanaryFakeClock{now: time.Date(2026, 7, 29, 21, 0, 0, 0, time.UTC)}
	executor := &soakCanaryFakeExecutor{clock: clock}
	snapshotter := &soakCanaryRecordingRepositorySnapshotter{}
	fixture.request.Clock = clock
	fixture.request.Executor = executor
	fixture.request.Snapshotter = snapshotter

	if _, err := RunSoakCanary(context.Background(), fixture.request); err != nil {
		t.Fatal(err)
	}
	if snapshotter.calls != 21 {
		t.Fatalf("repository snapshot calls=%d want=21", snapshotter.calls)
	}
	for _, request := range executor.requests {
		values := map[string]string{}
		for _, pair := range request.Environment {
			values[pair.Name] = pair.Value
		}
		for _, name := range []string{"HOME", "TMPDIR", "GOCACHE", "GOTMPDIR"} {
			if !pathWithin(fixture.request.EvidenceRoot, values[name]) {
				t.Fatalf("%s escaped evidence root: %q", name, values[name])
			}
		}
		for _, name := range []string{"GOMODCACHE", "GOENV", "CGO_ENABLED"} {
			if _, exists := values[name]; exists {
				t.Fatalf("host-controlled %s was inherited: %+v", name, request.Environment)
			}
		}
		if strings.Contains(values["PATH"], "/hostile") {
			t.Fatalf("host PATH was inherited: %q", values["PATH"])
		}
	}
}

func TestSoakCanaryRepositoryVerificationFailurePreservesExecutionTruth(t *testing.T) {
	tests := []struct {
		name       string
		failAtCall int
		wantStarts int
	}{
		{name: "before first spawn", failAtCall: 2, wantStarts: 0},
		{name: "after first execution", failAtCall: 3, wantStarts: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := validSoakCanaryFixture(t)
			clock := &soakCanaryFakeClock{now: time.Date(2026, 7, 29, 21, 0, 0, 0, time.UTC)}
			executor := &soakCanaryFakeExecutor{clock: clock}
			fixture.request.Clock = clock
			fixture.request.Executor = executor
			fixture.request.Snapshotter = &soakCanaryRecordingRepositorySnapshotter{
				failAtCall: test.failAtCall,
			}
			summary, err := RunSoakCanary(context.Background(), fixture.request)
			if err == nil || !containsSoakConflict(summary.ConflictCodes, "repository_snapshot_mismatch") {
				t.Fatalf("verification failure accepted: error=%v summary=%+v", err, summary)
			}
			if executor.starts != test.wantStarts ||
				summary.ChildProcessLaunches != test.wantStarts ||
				summary.LocalTestExecutionPerformed != (test.wantStarts > 0) {
				t.Fatalf("execution truth drifted: starts=%d summary=%+v", executor.starts, summary)
			}
		})
	}
}

func TestSoakCanaryExecutableProvenanceChangeFailsBeforeLaunch(t *testing.T) {
	fixture := validSoakCanaryFixture(t)
	clock := &soakCanaryFakeClock{now: time.Date(2026, 7, 29, 21, 0, 0, 0, time.UTC)}
	executor := &soakCanaryFakeExecutor{}
	fixture.request.Clock = clock
	fixture.request.Executor = executor
	fixture.request.AfterLaunchReservation = func(SoakCanaryAttempt) error {
		return os.WriteFile(fixture.goPath, []byte("altered executable"), 0o700)
	}
	summary, err := RunSoakCanary(context.Background(), fixture.request)
	if err == nil || !containsSoakConflict(summary.ConflictCodes, "executable_provenance_mismatch") {
		t.Fatalf("changed executable accepted: error=%v summary=%+v", err, summary)
	}
	if executor.starts != 0 || summary.ChildProcessLaunches != 0 {
		t.Fatalf("changed executable reached executor: starts=%d summary=%+v", executor.starts, summary)
	}
}

func TestSoakCanaryUnsafeRootsFailBeforeLaunch(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *soakCanaryTestFixture)
	}{
		{name: "evidence under repository", mutate: func(t *testing.T, f *soakCanaryTestFixture) {
			f.request.EvidenceRoot = filepath.Join(f.request.RepositoryRoot, "evidence")
			f.request.Authority.EvidenceRoot = f.request.EvidenceRoot
			f.request.CheckpointPath = filepath.Join(f.request.EvidenceRoot, "checkpoint.json")
			signSoakCanaryAuthority(&f.request.Authority)
			f.request.Activation.AuthorityRecordDigest = f.request.Authority.AuthorityRecordDigest
			signSoakCanaryActivation(&f.request.Activation)
		}},
		{name: "symlink repository", mutate: func(t *testing.T, f *soakCanaryTestFixture) {
			link := filepath.Join(t.TempDir(), "repository-link")
			createTestSymlink(t, f.request.RepositoryRoot, link)
			f.request.RepositoryRoot = link
		}},
		{name: "symlink evidence", mutate: func(t *testing.T, f *soakCanaryTestFixture) {
			link := filepath.Join(t.TempDir(), "evidence-link")
			createTestSymlink(t, f.request.EvidenceRoot, link)
			f.request.EvidenceRoot = link
			f.request.Authority.EvidenceRoot = link
			f.request.CheckpointPath = filepath.Join(link, "checkpoint.json")
			signSoakCanaryAuthority(&f.request.Authority)
			f.request.Activation.AuthorityRecordDigest = f.request.Authority.AuthorityRecordDigest
			signSoakCanaryActivation(&f.request.Activation)
		}},
		{name: "symlink checkpoint component", mutate: func(t *testing.T, f *soakCanaryTestFixture) {
			target := t.TempDir()
			link := filepath.Join(f.request.EvidenceRoot, "checkpoints")
			createTestSymlink(t, target, link)
			f.request.CheckpointPath = filepath.Join(link, "checkpoint.json")
		}},
		{name: "symlink output component", mutate: func(t *testing.T, f *soakCanaryTestFixture) {
			createTestSymlink(t, t.TempDir(), filepath.Join(f.request.EvidenceRoot, "nodes"))
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := validSoakCanaryFixture(t)
			test.mutate(t, &fixture)
			executor := &soakCanaryFakeExecutor{}
			fixture.request.Executor = executor
			summary, err := RunSoakCanary(context.Background(), fixture.request)
			if err == nil || !containsSoakConflict(summary.ConflictCodes, "unsafe_runtime_path") {
				t.Fatalf("unsafe root accepted: error=%v summary=%+v", err, summary)
			}
			if executor.starts != 0 {
				t.Fatalf("unsafe root reached executor: %d", executor.starts)
			}
		})
	}
}

func TestSoakCanaryTerminalBindsOperationalTruthAndExactClosure(t *testing.T) {
	fixture := validSoakCanaryFixture(t)
	clock := &soakCanaryFakeClock{now: time.Date(2026, 7, 29, 21, 0, 0, 0, time.UTC)}
	fixture.request.Clock = clock
	fixture.request.Executor = &soakCanaryFakeExecutor{clock: clock}
	summary, err := RunSoakCanary(context.Background(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := LoadSoakCanaryCheckpoint(fixture.request.CheckpointPath)
	if err != nil {
		t.Fatal(err)
	}
	if summary.CheckpointDigest != checkpoint.CheckpointDigest ||
		summary.LeaseMinimumMS != fixture.request.PlanInput.Lease.MinimumMS ||
		summary.LeaseTargetMS != fixture.request.PlanInput.Lease.TargetMS ||
		summary.LeaseMaximumMS != fixture.request.PlanInput.Lease.MaximumMS ||
		summary.PassedAttemptCount != 10 || summary.TotalChildElapsedMS <= 0 ||
		summary.TotalAttemptElapsedMS < summary.TotalChildElapsedMS {
		t.Fatalf("summary omitted terminal truth: %+v", summary)
	}
	index, artifacts, err := buildSoakCanaryTerminalIndex(summary)
	if err != nil {
		t.Fatal(err)
	}
	if index.ExactNextAction != "none" ||
		!terminalNoAction(index.ExactNextAction) {
		t.Fatalf("terminal next action=%q", index.ExactNextAction)
	}
	for _, path := range []string{"root.json", "terminal.json"} {
		var artifact soakCanaryProgressArtifact
		if err := json.Unmarshal(artifacts[path], &artifact); err != nil {
			t.Fatal(err)
		}
		truth := artifact.OperationalTruth
		if truth.AuthorityRecordDigest != summary.AuthorityRecordDigest ||
			truth.ActivationManifestDigest != summary.ActivationManifestDigest ||
			truth.CommandCatalogDigest != summary.CommandCatalogDigest ||
			truth.SourceProvenanceDigest != summary.SourceProvenanceDigest ||
			truth.RepositorySnapshotDigest != summary.RepositorySnapshotDigest ||
			truth.CheckpointDigest != summary.CheckpointDigest ||
			truth.TotalAttempts != 11 || truth.ChildProcessLaunches != 10 ||
			truth.ScaleLaunches != 1 || truth.ControlledRetryCount != 1 ||
			truth.PassedAttemptCount != 10 || !truth.LocalTestExecutionPerformed {
			t.Fatalf("%s omitted operational truth: %+v", path, artifact)
		}
	}
	readbacks, err := BuildSoakCanaryTerminalReadbacks(summary)
	if err != nil {
		t.Fatal(err)
	}
	for _, readback := range readbacks {
		if readback.ExactNextAction != "none" {
			t.Fatalf("surface=%s next=%q", readback.Surface, readback.ExactNextAction)
		}
	}
}

func TestSoakCanaryCompletionRemainsProvisionalWhenTerminalPathIsUnsafe(t *testing.T) {
	fixture := validSoakCanaryFixture(t)
	clock := &soakCanaryFakeClock{now: time.Date(2026, 7, 29, 21, 0, 0, 0, time.UTC)}
	fixture.request.Clock = clock
	fixture.request.Executor = &soakCanaryFakeExecutor{clock: clock}
	summary, err := RunSoakCanary(context.Background(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	createTestSymlink(t, t.TempDir(), filepath.Join(fixture.request.EvidenceRoot, "terminal"))
	if err := PersistSoakCanaryCompletion(fixture.request.EvidenceRoot, summary); err == nil {
		t.Fatal("unsafe terminal path accepted")
	}
	if _, err := os.Stat(filepath.Join(fixture.request.EvidenceRoot, "run-summary.json")); !os.IsNotExist(err) {
		t.Fatalf("final summary existed before terminal agreement: %v", err)
	}
	provisional, err := loadSoakCanaryJSON[SoakCanarySummary](
		filepath.Join(fixture.request.EvidenceRoot, "run-summary.provisional.json"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if provisional.Status != "terminal_reconciliation_pending" {
		t.Fatalf("provisional status=%q", provisional.Status)
	}
}

func rebuildSoakCanaryPlanBinding(t *testing.T, fixture *soakCanaryTestFixture) {
	t.Helper()
	fixture.request.PlanInput.PolicyDigest = soakPolicyDigest(fixture.request.PlanInput)
	fixture.request.PlanInput.Activation.BoundPolicyDigest = fixture.request.PlanInput.PolicyDigest
	fixture.request.PlanReadback = buildValidSoakPlan(t, fixture.request.PlanInput)
	activation, err := BuildSoakCanaryActivation(
		fixture.request.PlanInput,
		fixture.request.PlanReadback,
		fixture.request.PlanFixtureSHA256,
		fixture.request.Authority,
		fixture.request.Catalog,
		fixture.request.SourceProvenance,
		fixture.request.RepositorySnapshot,
		fixture.request.Activation.PhaseStartUTC,
		fixture.request.Activation.ControlledRetryNodeID,
	)
	if err != nil {
		t.Fatal(err)
	}
	fixture.request.Activation = activation
}

type soakCanaryStepClock struct {
	now time.Time
}

func (clock *soakCanaryStepClock) Now() time.Time {
	current := clock.now
	clock.now = clock.now.Add(time.Millisecond)
	return current
}

type soakCanaryStaticExecutor struct {
	starts int
}

type soakCanaryResultExecutor struct {
	clock  *soakCanaryFakeClock
	result SoakCanaryProcessResult
	starts int
}

func (executor *soakCanaryResultExecutor) Start(_ context.Context, _ SoakCanaryExecRequest) (SoakCanaryProcess, error) {
	executor.starts++
	result := executor.result
	if len(result.Stdout) == 0 {
		body, _ := json.Marshal(map[string]any{
			"Action": "pass", "Package": "./internal/mission",
			"Test": "TestMissionEventIndexScaleMetricsExposeReadAndEventCounts/10000",
		})
		result.Stdout = body
	}
	return &soakCanaryFakeProcess{
		pid: 30_000 + executor.starts, clock: executor.clock, result: result,
	}, nil
}

type soakCanaryRecordingRepositorySnapshotter struct {
	calls      int
	failAtCall int
}

func (snapshotter *soakCanaryRecordingRepositorySnapshotter) Snapshot(
	root string,
) (SoakCanaryRepositorySnapshot, error) {
	snapshotter.calls++
	if snapshotter.failAtCall > 0 && snapshotter.calls == snapshotter.failAtCall {
		return SoakCanaryRepositorySnapshot{}, errors.New("injected repository snapshot failure")
	}
	return BuildSoakCanaryRepositorySnapshot(root)
}

func (executor *soakCanaryStaticExecutor) Start(_ context.Context, request SoakCanaryExecRequest) (SoakCanaryProcess, error) {
	executor.starts++
	var lines []string
	for index := 0; index < request.ExpectedPassCount; index++ {
		body, _ := json.Marshal(map[string]any{
			"Action": "pass", "Package": "./internal/mission", "Test": request.ExpectedTestName,
		})
		lines = append(lines, string(body))
	}
	return &soakCanaryFakeProcess{
		pid: 20_000 + executor.starts,
		result: SoakCanaryProcessResult{
			ExitCode: 0, ElapsedMS: 1, Stdout: []byte(strings.Join(lines, "\n")),
		},
	}, nil
}
