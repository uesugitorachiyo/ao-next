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

func TestSoakCanaryValidActivationBuildsShellFreeCommands(t *testing.T) {
	fixture := validSoakCanaryFixture(t)
	readback := ValidateSoakCanaryActivation(fixture.request)

	if !readback.ActivationAllowed || len(readback.ConflictCodes) != 0 {
		t.Fatalf("valid activation failed closed: %+v", readback)
	}
	if !readback.PlannerActivationEligible || !readback.CanaryExecutionAuthorized ||
		readback.ChildProcessLaunches != 0 {
		t.Fatalf("activation authority is contradictory: %+v", readback)
	}
	if len(fixture.request.Activation.Partitions) != 10 {
		t.Fatalf("partitions=%d want=10", len(fixture.request.Activation.Partitions))
	}
	for _, command := range fixture.request.Catalog.Commands {
		if command.ExecutablePath != fixture.goPath {
			t.Fatalf("executable=%q want=%q", command.ExecutablePath, fixture.goPath)
		}
		if len(command.Argv) != 9 ||
			!reflect.DeepEqual(command.Argv[:3], []string{"test", "./internal/mission", "-race"}) ||
			command.Argv[3] != "-run" || command.Argv[4] != command.TestRegex ||
			command.Argv[5] != "-count="+strconv.Itoa(command.EffectiveRepeatCount) ||
			command.Argv[6] != "-json" || command.Argv[7] != "-timeout" ||
			command.Argv[8] != strconv.FormatInt(command.TimeoutMS, 10)+"ms" {
			t.Fatalf("command argv is not the fixed exec-style form: %+v", command)
		}
		for _, arg := range command.Argv {
			if strings.ContainsAny(arg, ";&|`") {
				t.Fatalf("shell metacharacter reached argv: %q", arg)
			}
		}
	}
}

func TestSoakCanaryActivationMutationsFailBeforeLaunch(t *testing.T) {
	tests := []struct {
		name   string
		code   string
		mutate func(*soakCanaryTestFixture)
	}{
		{name: "planner denied", code: "planner_activation_not_allowed", mutate: func(f *soakCanaryTestFixture) {
			f.request.PlanReadback.ActivationAllowed = false
		}},
		{name: "plan input digest", code: "plan_input_digest_mismatch", mutate: func(f *soakCanaryTestFixture) {
			f.request.Activation.PlanInputDigest = "sha256:" + strings.Repeat("0", 64)
			signSoakCanaryActivation(&f.request.Activation)
		}},
		{name: "policy digest", code: "policy_digest_mismatch", mutate: func(f *soakCanaryTestFixture) {
			f.request.Activation.PolicyDigest = "sha256:" + strings.Repeat("0", 64)
			signSoakCanaryActivation(&f.request.Activation)
		}},
		{name: "source provenance", code: "source_provenance_mismatch", mutate: func(f *soakCanaryTestFixture) {
			f.request.SourceProvenance.Revision = strings.Repeat("b", 40)
			signSoakCanarySourceProvenance(&f.request.SourceProvenance)
		}},
		{name: "execution profile", code: "execution_profile_digest_mismatch", mutate: func(f *soakCanaryTestFixture) {
			f.request.Activation.ExecutionProfileDigest = "sha256:" + strings.Repeat("2", 64)
			signSoakCanaryActivation(&f.request.Activation)
		}},
		{name: "catalog digest", code: "command_catalog_digest_mismatch", mutate: func(f *soakCanaryTestFixture) {
			f.request.Activation.CommandCatalogDigest = "sha256:" + strings.Repeat("2", 64)
			signSoakCanaryActivation(&f.request.Activation)
		}},
		{name: "activation digest", code: "activation_manifest_digest_mismatch", mutate: func(f *soakCanaryTestFixture) {
			f.request.Activation.ActivationManifestDigest = "sha256:" + strings.Repeat("3", 64)
		}},
		{name: "partition identity", code: "activation_partition_mismatch", mutate: func(f *soakCanaryTestFixture) {
			f.request.Activation.Partitions[0].NodeID = "changed-node"
			signSoakCanaryActivation(&f.request.Activation)
		}},
		{name: "shell command", code: "free_form_shell_command", mutate: func(f *soakCanaryTestFixture) {
			f.request.Catalog.Commands[0].Argv[4] += "; echo unsafe"
			resignSoakCanaryCatalogAndActivation(f)
		}},
		{name: "working directory traversal", code: "working_directory_traversal", mutate: func(f *soakCanaryTestFixture) {
			f.request.Catalog.Commands[0].WorkingDirectory = "../outside"
			resignSoakCanaryCatalogAndActivation(f)
		}},
		{name: "package", code: "unapproved_package", mutate: func(f *soakCanaryTestFixture) {
			f.request.Catalog.Commands[0].Package = "./..."
			f.request.Catalog.Commands[0].Argv[1] = "./..."
			resignSoakCanaryCatalogAndActivation(f)
		}},
		{name: "regex", code: "unanchored_test_regex", mutate: func(f *soakCanaryTestFixture) {
			f.request.Catalog.Commands[0].TestRegex = "TestMission"
			f.request.Catalog.Commands[0].Argv[4] = "TestMission"
			resignSoakCanaryCatalogAndActivation(f)
		}},
		{name: "environment", code: "environment_injection", mutate: func(f *soakCanaryTestFixture) {
			f.request.Catalog.Commands[0].Environment = append(
				f.request.Catalog.Commands[0].Environment,
				SoakCanaryEnvironment{Name: "TOKEN", Value: "secret"},
			)
			resignSoakCanaryCatalogAndActivation(f)
		}},
		{name: "unapproved executable", code: "unapproved_executable", mutate: func(f *soakCanaryTestFixture) {
			f.request.Catalog.Commands[0].ExecutablePath = filepath.Join(filepath.Dir(f.goPath), "not-go")
			resignSoakCanaryCatalogAndActivation(f)
		}},
		{name: "unplanned test", code: "unplanned_test_id", mutate: func(f *soakCanaryTestFixture) {
			f.request.Catalog.Commands[0].TestID = "unplanned-test"
			resignSoakCanaryCatalogAndActivation(f)
		}},
		{name: "scale repeat", code: "scale_repeat_above_one", mutate: func(f *soakCanaryTestFixture) {
			f.request.Catalog.Commands[0].RequestedRepeatCount = 2
			f.request.Catalog.Commands[0].EffectiveRepeatCount = 2
			f.request.Catalog.Commands[0].Argv[5] = "-count=2"
			resignSoakCanaryCatalogAndActivation(f)
		}},
		{name: "scale dimension", code: "command_scale_dimension_mismatch", mutate: func(f *soakCanaryTestFixture) {
			f.request.Catalog.Commands[0].ScaleDimension.Value++
			resignSoakCanaryCatalogAndActivation(f)
		}},
		{name: "regular repeat", code: "regular_repeat_above_approved", mutate: func(f *soakCanaryTestFixture) {
			f.request.Catalog.Commands[1].RequestedRepeatCount = 4
			f.request.Catalog.Commands[1].EffectiveRepeatCount = 4
			f.request.Catalog.Commands[1].Argv[5] = "-count=4"
			resignSoakCanaryCatalogAndActivation(f)
		}},
		{name: "command timeout", code: "command_timeout_mismatch", mutate: func(f *soakCanaryTestFixture) {
			f.request.Catalog.Commands[0].TimeoutMS = 500
			f.request.Catalog.Commands[0].Argv[8] = "500ms"
			resignSoakCanaryCatalogAndActivation(f)
		}},
		{name: "retry on scale", code: "controlled_retry_binding_invalid", mutate: func(f *soakCanaryTestFixture) {
			f.request.Activation.ControlledRetryNodeID = f.request.Activation.Partitions[0].NodeID
			signSoakCanaryActivation(&f.request.Activation)
		}},
		{name: "phase clock reset", code: "phase_clock_reset", mutate: func(f *soakCanaryTestFixture) {
			f.request.Activation.PhaseStartUTC = "2026-07-29T21:01:00Z"
			signSoakCanaryActivation(&f.request.Activation)
		}},
		{name: "unsafe authority", code: "unsafe_authority_boundary", mutate: func(f *soakCanaryTestFixture) {
			f.request.Authority.Safety.RepositoryMutationAllowed = true
			signSoakCanaryAuthority(&f.request.Authority)
			f.request.Activation.AuthorityRecordDigest = f.request.Authority.AuthorityRecordDigest
			signSoakCanaryActivation(&f.request.Activation)
		}},
		{name: "handoff digest", code: "handoff_digest_mismatch", mutate: func(f *soakCanaryTestFixture) {
			f.request.Authority.HandoffSHA256 = "sha256:" + strings.Repeat("0", 64)
			signSoakCanaryAuthority(&f.request.Authority)
			f.request.Activation.AuthorityRecordDigest = f.request.Authority.AuthorityRecordDigest
			signSoakCanaryActivation(&f.request.Activation)
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := validSoakCanaryFixture(t)
			test.mutate(&fixture)
			executor := &soakCanaryFakeExecutor{}
			fixture.request.Executor = executor
			summary, err := RunSoakCanary(context.Background(), fixture.request)
			if err == nil {
				t.Fatalf("mutation was accepted: %+v", summary)
			}
			if !containsSoakConflict(summary.ConflictCodes, test.code) {
				t.Fatalf("conflicts=%v want=%q err=%v", summary.ConflictCodes, test.code, err)
			}
			if executor.starts != 0 || summary.ChildProcessLaunches != 0 {
				t.Fatalf("mutation reached executor: starts=%d summary=%+v", executor.starts, summary)
			}
		})
	}
}

func TestSoakCanaryBindsTotalRetryBudgetToAuthorityBeforeExecutorReachability(t *testing.T) {
	t.Run("matching cap", func(t *testing.T) {
		fixture := validSoakCanaryFixture(t)
		rebindSoakCanaryTotalRetryBudget(t, &fixture, 1)

		readback := ValidateSoakCanaryActivation(fixture.request)
		if fixture.request.Authority.MaximumRetryCount != 1 ||
			!readback.ActivationAllowed ||
			!reflect.DeepEqual(readback.ConflictCodes, []string{}) ||
			readback.ChildProcessLaunches != 0 {
			t.Fatalf("matching retry authority was rejected: authority=%d readback=%+v", fixture.request.Authority.MaximumRetryCount, readback)
		}
	})

	t.Run("mismatched cap", func(t *testing.T) {
		fixture := validSoakCanaryFixture(t)
		rebindSoakCanaryTotalRetryBudget(t, &fixture, 0)
		executor := &soakCanaryFakeExecutor{}
		fixture.request.Executor = executor

		summary, err := RunSoakCanary(context.Background(), fixture.request)
		if err == nil ||
			!reflect.DeepEqual(summary.ConflictCodes, []string{"retry_budget_authority_mismatch"}) {
			t.Fatalf("error=%v conflicts=%v want retry_budget_authority_mismatch", err, summary.ConflictCodes)
		}
		if executor.starts != 0 || summary.ChildProcessLaunches != 0 {
			t.Fatalf("retry authority mismatch reached executor: starts=%d summary=%+v", executor.starts, summary)
		}
	})
}

func TestSoakCanaryControlledRetryCheckpointRestartAndIdempotency(t *testing.T) {
	fixture := validSoakCanaryFixture(t)
	clock := &soakCanaryFakeClock{now: time.Date(2026, 7, 29, 21, 0, 0, 0, time.UTC)}
	executor := &soakCanaryFakeExecutor{clock: clock}
	fixture.request.Clock = clock
	fixture.request.Executor = executor

	summary, err := RunSoakCanary(context.Background(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Status != "completed" || summary.PlannedNodes != 10 ||
		summary.CompletedNodes != 10 || summary.TotalAttempts != 11 ||
		summary.ChildProcessLaunches != 10 || summary.ScaleLaunches != 1 ||
		summary.ControlledRetryCount != 1 || !summary.LocalTestExecutionPerformed {
		t.Fatalf("unexpected summary: %+v", summary)
	}
	if executor.starts != 10 {
		t.Fatalf("starts=%d want=10", executor.starts)
	}
	checkpoint, err := LoadSoakCanaryCheckpoint(fixture.request.CheckpointPath)
	if err != nil {
		t.Fatal(err)
	}
	if checkpoint.PhaseStartUTC != fixture.request.Activation.PhaseStartUTC ||
		len(checkpoint.Attempts) != 11 || len(checkpoint.CompletedNodeIDs) != 10 ||
		!checkpoint.ControlledRetryConsumed || !checkpoint.ScaleLaunchConsumed {
		t.Fatalf("checkpoint lost execution truth: %+v", checkpoint)
	}
	retryAttempts := attemptsForNode(checkpoint.Attempts, fixture.request.Activation.ControlledRetryNodeID)
	if len(retryAttempts) != 2 || retryAttempts[0].ChildProcessLaunched ||
		retryAttempts[0].OutcomeClass != "transient_infrastructure" ||
		!retryAttempts[1].ChildProcessLaunched {
		t.Fatalf("controlled retry evidence is wrong: %+v", retryAttempts)
	}
	for _, field := range []func(SoakCanaryAttempt) string{
		func(a SoakCanaryAttempt) string { return a.NodeID },
		func(a SoakCanaryAttempt) string { return a.TestID },
		func(a SoakCanaryAttempt) string { return a.SourceHead },
		func(a SoakCanaryAttempt) string { return a.PolicyDigest },
		func(a SoakCanaryAttempt) string { return a.PhaseStartUTC },
		func(a SoakCanaryAttempt) string { return a.ActivationManifestDigest },
	} {
		if field(retryAttempts[0]) != field(retryAttempts[1]) {
			t.Fatalf("retry changed a bound field: %+v", retryAttempts)
		}
	}

	replayed, err := RunSoakCanary(context.Background(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	if executor.starts != 10 || replayed.SummaryDigest != summary.SummaryDigest {
		t.Fatalf("completed replay was not idempotent: starts=%d first=%s second=%s",
			executor.starts, summary.SummaryDigest, replayed.SummaryDigest)
	}
}

func TestSoakCanaryCheckpointRestartDoesNotRepeatCompletedScaleNodeOrRetryFailedAttempt(t *testing.T) {
	fixture := validSoakCanaryFixture(t)
	clock := &soakCanaryFakeClock{now: time.Date(2026, 7, 29, 21, 0, 0, 0, time.UTC)}
	firstExecutor := &soakCanaryFakeExecutor{clock: clock, failAtStart: 2}
	fixture.request.Clock = clock
	fixture.request.Executor = firstExecutor
	if _, err := RunSoakCanary(context.Background(), fixture.request); err == nil ||
		!strings.Contains(err.Error(), "injected start failure") {
		t.Fatalf("partial run error=%v", err)
	}
	checkpoint, err := LoadSoakCanaryCheckpoint(fixture.request.CheckpointPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(checkpoint.CompletedNodeIDs) != 1 || !checkpoint.ScaleLaunchConsumed {
		t.Fatalf("partial checkpoint lost scale completion: %+v", checkpoint)
	}

	secondExecutor := &soakCanaryFakeExecutor{clock: clock}
	fixture.request.Executor = secondExecutor
	summary, err := RunSoakCanary(context.Background(), fixture.request)
	if err == nil || !containsSoakConflict(summary.ConflictCodes, "failed_attempt_terminal") {
		t.Fatalf("restart summary=%+v starts=%d", summary, secondExecutor.starts)
	}
	if summary.TotalAttempts != 3 || summary.ChildProcessLaunches != 1 ||
		summary.CompletedNodes != 1 || !summary.LocalTestExecutionPerformed ||
		secondExecutor.starts != 0 {
		t.Fatalf("restart lost runtime truth: summary=%+v starts=%d", summary, secondExecutor.starts)
	}
	for _, request := range secondExecutor.requests {
		if request.TestID == "scale-event-index-10000" {
			t.Fatalf("restart repeated completed scale node: %+v", secondExecutor.requests)
		}
	}
}

func TestSoakCanaryBoundsOutputAndCountsExactGoEvents(t *testing.T) {
	fixture := validSoakCanaryFixture(t)
	fixture.request.OutputLimitBytes = 512
	clock := &soakCanaryFakeClock{now: time.Date(2026, 7, 29, 21, 0, 0, 0, time.UTC)}
	executor := &soakCanaryFakeExecutor{
		clock:        clock,
		stdoutSuffix: strings.Repeat("x", 4096),
		stderr:       strings.Repeat("y", 4096),
	}
	fixture.request.Clock = clock
	fixture.request.Executor = executor

	summary, err := RunSoakCanary(context.Background(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := LoadSoakCanaryCheckpoint(fixture.request.CheckpointPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, attempt := range checkpoint.Attempts {
		if !attempt.ChildProcessLaunched {
			continue
		}
		if attempt.Stdout.Bytes > 512 || attempt.Stderr.Bytes > 512 ||
			!attempt.Stdout.Truncated || !attempt.Stderr.Truncated ||
			attempt.Stdout.SHA256 == "" || attempt.Stderr.SHA256 == "" {
			t.Fatalf("output was not bounded and digest-addressed: %+v", attempt)
		}
		wantPasses := attempt.EffectiveRepeatCount
		if attempt.GoTestEvents.MatchingPasses != wantPasses {
			t.Fatalf("test=%s passes=%d want=%d", attempt.TestID, attempt.GoTestEvents.MatchingPasses, wantPasses)
		}
	}
	if summary.ConflictCodes == nil || len(summary.ConflictCodes) != 0 {
		t.Fatalf("completed summary conflicts=%v", summary.ConflictCodes)
	}
}

func TestSoakCanaryTerminalSurfacesShareCanonicalPayloadWithDistinctStateDigests(t *testing.T) {
	fixture := validSoakCanaryFixture(t)
	clock := &soakCanaryFakeClock{now: time.Date(2026, 7, 29, 21, 0, 0, 0, time.UTC)}
	fixture.request.Clock = clock
	fixture.request.Executor = &soakCanaryFakeExecutor{clock: clock}
	summary, err := RunSoakCanary(context.Background(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	terminalRoot := filepath.Join(fixture.request.EvidenceRoot, "terminal")
	indexPath, err := WriteSoakCanaryTerminalBundle(terminalRoot, summary)
	if err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(terminalRoot, "import-state.json")
	imported, err := ImportTerminalIndex(terminalRoot, indexPath, statePath)
	if err != nil {
		t.Fatal(err)
	}
	if imported.IndexDigest != summary.TerminalIndexDigest {
		t.Fatalf("import digest=%s summary=%s", imported.IndexDigest, summary.TerminalIndexDigest)
	}
	readbacks, err := BuildSoakCanaryTerminalReadbacks(summary)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateTerminalSurfaceAgreement(readbacks); err != nil {
		t.Fatal(err)
	}
	stateDigests := map[string]bool{}
	for _, readback := range readbacks {
		if readback.IndexDigest != summary.TerminalIndexDigest ||
			!readback.ReadOnly || readback.ExecutesWork ||
			readback.Counts.Completed != 10 || !readback.FinalResponseAllowed {
			t.Fatalf("terminal readback is contradictory: %+v", readback)
		}
		stateDigests[readback.StateDigest] = true
	}
	if len(stateDigests) != 4 {
		t.Fatalf("state digests are not surface-specific: %+v", readbacks)
	}

	readbacks[0].Counts.Completed = 9
	signTerminalIndexImport(&readbacks[0])
	if err := ValidateTerminalSurfaceAgreement(readbacks); err == nil ||
		!strings.Contains(err.Error(), "canonical payload mismatch") {
		t.Fatalf("terminal mutation error=%v", err)
	}
}

func TestSoakCanaryEvidenceVerificationRejectsStdoutAndStderrDigestMismatch(t *testing.T) {
	for _, stream := range []string{"stdout", "stderr"} {
		t.Run(stream, func(t *testing.T) {
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
			if err := VerifySoakCanaryEvidence(fixture.request, checkpoint); err != nil {
				t.Fatal(err)
			}
			var launched SoakCanaryAttempt
			for _, attempt := range checkpoint.Attempts {
				if attempt.ChildProcessLaunched {
					launched = attempt
					break
				}
			}
			artifact := launched.Stdout
			if stream == "stderr" {
				artifact = launched.Stderr
			}
			path := filepath.Join(fixture.request.EvidenceRoot, artifact.Path)
			if err := os.WriteFile(path, []byte("altered"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := VerifySoakCanaryEvidence(fixture.request, checkpoint); err == nil ||
				!strings.Contains(err.Error(), stream+" digest mismatch") {
				t.Fatalf("tampered %s error=%v", stream, err)
			}
		})
	}
}

func TestSoakCanaryCompletionPersistenceWritesSummaryAndTerminalBundle(t *testing.T) {
	fixture := validSoakCanaryFixture(t)
	clock := &soakCanaryFakeClock{now: time.Date(2026, 7, 29, 21, 0, 0, 0, time.UTC)}
	fixture.request.Clock = clock
	fixture.request.Executor = &soakCanaryFakeExecutor{clock: clock}
	summary, err := RunSoakCanary(context.Background(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	if err := PersistSoakCanaryCompletion(fixture.request.EvidenceRoot, summary); err != nil {
		t.Fatal(err)
	}
	persisted, err := loadSoakCanaryJSON[SoakCanarySummary](
		filepath.Join(fixture.request.EvidenceRoot, "run-summary.json"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.SummaryDigest != summary.SummaryDigest {
		t.Fatalf("persisted summary=%s want=%s", persisted.SummaryDigest, summary.SummaryDigest)
	}
	indexPath := filepath.Join(fixture.request.EvidenceRoot, "terminal", "canonical-terminal-index.json")
	index, err := loadCanonicalTerminalIndex(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	if index.Digest != summary.TerminalIndexDigest {
		t.Fatalf("terminal digest=%s want=%s", index.Digest, summary.TerminalIndexDigest)
	}
}

func TestSoakCanaryStrictInputTransportRejectsUnknownDuplicateTrailingAndSpecialFiles(t *testing.T) {
	fixture := validSoakCanaryFixture(t)
	body, err := json.Marshal(fixture.request.Authority)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	valid := filepath.Join(dir, "authority.json")
	if err := os.WriteFile(valid, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSoakCanaryAuthority(valid); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		body []byte
		want string
	}{
		{name: "unknown", body: append(append([]byte{}, body[:len(body)-1]...), []byte(`,"unknown":true}`)...), want: "unknown field"},
		{name: "duplicate", body: stringsReplaceFirstBytes(body, []byte(`"schema":`), []byte(`"schema":"duplicate","schema":`)), want: "duplicate JSON key"},
		{name: "trailing", body: append(append([]byte{}, body...), []byte(` {}`)...), want: "trailing JSON"},
		{name: "malformed", body: []byte(`{"schema":`), want: "invalid JSON"},
		{name: "oversized", body: []byte(strings.Repeat("x", soakCanaryMaxInputBytes+1)), want: "exceeds"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(dir, test.name+".json")
			if err := os.WriteFile(path, test.body, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadSoakCanaryAuthority(path); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v want substring %q", err, test.want)
			}
		})
	}
	symlink := filepath.Join(dir, "symlink.json")
	createTestSymlink(t, valid, symlink)
	if _, err := LoadSoakCanaryAuthority(symlink); err == nil {
		t.Fatal("symlink input was accepted")
	}
}

func TestSoakCanaryCLIValidateOnlyUsesStrictInputsAndReportsZeroLaunches(t *testing.T) {
	fixture := validSoakCanaryFixture(t)
	repositoryRoot := fixture.request.RepositoryRoot
	dir := t.TempDir()
	planPath := writeSoakCanaryJSON(t, dir, "plan.json", fixture.request.PlanInput)
	planBody, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatal(err)
	}
	fixture.request.PlanFixtureSHA256 = digestBytes(planBody)
	fixture.request.Activation.PlanFixtureSHA256 = fixture.request.PlanFixtureSHA256
	signSoakCanaryActivation(&fixture.request.Activation)
	authorityPath := writeSoakCanaryJSON(t, dir, "authority.json", fixture.request.Authority)
	catalogPath := writeSoakCanaryJSON(t, dir, "catalog.json", fixture.request.Catalog)
	activationPath := writeSoakCanaryJSON(t, dir, "activation.json", fixture.request.Activation)

	dependencies := soakCanaryCLIDependencies{
		provenanceProvider: soakCanaryStaticProvenanceProvider{fixture.request.SourceProvenance},
		snapshotter:        fixture.request.Snapshotter,
		gitVerifier:        fixture.request.GitVerifier,
		executor:           &soakCanaryFakeExecutor{},
		persistCompletion:  PersistSoakCanaryCompletion,
	}
	var stdout strings.Builder
	err = runSoakCanaryCLIWithDependencies([]string{
		"--plan", planPath,
		"--authority", authorityPath,
		"--catalog", catalogPath,
		"--activation", activationPath,
		"--checkpoint", fixture.request.CheckpointPath,
		"--evidence-root", fixture.request.EvidenceRoot,
		"--repository-root", repositoryRoot,
		"--validate-only", "--json",
	}, &stdout, dependencies)
	if err != nil {
		t.Fatalf("error=%v stdout=%s", err, stdout.String())
	}
	var readback SoakCanaryActivationReadback
	if err := json.Unmarshal([]byte(stdout.String()), &readback); err != nil {
		t.Fatal(err)
	}
	if !readback.ActivationAllowed || readback.ChildProcessLaunches != 0 {
		t.Fatalf("validation-only readback is unsafe: %+v", readback)
	}
	if _, err := os.Stat(fixture.request.CheckpointPath); !os.IsNotExist(err) {
		t.Fatalf("validation-only created checkpoint: %v", err)
	}

	fixture.request.Activation.ActivationManifestDigest = "sha256:" + strings.Repeat("0", 64)
	writeSoakCanaryJSON(t, dir, "activation.json", fixture.request.Activation)
	stdout.Reset()
	err = runSoakCanaryCLIWithDependencies([]string{
		"--plan", planPath,
		"--authority", authorityPath,
		"--catalog", catalogPath,
		"--activation", activationPath,
		"--checkpoint", fixture.request.CheckpointPath,
		"--evidence-root", fixture.request.EvidenceRoot,
		"--repository-root", repositoryRoot,
		"--validate-only", "--json",
	}, &stdout, dependencies)
	if err == nil || !strings.Contains(err.Error(), "activation_manifest_digest_mismatch") {
		t.Fatalf("invalid error=%v stdout=%s", err, stdout.String())
	}
	if _, err := os.Stat(fixture.request.CheckpointPath); !os.IsNotExist(err) {
		t.Fatalf("invalid validation created checkpoint: %v", err)
	}
}

type soakCanaryStaticProvenanceProvider struct {
	value SoakCanarySourceProvenance
}

func (provider soakCanaryStaticProvenanceProvider) SourceProvenance() (
	SoakCanarySourceProvenance,
	error,
) {
	return provider.value, nil
}

func TestSoakCanaryRepositoryActivationFixturesExposeExactDigestDecision(t *testing.T) {
	validPath := filepath.Join("..", "..", "examples", "valid", "soak-canary-activation.json")
	valid, err := LoadSoakCanaryActivation(validPath)
	if err != nil {
		t.Fatal(err)
	}
	unsigned := valid
	signSoakCanaryActivation(&unsigned)
	if valid.ActivationManifestDigest != unsigned.ActivationManifestDigest ||
		len(valid.Partitions) != 10 || !valid.CanaryExecutionAuthorized {
		t.Fatalf("valid activation fixture is contradictory: %+v", valid)
	}

	invalidPath := filepath.Join("..", "..", "examples", "invalid", "soak-canary-activation-digest-mismatch.json")
	invalid, err := LoadSoakCanaryActivation(invalidPath)
	if err != nil {
		t.Fatal(err)
	}
	unsigned = invalid
	signSoakCanaryActivation(&unsigned)
	if invalid.ActivationManifestDigest == unsigned.ActivationManifestDigest {
		t.Fatal("invalid activation fixture unexpectedly has a valid digest")
	}

	matrixPath := filepath.Join("..", "..", "examples", "invalid", "soak-canary-validation-matrix.json")
	matrixBody, err := readBoundedRegularFile(matrixPath, soakCanaryMaxInputBytes)
	if err != nil {
		t.Fatal(err)
	}
	var matrix struct {
		Schema string `json:"schema"`
		Cases  []struct {
			ID                   string `json:"id"`
			ExpectedConflictCode string `json:"expected_conflict_code,omitempty"`
			ExpectedError        string `json:"expected_error,omitempty"`
		} `json:"cases"`
	}
	matrix, err = decodeSoakCanaryJSON[struct {
		Schema string `json:"schema"`
		Cases  []struct {
			ID                   string `json:"id"`
			ExpectedConflictCode string `json:"expected_conflict_code,omitempty"`
			ExpectedError        string `json:"expected_error,omitempty"`
		} `json:"cases"`
	}](matrixBody)
	if err != nil {
		t.Fatal(err)
	}
	required := map[string]string{
		"altered-plan-digest":           "plan_input_digest_mismatch",
		"altered-policy-digest":         "policy_digest_mismatch",
		"wrong-source-head":             "source_head_mismatch",
		"modified-build-provenance":     "source_provenance_mismatch",
		"changed-repository-snapshot":   "repository_snapshot_digest_mismatch",
		"altered-command-catalog":       "command_catalog_digest_mismatch",
		"altered-partition-binding":     "activation_partition_mismatch",
		"altered-activation-digest":     "activation_manifest_digest_mismatch",
		"unplanned-test":                "activation_catalog_bijection_mismatch",
		"regular-before-scale":          "scale_partition_not_first",
		"scale-repeat-amplification":    "scale_repeat_above_one",
		"changed-scale-dimension":       "command_scale_dimension_mismatch",
		"second-retry":                  "second_retry_requested",
		"failed-attempt-restart":        "failed_attempt_terminal",
		"changed-retry-binding":         "controlled_retry_binding_invalid",
		"free-form-shell-command":       "free_form_shell_command",
		"changed-executable-provenance": "executable_provenance_mismatch",
		"symlinked-runtime-root":        "unsafe_runtime_path",
	}
	requiredErrors := map[string]string{
		"resigned-false-go-event-counts": "Go test event counts mismatch",
	}
	seen := map[string]bool{}
	for _, test := range matrix.Cases {
		if test.ID == "" || seen[test.ID] ||
			(test.ExpectedConflictCode == "") == (test.ExpectedError == "") {
			t.Fatalf("invalid public matrix row: %+v", test)
		}
		seen[test.ID] = true
		if want, exists := required[test.ID]; exists && test.ExpectedConflictCode != want {
			t.Fatalf("matrix case=%s conflict=%s want=%s", test.ID, test.ExpectedConflictCode, want)
		}
		if want, exists := requiredErrors[test.ID]; exists && test.ExpectedError != want {
			t.Fatalf("matrix case=%s error=%s want=%s", test.ID, test.ExpectedError, want)
		}
	}
	for id := range required {
		if !seen[id] {
			t.Fatalf("public invalid matrix omitted %s", id)
		}
		for id := range requiredErrors {
			if !seen[id] {
				t.Fatalf("public invalid matrix omitted %s", id)
			}
		}
	}
}

type soakCanaryTestFixture struct {
	request SoakCanaryRunRequest
	goPath  string
}

func validSoakCanaryFixture(t *testing.T) soakCanaryTestFixture {
	t.Helper()
	input := validSoakPlanInput()
	input.PlanID = "operational-plan-001"
	input.MissionID = "operational-mission-001"
	input.TestCatalog = []SoakTestEntry{}
	input.DurationHistory = []SoakDurationHistory{}
	input.Partitions = []SoakPartitionRequest{}
	input.PartitionBudgets = []SoakPartitionBudget{}
	scaleID := "scale-event-index-10000"
	regular := []struct {
		id, name string
	}{
		{"regular-issue-reader-special-files", "TestIssueRepairRequestReaderRejectsSymlinkAndFIFO"},
		{"regular-correlated-import-binding", "TestCorrelatedImportRequiresArtifactRoleAndDigestWithoutMutation"},
		{"regular-archive-duplicate-authority", "TestMissionArchiveEntryPointsRejectDuplicateAuthorityAtAnyDepth"},
		{"regular-checkpoint-doctor", "TestContinueWritesCheckpointBundleAndDoctorSupervisorHealth"},
		{"regular-final-reconciliation-shape", "TestFinalReconciliationEventSearchFixturePreservesReadbackShape"},
		{"regular-final-rollup-ready-denial", "TestFinalRollupReadyNodeDenialFixtureValidatesSchema"},
		{"regular-ledger-compaction", "TestMissionLedgerCompactionTrimsHistoryAndRecordsEvidence"},
		{"regular-dashboard-corrupt-unrelated", "TestMissionDashboardUsesOneMissionReadPathWithCorruptUnrelatedRecord"},
		{"regular-objective-pending-blueprint", "TestObjectiveWorkflowRoutesPendingBlueprint"},
	}
	input.TestCatalog = append(input.TestCatalog, SoakTestEntry{
		ID: scaleID, Classification: "scale", RequestedRepeatCount: 1,
		ScaleDimension: &SoakScaleDimension{Unit: "records", Value: 10_000},
	})
	input.DurationHistory = append(input.DurationHistory, SoakDurationHistory{
		TestID: scaleID, SourceHead: input.SourceHead,
		ExecutionProfileDigest: input.ExecutionProfile.Digest,
		Unit:                   "milliseconds", Samples: []int64{100, 110, 120},
	})
	for _, test := range regular {
		input.TestCatalog = append(input.TestCatalog, SoakTestEntry{
			ID: test.id, Classification: "regular", RequestedRepeatCount: 3,
		})
		input.DurationHistory = append(input.DurationHistory, SoakDurationHistory{
			TestID: test.id, SourceHead: input.SourceHead,
			ExecutionProfileDigest: input.ExecutionProfile.Digest,
			Unit:                   "milliseconds", Samples: []int64{10, 12, 14},
		})
	}
	input.Partitions = append(input.Partitions, SoakPartitionRequest{
		PartitionID: "partition-01", NodeID: "node-01",
		TestIDs: []string{scaleID, regular[0].id},
	})
	input.PartitionBudgets = append(input.PartitionBudgets, SoakPartitionBudget{
		PartitionID: "partition-01", NodeBudgetMS: 10_000,
	})
	for index, test := range regular[1:] {
		partitionID := "partition-" + strconv.Itoa(index+2)
		input.Partitions = append(input.Partitions, SoakPartitionRequest{
			PartitionID: partitionID, NodeID: "node-" + strconv.Itoa(index+2),
			TestIDs: []string{test.id},
		})
		input.PartitionBudgets = append(input.PartitionBudgets, SoakPartitionBudget{
			PartitionID: partitionID, NodeBudgetMS: 10_000,
		})
	}
	input.Budgets.MaximumTests = 10
	input.Budgets.MaximumPartitions = 10
	input.Budgets.SetupOverheadMS = 10
	input.Budgets.SafetyOverheadMS = 10
	input.TimeoutPolicy.PerAttemptTimeoutMS = 1_000
	input.TimeoutPolicy.TotalNodeTimeoutMS = 2_000
	input.Lease = SoakLeaseBudget{MinimumMS: 1, TargetMS: 10_000, MaximumMS: 20_000}
	input.PolicyDigest = soakPolicyDigest(input)
	input.Activation.BoundPolicyDigest = input.PolicyDigest
	readback := buildValidSoakPlan(t, input)
	if len(readback.Partitions) != 10 {
		t.Fatalf("test plan partitions=%d want=10", len(readback.Partitions))
	}

	root := t.TempDir()
	goPath := filepath.Join(root, "go")
	if err := os.WriteFile(goPath, []byte("bounded fake go executable"), 0o700); err != nil {
		t.Fatal(err)
	}
	evidenceRoot := t.TempDir()
	handoffPath := filepath.Join(evidenceRoot, "handoff.md")
	handoffBody := []byte("bounded operational canary authority\n")
	if err := os.WriteFile(handoffPath, handoffBody, 0o600); err != nil {
		t.Fatal(err)
	}
	authority := SoakCanaryAuthority{
		Schema: SoakCanaryAuthoritySchema, CampaignID: "campaign-001",
		CanaryID: "canary-001", MissionID: input.MissionID,
		HandoffPath:    handoffPath,
		HandoffSHA256:  digestBytes(handoffBody),
		AuthorityClass: "bounded_local_operational_canary",
		EvidenceRoot:   evidenceRoot, SourceHead: input.SourceHead,
		CreatedAtUTC: "2026-07-29T21:00:00Z", HardWallMS: 45 * 60 * 1000,
		MaximumPlannedNodes: 10, MaximumAttempts: 11,
		MaximumChildProcessLaunches: 10, MaximumScaleLaunches: 1,
		MaximumRetryCount: 1, LocalTestExecutionAllowed: true,
		Safety: SoakCanarySafety{},
	}
	signSoakCanaryAuthority(&authority)

	catalog := SoakCanaryCommandCatalog{
		Schema: SoakCanaryCommandCatalogSchema, SourceHead: input.SourceHead,
		ExecutionProfileID:     input.ExecutionProfile.ID,
		ExecutionProfileDigest: input.ExecutionProfile.Digest,
	}
	testNames := map[string]string{
		scaleID: "TestMissionEventIndexScaleMetricsExposeReadAndEventCounts/10000",
	}
	for _, test := range regular {
		testNames[test.id] = test.name
	}
	for _, partition := range readback.Partitions {
		testID := partition.Tests[0]
		testName := testNames[testID]
		regex := "^" + testName + "$"
		if testID == scaleID {
			regex = "^TestMissionEventIndexScaleMetricsExposeReadAndEventCounts$/^10000$"
		}
		command := SoakCanaryCommand{
			TestID: testID, TestName: testName,
			ExecutablePath: goPath, ExecutableSHA256: digestBytes([]byte("bounded fake go executable")),
			WorkingDirectory: ".", Package: "./internal/mission",
			TestRegex: regex, Classification: partition.Classification,
			RequestedRepeatCount: partition.RequestedRepeatCount,
			EffectiveRepeatCount: partition.EffectiveRepeatCount,
			TimeoutMS:            input.TimeoutPolicy.PerAttemptTimeoutMS,
			Race:                 true, OutputFormat: "go-test-json",
			Environment: []SoakCanaryEnvironment{
				{Name: "GOTOOLCHAIN", Value: "local"},
				{Name: "GOPROXY", Value: "off"},
				{Name: "GOSUMDB", Value: "off"},
				{Name: "GOVCS", Value: "*:off"},
			},
		}
		if testID == scaleID {
			dimension := *input.TestCatalog[0].ScaleDimension
			command.ScaleDimension = &dimension
		}
		command.Argv = []string{
			"test", command.Package, "-race", "-run", command.TestRegex,
			"-count=" + strconv.Itoa(command.EffectiveRepeatCount),
			"-json", "-timeout", strconv.FormatInt(command.TimeoutMS, 10) + "ms",
		}
		catalog.Commands = append(catalog.Commands, command)
	}
	signSoakCanaryCommandCatalog(&catalog)
	provenance := SoakCanarySourceProvenance{
		Schema: SoakCanarySourceProvenanceSchema, Revision: input.SourceHead,
		Modified: false, Provider: "injected_test",
	}
	signSoakCanarySourceProvenance(&provenance)
	repositorySnapshot, err := BuildSoakCanaryRepositorySnapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	activation, err := BuildSoakCanaryActivation(
		input, readback, "sha256:"+strings.Repeat("5", 64), authority, catalog,
		provenance, repositorySnapshot,
		"2026-07-29T21:00:00Z", readback.Partitions[1].NodeID,
	)
	if err != nil {
		t.Fatal(err)
	}
	request := SoakCanaryRunRequest{
		PlanInput: input, PlanReadback: readback,
		PlanFixtureSHA256: activation.PlanFixtureSHA256,
		Authority:         authority, Catalog: catalog, Activation: activation,
		SourceProvenance: provenance, RepositorySnapshot: repositorySnapshot,
		Snapshotter:    PureGoSoakCanaryRepositorySnapshotter{},
		GitVerifier:    soakCanaryCleanGitVerifier{},
		RepositoryRoot: root, EvidenceRoot: evidenceRoot,
		CheckpointPath:   filepath.Join(evidenceRoot, "checkpoints", "checkpoint.json"),
		OutputLimitBytes: 64 * 1024,
	}
	return soakCanaryTestFixture{request: request, goPath: goPath}
}

func rebindSoakCanaryTotalRetryBudget(t *testing.T, fixture *soakCanaryTestFixture, cap int) {
	t.Helper()
	setSoakMaximumTotalRetries(t, fixture.request.PlanInput.RetryPolicy, soakIntPointer(cap))
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

func rebindSoakCanarySource(t *testing.T, fixture *soakCanaryTestFixture, sourceHead string) {
	t.Helper()
	fixture.request.PlanInput.SourceHead = sourceHead
	for index := range fixture.request.PlanInput.DurationHistory {
		fixture.request.PlanInput.DurationHistory[index].SourceHead = sourceHead
	}
	fixture.request.PlanInput.PolicyDigest = soakPolicyDigest(fixture.request.PlanInput)
	fixture.request.PlanInput.Activation.BoundPolicyDigest = fixture.request.PlanInput.PolicyDigest
	fixture.request.PlanReadback = buildValidSoakPlan(t, fixture.request.PlanInput)
	fixture.request.Authority.SourceHead = sourceHead
	signSoakCanaryAuthority(&fixture.request.Authority)
	fixture.request.Catalog.SourceHead = sourceHead
	signSoakCanaryCommandCatalog(&fixture.request.Catalog)
	fixture.request.SourceProvenance.Revision = sourceHead
	signSoakCanarySourceProvenance(&fixture.request.SourceProvenance)
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

func writeSoakCanaryJSON(t *testing.T, dir, name string, value any) string {
	t.Helper()
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, append(body, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func resignSoakCanaryCatalogAndActivation(fixture *soakCanaryTestFixture) {
	signSoakCanaryCommandCatalog(&fixture.request.Catalog)
	fixture.request.Activation.CommandCatalogDigest = fixture.request.Catalog.CommandCatalogDigest
	signSoakCanaryActivation(&fixture.request.Activation)
}

type soakCanaryFakeClock struct {
	now time.Time
}

type soakCanaryCleanGitVerifier struct{}

func (soakCanaryCleanGitVerifier) Verify(string, string) error {
	return nil
}

func (clock *soakCanaryFakeClock) Now() time.Time {
	return clock.now
}

func (clock *soakCanaryFakeClock) advance(duration time.Duration) {
	clock.now = clock.now.Add(duration)
}

type soakCanaryFakeExecutor struct {
	clock        *soakCanaryFakeClock
	starts       int
	failAtStart  int
	requests     []SoakCanaryExecRequest
	stdoutSuffix string
	stderr       string
}

func (executor *soakCanaryFakeExecutor) Start(_ context.Context, request SoakCanaryExecRequest) (SoakCanaryProcess, error) {
	executor.starts++
	executor.requests = append(executor.requests, request)
	if executor.failAtStart > 0 && executor.starts == executor.failAtStart {
		return nil, errors.New("injected start failure")
	}
	if request.ExecutablePath == "" {
		return nil, errors.New("missing executable")
	}
	var lines []string
	for index := 0; index < request.ExpectedPassCount; index++ {
		event := map[string]any{
			"Action": "pass", "Package": "./internal/mission",
			"Test": request.ExpectedTestName,
		}
		body, _ := json.Marshal(event)
		lines = append(lines, string(body))
	}
	unrelated, _ := json.Marshal(map[string]any{
		"Action": "pass", "Package": "./internal/mission", "Test": "Unrelated",
	})
	lines = append(lines, string(unrelated), executor.stdoutSuffix)
	return &soakCanaryFakeProcess{
		pid: 10_000 + executor.starts, clock: executor.clock,
		result: SoakCanaryProcessResult{
			ExitCode: 0, ElapsedMS: 5,
			Stdout: []byte(strings.Join(lines, "\n")),
			Stderr: []byte(executor.stderr),
		},
	}, nil
}

type soakCanaryFakeProcess struct {
	pid    int
	clock  *soakCanaryFakeClock
	result SoakCanaryProcessResult
}

func (process *soakCanaryFakeProcess) PID() int {
	return process.pid
}

func (process *soakCanaryFakeProcess) Wait() SoakCanaryProcessResult {
	if process.clock != nil {
		process.clock.advance(time.Duration(process.result.ElapsedMS) * time.Millisecond)
	}
	return process.result
}

func attemptsForNode(attempts []SoakCanaryAttempt, nodeID string) []SoakCanaryAttempt {
	var selected []SoakCanaryAttempt
	for _, attempt := range attempts {
		if attempt.NodeID == nodeID {
			selected = append(selected, attempt)
		}
	}
	return selected
}

func stringsReplaceFirstBytes(body, old, replacement []byte) []byte {
	return []byte(strings.Replace(string(body), string(old), string(replacement), 1))
}
