package mission

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type SoakCanaryClock interface {
	Now() time.Time
}

type SoakCanaryExecRequest struct {
	TestID            string
	ExpectedTestName  string
	ExpectedPassCount int
	ExecutablePath    string
	Argv              []string
	RepositoryRoot    string
	WorkingDirectory  string
	Environment       []SoakCanaryEnvironment
	TimeoutMS         int64
	OutputLimitBytes  int
}

type SoakCanaryProcessResult struct {
	ExitCode        int
	Signal          string
	ElapsedMS       int64
	Stdout          []byte
	Stderr          []byte
	StdoutTruncated bool
	StderrTruncated bool
}

type SoakCanaryExecutor interface {
	Start(context.Context, SoakCanaryExecRequest) (SoakCanaryProcess, error)
}

type SoakCanaryProcess interface {
	PID() int
	Wait() SoakCanaryProcessResult
}

type realSoakCanaryClock struct{}

func (realSoakCanaryClock) Now() time.Time {
	return time.Now().UTC()
}

type OSExecSoakCanaryExecutor struct{}

type osExecSoakCanaryProcess struct {
	command *exec.Cmd
	started time.Time
	stdout  *soakCanaryBoundedBuffer
	stderr  *soakCanaryBoundedBuffer
	once    sync.Once
	result  SoakCanaryProcessResult
}

func (OSExecSoakCanaryExecutor) Start(ctx context.Context, request SoakCanaryExecRequest) (SoakCanaryProcess, error) {
	if !filepath.IsAbs(request.ExecutablePath) || request.WorkingDirectory != "." ||
		!validSoakCanaryRuntimeEnvironment(request.Environment) || request.OutputLimitBytes <= 0 {
		return nil, errors.New("soak canary executor received an unvalidated request")
	}
	command := exec.CommandContext(ctx, request.ExecutablePath, request.Argv...)
	command.Dir = filepath.Join(request.RepositoryRoot, request.WorkingDirectory)
	command.Env = soakCanarySanitizedEnvironment(request.Environment)
	stdout := &soakCanaryBoundedBuffer{limit: request.OutputLimitBytes}
	stderr := &soakCanaryBoundedBuffer{limit: request.OutputLimitBytes}
	command.Stdout = stdout
	command.Stderr = stderr
	started := time.Now()
	if err := command.Start(); err != nil {
		return nil, err
	}
	return &osExecSoakCanaryProcess{
		command: command, started: started, stdout: stdout, stderr: stderr,
	}, nil
}

func (process *osExecSoakCanaryProcess) PID() int {
	if process.command.Process == nil {
		return 0
	}
	return process.command.Process.Pid
}

func (process *osExecSoakCanaryProcess) Wait() SoakCanaryProcessResult {
	process.once.Do(func() {
		err := process.command.Wait()
		process.result = SoakCanaryProcessResult{
			ExitCode:  soakCanaryExitCode(err),
			ElapsedMS: time.Since(process.started).Milliseconds(),
			Stdout:    process.stdout.Bytes(), Stderr: process.stderr.Bytes(),
			StdoutTruncated: process.stdout.truncated,
			StderrTruncated: process.stderr.truncated,
		}
		if exit, ok := err.(*exec.ExitError); ok && exit.ProcessState != nil {
			process.result.Signal = soakCanaryProcessSignal(exit.ProcessState.String())
		}
	})
	return process.result
}

type soakCanaryBoundedBuffer struct {
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func (buffer *soakCanaryBoundedBuffer) Write(body []byte) (int, error) {
	originalLength := len(body)
	remaining := buffer.limit - buffer.buffer.Len()
	if remaining <= 0 {
		buffer.truncated = buffer.truncated || originalLength > 0
		return originalLength, nil
	}
	if len(body) > remaining {
		buffer.truncated = true
		body = body[:remaining]
	}
	_, _ = buffer.buffer.Write(body)
	return originalLength, nil
}

func (buffer *soakCanaryBoundedBuffer) Bytes() []byte {
	return append([]byte(nil), buffer.buffer.Bytes()...)
}

func RunSoakCanary(ctx context.Context, request SoakCanaryRunRequest) (SoakCanarySummary, error) {
	validation := ValidateSoakCanaryActivation(request)
	if !validation.ActivationAllowed {
		summary := rejectedSoakCanarySummary(request, validation.ConflictCodes)
		return summary, fmt.Errorf("soak canary activation rejected: %s", strings.Join(validation.ConflictCodes, ","))
	}
	if request.Executor == nil {
		summary := rejectedSoakCanarySummary(request, []string{"executor_missing"})
		return summary, errors.New("soak canary executor is required")
	}
	if request.Snapshotter == nil {
		summary := rejectedSoakCanarySummary(request, []string{"repository_snapshotter_missing"})
		return summary, errors.New("soak canary repository snapshotter is required")
	}
	if request.GitVerifier == nil {
		summary := rejectedSoakCanarySummary(request, []string{"repository_git_verifier_missing"})
		return summary, errors.New("soak canary Git verifier is required")
	}
	if request.Clock == nil {
		request.Clock = realSoakCanaryClock{}
	}
	if request.OutputLimitBytes <= 0 {
		request.OutputLimitBytes = soakCanaryDefaultOutputBytes
	}
	if request.HeartbeatInterval <= 0 {
		request.HeartbeatInterval = soakCanaryDefaultHeartbeat
	}
	runtimeEnvironment, err := prepareSoakCanaryRuntimeEnvironment(request)
	if err != nil {
		summary := rejectedSoakCanarySummary(request, []string{"unsafe_runtime_path"})
		return summary, err
	}
	now := request.Clock.Now().UTC()
	phaseStart, _ := time.Parse(time.RFC3339, request.Activation.PhaseStartUTC)
	if now.Before(phaseStart) || now.Sub(phaseStart).Milliseconds() > request.Authority.HardWallMS {
		summary := rejectedSoakCanarySummary(request, []string{"authority_time_window_expired"})
		return summary, errors.New("soak canary authority time window expired")
	}

	checkpoint, err := loadOrCreateSoakCanaryCheckpoint(request)
	if err != nil {
		code := "checkpoint_invalid"
		if strings.Contains(err.Error(), "semantic mismatch") {
			code = "checkpoint_semantic_mismatch"
		}
		summary := rejectedSoakCanarySummary(request, []string{code})
		return summary, err
	}
	for _, attempt := range checkpoint.Attempts {
		if attempt.ExecutionState == "reserved" || attempt.ExecutionState == "running" {
			code := "indeterminate_child_launch"
			if attempt.Classification == "scale" {
				code = "indeterminate_scale_launch"
			}
			summary := runtimeSoakCanarySummary(request, checkpoint, []string{code}, request.Clock.Now().UTC())
			return summary, fmt.Errorf("soak canary restart blocked by %s", code)
		}
		if attempt.ExecutionState == "completed" && attempt.OutcomeClass != "passed" &&
			!soakCanaryDesignatedRetryPrecondition(request, attempt) {
			code := "failed_attempt_terminal"
			summary := runtimeSoakCanarySummary(request, checkpoint, []string{code}, request.Clock.Now().UTC())
			return summary, fmt.Errorf(
				"soak canary restart blocked by terminal attempt %s#%d",
				attempt.NodeID,
				attempt.AttemptNumber,
			)
		}
	}
	if len(checkpoint.CompletedNodeIDs) == len(request.Activation.Partitions) {
		completedAt, err := time.Parse(time.RFC3339Nano, checkpoint.CompletedAtUTC)
		if err != nil {
			return runtimeSoakCanarySummary(
				request, checkpoint, []string{"checkpoint_completion_time_invalid"}, request.Clock.Now().UTC(),
			), err
		}
		return ReconcileSoakCanary(request, checkpoint, completedAt)
	}

	commands := map[string]SoakCanaryCommand{}
	for _, command := range request.Catalog.Commands {
		commands[command.TestID] = command
	}
	for _, partition := range request.Activation.Partitions {
		if soakCanaryStringPresent(checkpoint.CompletedNodeIDs, partition.NodeID) {
			continue
		}
		if request.Clock.Now().UTC().Sub(phaseStart).Milliseconds() >= request.Authority.HardWallMS {
			return runtimeSoakCanarySummary(
				request, checkpoint, []string{"hard_wall_reached"}, request.Clock.Now().UTC(),
			), errors.New("soak canary hard wall reached")
		}
		command, commandFound := commands[partition.TestID]
		if !commandFound {
			return runtimeSoakCanarySummary(
				request, checkpoint, []string{"activation_catalog_bijection_mismatch"}, request.Clock.Now().UTC(),
			), errors.New("soak canary partition has no bound command")
		}
		if partition.NodeID == request.Activation.ControlledRetryNodeID &&
			!checkpoint.ControlledRetryConsumed {
			attempt := newSoakCanaryAttempt(request, checkpoint, partition, command)
			attempt.AttemptNumber = 1
			attempt.ExecutionState = "completed"
			attempt.OutcomeClass = "transient_infrastructure"
			attempt.StartedAtUTC = request.Clock.Now().UTC().Format(time.RFC3339Nano)
			attempt.CompletedAtUTC = attempt.StartedAtUTC
			attempt.ExitCode = -1
			attempt.Stdout = emptySoakCanaryOutput()
			attempt.Stderr = emptySoakCanaryOutput()
			if _, err := persistSoakCanaryAttemptTransition(
				request, &checkpoint, -1, attempt, "completed",
			); err != nil {
				return runtimeSoakCanarySummary(
					request, checkpoint, []string{"checkpoint_write_failed"}, request.Clock.Now().UTC(),
				), err
			}
		}

		attemptNumber := soakCanaryNodeAttemptCount(checkpoint.Attempts, partition.NodeID) + 1
		if partition.Classification == "scale" && attemptNumber != 1 {
			return runtimeSoakCanarySummary(
				request, checkpoint, []string{"scale_retry_requested"}, request.Clock.Now().UTC(),
			), errors.New("soak canary scale retry is prohibited")
		}
		if attemptNumber > 2 {
			return runtimeSoakCanarySummary(
				request, checkpoint, []string{"second_retry_requested"}, request.Clock.Now().UTC(),
			), errors.New("soak canary second retry is prohibited")
		}
		attempt := newSoakCanaryAttempt(request, checkpoint, partition, command)
		attempt.AttemptNumber = attemptNumber
		attempt.StartedAtUTC = request.Clock.Now().UTC().Format(time.RFC3339Nano)
		attempt.ExecutionState = "reserved"
		attempt.OutcomeClass = "launch_reserved"
		attempt.Stdout = emptySoakCanaryOutput()
		attempt.Stderr = emptySoakCanaryOutput()
		attemptIndex, err := persistSoakCanaryAttemptTransition(
			request, &checkpoint, -1, attempt, "reserved",
		)
		if err != nil {
			return runtimeSoakCanarySummary(
				request, checkpoint, []string{"checkpoint_write_failed"}, request.Clock.Now().UTC(),
			), err
		}
		attempt = checkpoint.Attempts[attemptIndex]
		if request.AfterLaunchReservation != nil {
			if err := request.AfterLaunchReservation(attempt); err != nil {
				return runtimeSoakCanarySummary(
					request, checkpoint, []string{"launch_reservation_interrupted"}, request.Clock.Now().UTC(),
				), err
			}
		}
		execRequest := SoakCanaryExecRequest{
			TestID: partition.TestID, ExpectedTestName: command.TestName,
			ExpectedPassCount: partition.EffectiveRepeatCount,
			ExecutablePath:    command.ExecutablePath,
			Argv:              append([]string(nil), command.Argv...),
			RepositoryRoot:    request.RepositoryRoot,
			WorkingDirectory:  command.WorkingDirectory,
			Environment:       append([]SoakCanaryEnvironment(nil), runtimeEnvironment...),
			TimeoutMS:         soakCanaryAttemptContextLimit(request, checkpoint, partition, phaseStart),
			OutputLimitBytes:  request.OutputLimitBytes,
		}
		if execRequest.TimeoutMS <= 0 {
			attempt.ExecutionState = "completed"
			attempt.OutcomeClass = "execution_budget_exhausted"
			attempt.CompletedAtUTC = request.Clock.Now().UTC().Format(time.RFC3339Nano)
			attempt.ExitCode = -1
			if _, persistErr := persistSoakCanaryAttemptTransition(
				request, &checkpoint, attemptIndex, attempt, "completed",
			); persistErr != nil {
				return runtimeSoakCanarySummary(
					request, checkpoint, []string{"checkpoint_write_failed"}, request.Clock.Now().UTC(),
				), persistErr
			}
			return runtimeSoakCanarySummary(
				request, checkpoint, []string{"execution_budget_exhausted"}, request.Clock.Now().UTC(),
			), errors.New("soak canary execution budget is exhausted")
		}
		if !validSoakCanaryExecutable(command) {
			attempt.ExecutionState = "completed"
			attempt.OutcomeClass = "executable_provenance_mismatch_before_launch"
			attempt.CompletedAtUTC = request.Clock.Now().UTC().Format(time.RFC3339Nano)
			attempt.ExitCode = -1
			if _, persistErr := persistSoakCanaryAttemptTransition(
				request, &checkpoint, attemptIndex, attempt, "completed",
			); persistErr != nil {
				return runtimeSoakCanarySummary(
					request, checkpoint, []string{"checkpoint_write_failed"}, request.Clock.Now().UTC(),
				), persistErr
			}
			return runtimeSoakCanarySummary(
				request, checkpoint, []string{"executable_provenance_mismatch"}, request.Clock.Now().UTC(),
			), errors.New("soak canary executable provenance changed before launch")
		}
		if gitErr := verifySoakCanaryGitRepository(request); gitErr != nil {
			attempt.ExecutionState = "completed"
			attempt.OutcomeClass = "repository_git_state_mismatch_before_launch"
			attempt.CompletedAtUTC = request.Clock.Now().UTC().Format(time.RFC3339Nano)
			attempt.TotalAttemptElapsedMS = soakCanaryElapsedMS(attempt.StartedAtUTC, attempt.CompletedAtUTC)
			attempt.ElapsedMS = attempt.TotalAttemptElapsedMS
			attempt.ExitCode = -1
			if _, persistErr := persistSoakCanaryAttemptTransition(
				request, &checkpoint, attemptIndex, attempt, "completed",
			); persistErr != nil {
				return runtimeSoakCanarySummary(
					request, checkpoint, []string{"checkpoint_write_failed"}, request.Clock.Now().UTC(),
				), errors.Join(gitErr, persistErr)
			}
			return runtimeSoakCanarySummary(
				request, checkpoint, []string{"repository_git_state_mismatch"}, request.Clock.Now().UTC(),
			), gitErr
		}
		beforeSnapshot, snapshotErr := verifySoakCanaryRepositorySnapshot(request)
		if snapshotErr != nil {
			attempt.ExecutionState = "completed"
			attempt.OutcomeClass = "repository_snapshot_mismatch_before_launch"
			attempt.CompletedAtUTC = request.Clock.Now().UTC().Format(time.RFC3339Nano)
			attempt.TotalAttemptElapsedMS = soakCanaryElapsedMS(attempt.StartedAtUTC, attempt.CompletedAtUTC)
			attempt.ElapsedMS = attempt.TotalAttemptElapsedMS
			attempt.ExitCode = -1
			if _, persistErr := persistSoakCanaryAttemptTransition(
				request, &checkpoint, attemptIndex, attempt, "completed",
			); persistErr != nil {
				return runtimeSoakCanarySummary(
					request, checkpoint, []string{"checkpoint_write_failed"}, request.Clock.Now().UTC(),
				), errors.Join(snapshotErr, persistErr)
			}
			return runtimeSoakCanarySummary(
				request, checkpoint, []string{"repository_snapshot_mismatch"}, request.Clock.Now().UTC(),
			), snapshotErr
		}
		attempt.RepositorySnapshotBeforeDigest = beforeSnapshot.SnapshotDigest
		attemptContext, cancel := context.WithTimeout(
			ctx, time.Duration(execRequest.TimeoutMS)*time.Millisecond,
		)
		process, startErr := request.Executor.Start(attemptContext, execRequest)
		if startErr != nil {
			cancel()
			attempt.ExecutionState = "completed"
			attempt.OutcomeClass = "child_process_start_failed"
			attempt.CompletedAtUTC = request.Clock.Now().UTC().Format(time.RFC3339Nano)
			attempt.TotalAttemptElapsedMS = soakCanaryElapsedMS(attempt.StartedAtUTC, attempt.CompletedAtUTC)
			attempt.ElapsedMS = attempt.TotalAttemptElapsedMS
			attempt.ExitCode = -1
			if _, persistErr := persistSoakCanaryAttemptTransition(
				request, &checkpoint, attemptIndex, attempt, "completed",
			); persistErr != nil {
				return runtimeSoakCanarySummary(
					request, checkpoint, []string{"checkpoint_write_failed"}, request.Clock.Now().UTC(),
				), errors.Join(startErr, persistErr)
			}
			return runtimeSoakCanarySummary(
				request, checkpoint, []string{"child_process_start_failed"}, request.Clock.Now().UTC(),
			), startErr
		}
		attempt.ChildProcessLaunched = true
		attempt.ChildPID = process.PID()
		attempt.ChildStartedAtUTC = request.Clock.Now().UTC().Format(time.RFC3339Nano)
		attempt.ExecutionState = "running"
		attempt.OutcomeClass = "running"
		if _, err := persistSoakCanaryAttemptTransition(
			request, &checkpoint, attemptIndex, attempt, "running",
		); err != nil {
			cancel()
			reapErr := completeSoakCanaryRunningCheckpointFailure(
				request,
				&checkpoint,
				attemptIndex,
				partition,
				command,
				attemptNumber,
				process,
			)
			return runtimeSoakCanarySummary(
				request, checkpoint, []string{"checkpoint_write_failed"}, request.Clock.Now().UTC(),
			), errors.Join(err, reapErr)
		}
		attempt = checkpoint.Attempts[attemptIndex]
		result, heartbeatErr := waitForSoakCanaryProcess(
			request,
			&checkpoint,
			attemptIndex,
			process,
		)
		cancel()
		attempt = checkpoint.Attempts[attemptIndex]
		attempt.ExecutionState = "completed"
		attempt.ChildElapsedMS = result.ElapsedMS
		attempt.ExitCode = result.ExitCode
		attempt.Signal = result.Signal
		stdout, stdoutTruncated := boundSoakCanaryOutput(result.Stdout, request.OutputLimitBytes)
		stderr, stderrTruncated := boundSoakCanaryOutput(result.Stderr, request.OutputLimitBytes)
		stdoutTruncated = stdoutTruncated || result.StdoutTruncated
		stderrTruncated = stderrTruncated || result.StderrTruncated
		attempt.GoTestEvents = parseSoakCanaryGoTestEvents(stdout, command.TestName)
		var completionErr error
		attempt.OutcomeClass = "passed"
		if heartbeatErr != nil {
			attempt.OutcomeClass = "heartbeat_checkpoint_write_failed"
			completionErr = errors.Join(completionErr, heartbeatErr)
		}
		attempt.Stdout, err = persistSoakCanaryOutput(
			request, partition.NodeID, attemptNumber, "stdout.jsonl", stdout, stdoutTruncated,
		)
		if err != nil {
			attempt.Stdout = emptySoakCanaryOutput()
			attempt.OutcomeClass = "stdout_evidence_write_failed"
			completionErr = errors.Join(completionErr, err)
		}
		attempt.Stderr, err = persistSoakCanaryOutput(
			request, partition.NodeID, attemptNumber, "stderr.txt", stderr, stderrTruncated,
		)
		if err != nil {
			attempt.Stderr = emptySoakCanaryOutput()
			if attempt.OutcomeClass == "passed" {
				attempt.OutcomeClass = "stderr_evidence_write_failed"
			}
			completionErr = errors.Join(completionErr, err)
		}
		afterSnapshot, repositoryErr := verifySoakCanaryRepositorySnapshot(request)
		attempt.RepositorySnapshotAfterDigest = afterSnapshot.SnapshotDigest
		gitErr := verifySoakCanaryGitRepository(request)
		executableProvenanceValid := validSoakCanaryExecutable(command)
		if attempt.OutcomeClass == "passed" && result.ExitCode != 0 {
			attempt.OutcomeClass = "test_failure"
		}
		if attempt.OutcomeClass == "passed" &&
			attempt.GoTestEvents.MatchingPasses != partition.EffectiveRepeatCount {
			attempt.OutcomeClass = "go_event_count_mismatch"
		}
		if repositoryErr != nil {
			attempt.OutcomeClass = "repository_snapshot_mismatch"
			completionErr = errors.Join(completionErr, repositoryErr)
		}
		if gitErr != nil {
			attempt.OutcomeClass = "repository_git_state_mismatch"
			completionErr = errors.Join(completionErr, gitErr)
		}
		if !executableProvenanceValid {
			attempt.OutcomeClass = "executable_provenance_mismatch"
			completionErr = errors.Join(
				completionErr,
				errors.New("soak canary executable provenance changed after launch"),
			)
		}
		attempt.CompletedAtUTC = request.Clock.Now().UTC().Format(time.RFC3339Nano)
		attempt.TotalAttemptElapsedMS = soakCanaryElapsedMS(attempt.StartedAtUTC, attempt.CompletedAtUTC)
		if attempt.TotalAttemptElapsedMS < attempt.ChildElapsedMS {
			attempt.TotalAttemptElapsedMS = attempt.ChildElapsedMS
		}
		attempt.ElapsedMS = attempt.TotalAttemptElapsedMS
		if attempt.ElapsedMS > partition.EstimatedDurationMS {
			attempt.OutcomeClass = "actual_duration_above_estimate"
		}
		if attempt.ElapsedMS > partition.PerAttemptTimeoutMS {
			attempt.OutcomeClass = "per_attempt_timeout_exceeded"
		}
		limitConflicts := evaluateSoakCanaryLimits(soakCanaryLimitInputForAttempt(
			request, checkpoint, partition, attempt, phaseStart,
		))
		if len(limitConflicts) > 0 {
			attempt.OutcomeClass = soakCanaryPrimaryLimitConflict(limitConflicts)
		}
		if _, err := persistSoakCanaryAttemptTransition(
			request, &checkpoint, attemptIndex, attempt, "completed",
		); err != nil {
			return runtimeSoakCanarySummary(
				request, checkpoint, []string{"checkpoint_write_failed"}, request.Clock.Now().UTC(),
			), err
		}
		if attempt.OutcomeClass != "passed" {
			runErr := fmt.Errorf("soak canary node %s failed: %s", partition.NodeID, attempt.OutcomeClass)
			runErr = errors.Join(runErr, completionErr)
			return runtimeSoakCanarySummary(
				request, checkpoint, []string{attempt.OutcomeClass}, request.Clock.Now().UTC(),
			), runErr
		}
	}
	checkpoint.CompletedAtUTC = request.Clock.Now().UTC().Format(time.RFC3339Nano)
	signSoakCanaryCheckpoint(&checkpoint)
	if err := validateExistingSoakCanaryPathComponents(
		request.EvidenceRoot, request.CheckpointPath,
	); err != nil {
		return runtimeSoakCanarySummary(
			request, checkpoint, []string{"unsafe_runtime_path"}, request.Clock.Now().UTC(),
		), err
	}
	if err := writeSoakCanaryCheckpoint(request.CheckpointPath, checkpoint); err != nil {
		return runtimeSoakCanarySummary(
			request, checkpoint, []string{"checkpoint_write_failed"}, request.Clock.Now().UTC(),
		), err
	}
	completedAt, err := time.Parse(time.RFC3339Nano, checkpoint.CompletedAtUTC)
	if err != nil {
		return runtimeSoakCanarySummary(
			request, checkpoint, []string{"checkpoint_completion_time_invalid"}, request.Clock.Now().UTC(),
		), err
	}
	return ReconcileSoakCanary(request, checkpoint, completedAt)
}

func completeSoakCanaryRunningCheckpointFailure(
	request SoakCanaryRunRequest,
	checkpoint *SoakCanaryCheckpoint,
	attemptIndex int,
	partition SoakCanaryPartitionBinding,
	command SoakCanaryCommand,
	attemptNumber int,
	process SoakCanaryProcess,
) error {
	result := process.Wait()
	attempt := checkpoint.Attempts[attemptIndex]
	attempt.ExecutionState = "completed"
	attempt.OutcomeClass = "running_checkpoint_write_failed_reaped"
	attempt.ChildElapsedMS = result.ElapsedMS
	attempt.ExitCode = result.ExitCode
	attempt.Signal = result.Signal

	stdout, stdoutTruncated := boundSoakCanaryOutput(result.Stdout, request.OutputLimitBytes)
	stderr, stderrTruncated := boundSoakCanaryOutput(result.Stderr, request.OutputLimitBytes)
	stdoutTruncated = stdoutTruncated || result.StdoutTruncated
	stderrTruncated = stderrTruncated || result.StderrTruncated
	attempt.GoTestEvents = parseSoakCanaryGoTestEvents(stdout, command.TestName)

	var observedErr error
	var err error
	attempt.Stdout, err = persistSoakCanaryOutput(
		request, partition.NodeID, attemptNumber, "stdout.jsonl", stdout, stdoutTruncated,
	)
	if err != nil {
		attempt.Stdout = emptySoakCanaryOutput()
		observedErr = errors.Join(observedErr, err)
	}
	attempt.Stderr, err = persistSoakCanaryOutput(
		request, partition.NodeID, attemptNumber, "stderr.txt", stderr, stderrTruncated,
	)
	if err != nil {
		attempt.Stderr = emptySoakCanaryOutput()
		observedErr = errors.Join(observedErr, err)
	}
	afterSnapshot, snapshotErr := verifySoakCanaryRepositorySnapshot(request)
	attempt.RepositorySnapshotAfterDigest = afterSnapshot.SnapshotDigest
	observedErr = errors.Join(observedErr, snapshotErr)
	observedErr = errors.Join(observedErr, verifySoakCanaryGitRepository(request))
	if !validSoakCanaryExecutable(command) {
		observedErr = errors.Join(
			observedErr,
			errors.New("soak canary executable provenance changed after launch"),
		)
	}
	attempt.CompletedAtUTC = request.Clock.Now().UTC().Format(time.RFC3339Nano)
	attempt.TotalAttemptElapsedMS = soakCanaryElapsedMS(attempt.StartedAtUTC, attempt.CompletedAtUTC)
	if attempt.TotalAttemptElapsedMS < attempt.ChildElapsedMS {
		attempt.TotalAttemptElapsedMS = attempt.ChildElapsedMS
	}
	attempt.ElapsedMS = attempt.TotalAttemptElapsedMS
	if _, err := persistSoakCanaryAttemptTransition(
		request,
		checkpoint,
		attemptIndex,
		attempt,
		"completed",
	); err != nil {
		observedErr = errors.Join(observedErr, err)
	}
	return observedErr
}

func soakCanaryDesignatedRetryPrecondition(
	request SoakCanaryRunRequest,
	attempt SoakCanaryAttempt,
) bool {
	return attempt.NodeID == request.Activation.ControlledRetryNodeID &&
		attempt.AttemptNumber == 1 &&
		attempt.ExecutionState == "completed" &&
		attempt.OutcomeClass == request.Activation.ControlledRetryReason &&
		!attempt.ChildProcessLaunched &&
		attempt.ReservationSequence == 0 &&
		attempt.RunningSequence == 0
}

func waitForSoakCanaryProcess(
	request SoakCanaryRunRequest,
	checkpoint *SoakCanaryCheckpoint,
	attemptIndex int,
	process SoakCanaryProcess,
) (SoakCanaryProcessResult, error) {
	results := make(chan SoakCanaryProcessResult, 1)
	go func() {
		results <- process.Wait()
	}()
	ticker := time.NewTicker(request.HeartbeatInterval)
	defer ticker.Stop()
	var heartbeatErr error
	for {
		select {
		case result := <-results:
			return result, heartbeatErr
		case <-ticker.C:
			if heartbeatErr != nil {
				continue
			}
			attempt := checkpoint.Attempts[attemptIndex]
			if _, err := persistSoakCanaryAttemptTransition(
				request,
				checkpoint,
				attemptIndex,
				attempt,
				"heartbeat",
			); err != nil {
				heartbeatErr = err
			}
		}
	}
}

func loadOrCreateSoakCanaryCheckpoint(request SoakCanaryRunRequest) (SoakCanaryCheckpoint, error) {
	checkpoint, err := LoadSoakCanaryCheckpoint(request.CheckpointPath)
	if err == nil {
		if err := validateSoakCanaryCheckpoint(request, checkpoint); err != nil {
			return checkpoint, err
		}
		return checkpoint, nil
	}
	if !os.IsNotExist(err) {
		return checkpoint, err
	}
	checkpoint = SoakCanaryCheckpoint{
		Schema:   SoakCanaryCheckpointSchema,
		CanaryID: request.Activation.CanaryID, MissionID: request.Activation.MissionID,
		PlanID: request.Activation.PlanID, PhaseStartUTC: request.Activation.PhaseStartUTC,
		SourceHead:               request.Activation.SourceHead,
		SourceProvenanceDigest:   request.Activation.SourceProvenanceDigest,
		RepositorySnapshotDigest: request.Activation.RepositorySnapshotDigest,
		PlanInputDigest:          request.Activation.PlanInputDigest,
		PolicyDigest:             request.Activation.PolicyDigest,
		CommandCatalogDigest:     request.Activation.CommandCatalogDigest,
		AuthorityRecordDigest:    request.Activation.AuthorityRecordDigest,
		ActivationManifestDigest: request.Activation.ActivationManifestDigest,
		Attempts:                 []SoakCanaryAttempt{}, Events: []SoakCanaryCheckpointEvent{},
		CompletedNodeIDs: []string{},
	}
	signSoakCanaryCheckpoint(&checkpoint)
	if err := writeSoakCanaryCheckpoint(request.CheckpointPath, checkpoint); err != nil {
		return checkpoint, err
	}
	return checkpoint, nil
}

func newSoakCanaryAttempt(
	request SoakCanaryRunRequest,
	checkpoint SoakCanaryCheckpoint,
	partition SoakCanaryPartitionBinding,
	command SoakCanaryCommand,
) SoakCanaryAttempt {
	argvBody, _ := json.Marshal(command.Argv)
	return SoakCanaryAttempt{
		Schema:   SoakCanaryAttemptSchema,
		CanaryID: request.Activation.CanaryID, MissionID: request.Activation.MissionID,
		PlanID: request.Activation.PlanID, PartitionID: partition.PartitionID,
		NodeID: partition.NodeID, TestID: partition.TestID,
		PhaseStartUTC:                  request.Activation.PhaseStartUTC,
		SourceHead:                     request.Activation.SourceHead,
		SourceProvenanceDigest:         request.Activation.SourceProvenanceDigest,
		RepositorySnapshotBeforeDigest: request.Activation.RepositorySnapshotDigest,
		PlanInputDigest:                request.Activation.PlanInputDigest,
		PolicyDigest:                   request.Activation.PolicyDigest,
		ExecutionProfileDigest:         request.Activation.ExecutionProfileDigest,
		CommandCatalogDigest:           request.Activation.CommandCatalogDigest,
		AuthorityRecordDigest:          request.Activation.AuthorityRecordDigest,
		ActivationManifestDigest:       request.Activation.ActivationManifestDigest,
		CommandArgvDigest:              digestBytes(argvBody),
		RequestedRepeatCount:           partition.RequestedRepeatCount,
		EffectiveRepeatCount:           partition.EffectiveRepeatCount,
		Classification:                 partition.Classification, ScaleDimension: partition.ScaleDimension,
		CheckpointBeforeDigest: checkpoint.CheckpointDigest,
		Safety:                 request.Activation.Safety,
	}
}

func persistSoakCanaryAttemptTransition(
	request SoakCanaryRunRequest,
	checkpoint *SoakCanaryCheckpoint,
	attemptIndex int,
	attempt SoakCanaryAttempt,
	eventName string,
) (int, error) {
	if attemptIndex < 0 && len(checkpoint.Attempts) >= request.Authority.MaximumAttempts {
		return -1, errors.New("soak canary attempt limit exceeded")
	}
	if attemptIndex >= len(checkpoint.Attempts) {
		return -1, errors.New("soak canary attempt transition index is invalid")
	}
	prior := checkpoint.CheckpointDigest
	checkpoint.Sequence++
	checkpoint.PriorCheckpointDigest = prior
	attempt.CheckpointAfterSequence = checkpoint.Sequence
	switch eventName {
	case "reserved":
		attempt.ReservationSequence = checkpoint.Sequence
	case "running":
		attempt.RunningSequence = checkpoint.Sequence
	case "heartbeat":
		if attempt.ExecutionState != "running" || attempt.RunningSequence <= 0 {
			return -1, errors.New("soak canary heartbeat requires a running attempt")
		}
	case "completed":
		attempt.CompletionSequence = checkpoint.Sequence
	default:
		return -1, errors.New("soak canary attempt transition is invalid")
	}
	signSoakCanaryAttempt(&attempt)
	if attemptIndex < 0 {
		checkpoint.Attempts = append(checkpoint.Attempts, attempt)
		attemptIndex = len(checkpoint.Attempts) - 1
	} else {
		checkpoint.Attempts[attemptIndex] = attempt
	}
	event := SoakCanaryCheckpointEvent{
		Sequence: checkpoint.Sequence, Event: eventName,
		NodeID: attempt.NodeID, AttemptNumber: attempt.AttemptNumber,
		AttemptSnapshotDigest:  soakCanaryAttemptSnapshotDigest(attempt, eventName, checkpoint.Sequence),
		CheckpointBeforeDigest: prior,
	}
	if len(checkpoint.Events) > 0 {
		event.PriorEventDigest = checkpoint.Events[len(checkpoint.Events)-1].EventDigest
	}
	signSoakCanaryCheckpointEvent(&event)
	checkpoint.Events = append(checkpoint.Events, event)
	if eventName == "completed" && attempt.OutcomeClass == "transient_infrastructure" {
		if checkpoint.ControlledRetryConsumed {
			return -1, errors.New("soak canary controlled retry was already consumed")
		}
		checkpoint.ControlledRetryConsumed = true
	}
	if eventName == "reserved" && attempt.Classification == "scale" {
		if checkpoint.ScaleLaunchConsumed {
			return -1, errors.New("soak canary scale launch was already consumed")
		}
		checkpoint.ScaleLaunchConsumed = true
	}
	if eventName == "completed" && attempt.OutcomeClass == "passed" {
		completed := map[string]bool{}
		for _, nodeID := range checkpoint.CompletedNodeIDs {
			completed[nodeID] = true
		}
		if completed[attempt.NodeID] {
			return -1, errors.New("soak canary duplicate node completion")
		}
		completed[attempt.NodeID] = true
		checkpoint.CompletedNodeIDs = sortedSoakCanaryKeys(completed)
	}
	signSoakCanaryCheckpoint(checkpoint)
	if err := validateExistingSoakCanaryPathComponents(
		request.EvidenceRoot, request.CheckpointPath,
	); err != nil {
		return -1, err
	}
	if err := writeSoakCanaryCheckpoint(request.CheckpointPath, *checkpoint); err != nil {
		return -1, err
	}
	return attemptIndex, nil
}

func persistSoakCanaryOutput(
	request SoakCanaryRunRequest,
	nodeID string,
	attemptNumber int,
	suffix string,
	body []byte,
	truncated bool,
) (SoakCanaryOutputArtifact, error) {
	relative := filepath.Join(
		"nodes", safeSoakCanaryFilename(nodeID),
		"attempt-"+strconv.Itoa(attemptNumber)+"."+suffix,
	)
	path := filepath.Join(request.EvidenceRoot, relative)
	if !pathWithin(request.EvidenceRoot, path) {
		return SoakCanaryOutputArtifact{}, errors.New("soak canary output path escaped evidence root")
	}
	if err := validateExistingSoakCanaryPathComponents(request.EvidenceRoot, path); err != nil {
		return SoakCanaryOutputArtifact{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return SoakCanaryOutputArtifact{}, err
	}
	if err := writeAtomicFile(path, body, 0o600); err != nil {
		return SoakCanaryOutputArtifact{}, err
	}
	return SoakCanaryOutputArtifact{
		Path: relative, SHA256: digestBytes(body), Bytes: len(body), Truncated: truncated,
	}, nil
}

func parseSoakCanaryGoTestEvents(body []byte, expectedTest string) SoakCanaryGoTestCounts {
	counts := SoakCanaryGoTestCounts{}
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 64*1024), soakCanaryDefaultOutputBytes)
	for scanner.Scan() {
		var event struct {
			Action string `json:"Action"`
			Test   string `json:"Test"`
		}
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			continue
		}
		counts.TotalEvents++
		if event.Action == "pass" && event.Test == expectedTest {
			counts.MatchingPasses++
		}
	}
	return counts
}

func boundSoakCanaryOutput(body []byte, limit int) ([]byte, bool) {
	if len(body) <= limit {
		return append([]byte(nil), body...), false
	}
	return append([]byte(nil), body[:limit]...), true
}

func emptySoakCanaryOutput() SoakCanaryOutputArtifact {
	return SoakCanaryOutputArtifact{SHA256: digestBytes(nil)}
}

func rejectedSoakCanarySummary(request SoakCanaryRunRequest, conflicts []string) SoakCanarySummary {
	summary := SoakCanarySummary{
		Schema: SoakCanarySummarySchema, Status: "rejected",
		CanaryID: request.Activation.CanaryID, MissionID: request.Activation.MissionID,
		PlanID: request.Activation.PlanID, SourceHead: request.Activation.SourceHead,
		SourceProvenanceDigest:   request.Activation.SourceProvenanceDigest,
		RepositorySnapshotDigest: request.Activation.RepositorySnapshotDigest,
		PlanInputDigest:          request.Activation.PlanInputDigest,
		PolicyDigest:             request.Activation.PolicyDigest,
		CommandCatalogDigest:     request.Activation.CommandCatalogDigest,
		AuthorityRecordDigest:    request.Activation.AuthorityRecordDigest,
		ActivationManifestDigest: request.Activation.ActivationManifestDigest,
		PlannedPartitions:        len(request.Activation.Partitions),
		PlannedNodes:             len(request.Activation.Partitions),
		PhaseStartUTC:            request.Activation.PhaseStartUTC,
		LeaseMinimumMS:           request.PlanInput.Lease.MinimumMS,
		LeaseTargetMS:            request.PlanInput.Lease.TargetMS,
		LeaseMaximumMS:           request.PlanInput.Lease.MaximumMS,
		ConflictCodes:            append([]string(nil), conflicts...),
		Safety:                   request.Activation.Safety,
	}
	sort.Strings(summary.ConflictCodes)
	signSoakCanarySummary(&summary)
	return summary
}

func runtimeSoakCanarySummary(
	request SoakCanaryRunRequest,
	checkpoint SoakCanaryCheckpoint,
	conflicts []string,
	observedAt time.Time,
) SoakCanarySummary {
	summary := rejectedSoakCanarySummary(request, conflicts)
	summary.Status = "failed"
	summary.CompletedNodes = len(checkpoint.CompletedNodeIDs)
	summary.TotalAttempts = len(checkpoint.Attempts)
	summary.Attempts = append([]SoakCanaryAttempt(nil), checkpoint.Attempts...)
	summary.CheckpointDigest = checkpoint.CheckpointDigest
	for _, attempt := range checkpoint.Attempts {
		summary.TotalAttemptElapsedMS += attempt.TotalAttemptElapsedMS
		if attempt.ChildProcessLaunched {
			summary.ChildProcessLaunches++
			summary.TotalChildElapsedMS += attempt.ChildElapsedMS
			if attempt.Classification == "scale" {
				summary.ScaleLaunches++
			}
		}
		if attempt.OutcomeClass == "transient_infrastructure" {
			summary.ControlledRetryCount++
		}
		if attempt.OutcomeClass == "passed" {
			summary.PassedAttemptCount++
		}
	}
	summary.LocalTestExecutionPerformed = summary.ChildProcessLaunches > 0
	if checkpoint.CompletedAtUTC != "" {
		summary.CompletedAtUTC = checkpoint.CompletedAtUTC
	} else {
		summary.CompletedAtUTC = observedAt.UTC().Format(time.RFC3339Nano)
	}
	if phaseStart, err := time.Parse(time.RFC3339, request.Activation.PhaseStartUTC); err == nil {
		summary.PhaseElapsedMS = observedAt.UTC().Sub(phaseStart).Milliseconds()
	}
	signSoakCanarySummary(&summary)
	return summary
}

func soakCanaryElapsedMS(startedAt, completedAt string) int64 {
	start, startErr := time.Parse(time.RFC3339Nano, startedAt)
	completed, completedErr := time.Parse(time.RFC3339Nano, completedAt)
	if startErr != nil || completedErr != nil || completed.Before(start) {
		return 0
	}
	return completed.Sub(start).Milliseconds()
}

func soakCanarySanitizedEnvironment(bound []SoakCanaryEnvironment) []string {
	environment := make([]string, 0, len(bound))
	for _, pair := range bound {
		environment = append(environment, pair.Name+"="+pair.Value)
	}
	return environment
}

func prepareSoakCanaryRuntimeEnvironment(request SoakCanaryRunRequest) ([]SoakCanaryEnvironment, error) {
	runtimeRoot := filepath.Join(request.EvidenceRoot, "runtime")
	if err := validateExistingSoakCanaryPathComponents(request.EvidenceRoot, runtimeRoot); err != nil {
		return nil, err
	}
	directories := map[string]string{
		"HOME":     filepath.Join(runtimeRoot, "home"),
		"TMPDIR":   filepath.Join(runtimeRoot, "tmp"),
		"GOCACHE":  filepath.Join(runtimeRoot, "gocache"),
		"GOTMPDIR": filepath.Join(runtimeRoot, "gotmp"),
	}
	for _, path := range directories {
		if !pathWithin(request.EvidenceRoot, path) {
			return nil, errors.New("soak canary runtime directory escaped evidence root")
		}
		if err := os.MkdirAll(path, 0o700); err != nil {
			return nil, err
		}
		if err := validateExistingSoakCanaryPathComponents(request.EvidenceRoot, path); err != nil {
			return nil, err
		}
	}
	pathEntries := []string{filepath.Join(runtime.GOROOT(), "bin"), "/usr/bin", "/bin"}
	if runtime.GOOS == "windows" {
		systemRoot := filepath.Join(filepath.VolumeName(runtime.GOROOT())+string(os.PathSeparator), "Windows")
		pathEntries = []string{
			filepath.Join(runtime.GOROOT(), "bin"),
			filepath.Join(systemRoot, "System32"),
		}
	}
	pathValue := strings.Join(pathEntries, string(os.PathListSeparator))
	environment := []SoakCanaryEnvironment{
		{Name: "GOTOOLCHAIN", Value: "local"},
		{Name: "GOPROXY", Value: "off"},
		{Name: "GOSUMDB", Value: "off"},
		{Name: "GOVCS", Value: "*:off"},
		{Name: "HOME", Value: directories["HOME"]},
		{Name: "TMPDIR", Value: directories["TMPDIR"]},
		{Name: "GOCACHE", Value: directories["GOCACHE"]},
		{Name: "GOTMPDIR", Value: directories["GOTMPDIR"]},
		{Name: "PATH", Value: pathValue},
	}
	if runtime.GOOS == "windows" {
		environment = append(environment, SoakCanaryEnvironment{
			Name:  "SYSTEMROOT",
			Value: filepath.Join(filepath.VolumeName(runtime.GOROOT())+string(os.PathSeparator), "Windows"),
		})
	}
	return environment, nil
}

func validSoakCanaryRuntimeEnvironment(environment []SoakCanaryEnvironment) bool {
	required := map[string]bool{
		"GOTOOLCHAIN": true, "GOPROXY": true, "GOSUMDB": true, "GOVCS": true,
		"HOME": true, "TMPDIR": true, "GOCACHE": true, "GOTMPDIR": true, "PATH": true,
	}
	if runtime.GOOS == "windows" {
		required["SYSTEMROOT"] = true
	}
	seen := map[string]bool{}
	for _, pair := range environment {
		if !required[pair.Name] || seen[pair.Name] || pair.Value == "" {
			return false
		}
		seen[pair.Name] = true
	}
	return len(seen) == len(required)
}

func validateSoakCanaryRuntimePaths(repositoryRoot, evidenceRoot, checkpointPath string) error {
	for _, path := range []string{repositoryRoot, evidenceRoot} {
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("soak canary runtime root is missing, non-directory, or symlinked")
		}
	}
	repositoryResolved, err := filepath.EvalSymlinks(repositoryRoot)
	if err != nil {
		return err
	}
	evidenceResolved, err := filepath.EvalSymlinks(evidenceRoot)
	if err != nil {
		return err
	}
	if pathWithin(repositoryResolved, evidenceResolved) ||
		pathWithin(evidenceResolved, repositoryResolved) {
		return errors.New("soak canary evidence root and repository must be disjoint")
	}
	if !pathWithin(evidenceRoot, checkpointPath) {
		return errors.New("soak canary checkpoint escaped evidence root")
	}
	for _, path := range []string{
		checkpointPath,
		filepath.Join(evidenceRoot, "nodes"),
		filepath.Join(evidenceRoot, "runtime"),
		filepath.Join(evidenceRoot, "terminal"),
		filepath.Join(evidenceRoot, "run-summary.json"),
		filepath.Join(evidenceRoot, "run-summary.provisional.json"),
	} {
		if err := validateExistingSoakCanaryPathComponents(evidenceRoot, path); err != nil {
			return err
		}
	}
	return nil
}

func validateExistingSoakCanaryPathComponents(root, path string) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return err
	}
	if !pathWithin(root, path) {
		return errors.New("soak canary path escaped validation root")
	}
	for current := filepath.Clean(path); ; current = filepath.Dir(current) {
		info, statErr := os.Lstat(current)
		if statErr == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("soak canary path component is symlinked: %s", current)
		}
		if statErr != nil && !os.IsNotExist(statErr) {
			return statErr
		}
		if sameCleanPath(current, root) {
			break
		}
	}
	return nil
}

func soakCanaryAttemptContextLimit(
	request SoakCanaryRunRequest,
	checkpoint SoakCanaryCheckpoint,
	partition SoakCanaryPartitionBinding,
	phaseStart time.Time,
) int64 {
	elapsed := request.Clock.Now().UTC().Sub(phaseStart).Milliseconds()
	nodeElapsed := int64(0)
	for _, prior := range checkpoint.Attempts {
		if prior.NodeID == partition.NodeID && prior.ExecutionState == "completed" {
			nodeElapsed += prior.TotalAttemptElapsedMS
		}
	}
	limits := []int64{
		partition.PerAttemptTimeoutMS,
		request.Authority.HardWallMS - elapsed,
		request.PlanInput.Lease.MaximumMS - elapsed,
		partition.TotalNodeTimeoutMS - nodeElapsed,
		partition.NodeBudgetMS - nodeElapsed,
	}
	if soakCanaryNodeAttemptCount(checkpoint.Attempts, partition.NodeID) > 1 {
		limits = append(limits, partition.RetryAllowanceMS)
	}
	result := limits[0]
	for _, limit := range limits[1:] {
		if limit < result {
			result = limit
		}
	}
	return result
}

func soakCanaryLimitInputForAttempt(
	request SoakCanaryRunRequest,
	checkpoint SoakCanaryCheckpoint,
	partition SoakCanaryPartitionBinding,
	attempt SoakCanaryAttempt,
	phaseStart time.Time,
) SoakCanaryLimitInput {
	nodeElapsed := attempt.TotalAttemptElapsedMS
	aggregateElapsed := attempt.TotalAttemptElapsedMS
	for _, prior := range checkpoint.Attempts {
		if prior.NodeID == attempt.NodeID && prior.AttemptNumber == attempt.AttemptNumber {
			continue
		}
		if prior.ExecutionState != "completed" {
			continue
		}
		if prior.NodeID == attempt.NodeID {
			nodeElapsed += prior.TotalAttemptElapsedMS
		}
		if prior.ChildProcessLaunched {
			aggregateElapsed += prior.TotalAttemptElapsedMS
		}
	}
	return SoakCanaryLimitInput{
		ChildElapsedMS: attempt.ChildElapsedMS, TotalAttemptElapsedMS: attempt.TotalAttemptElapsedMS,
		NodeElapsedMS: nodeElapsed, AggregateElapsedMS: aggregateElapsed,
		AggregateLimitMS:    request.PlanReadback.LeaseBudget.TotalPlannedWithRetryMS,
		PhaseElapsedMS:      request.Clock.Now().UTC().Sub(phaseStart).Milliseconds(),
		PerAttemptTimeoutMS: partition.PerAttemptTimeoutMS,
		TotalNodeTimeoutMS:  partition.TotalNodeTimeoutMS,
		NodeBudgetMS:        partition.NodeBudgetMS, RetryAllowanceMS: partition.RetryAllowanceMS,
		LeaseMaximumMS: request.PlanInput.Lease.MaximumMS,
		HardWallMS:     request.Authority.HardWallMS, IsRetry: attempt.AttemptNumber > 1,
	}
}

func evaluateSoakCanaryLimits(input SoakCanaryLimitInput) []string {
	conflicts := map[string]bool{}
	if input.ChildElapsedMS > input.PerAttemptTimeoutMS {
		conflicts["per_attempt_timeout_exceeded"] = true
	}
	if input.NodeElapsedMS > input.TotalNodeTimeoutMS {
		conflicts["total_node_timeout_exceeded"] = true
	}
	if input.NodeElapsedMS > input.NodeBudgetMS {
		conflicts["node_budget_exceeded"] = true
	}
	if input.IsRetry && input.TotalAttemptElapsedMS > input.RetryAllowanceMS {
		conflicts["retry_allowance_exceeded"] = true
	}
	if input.AggregateLimitMS > 0 && input.AggregateElapsedMS > input.AggregateLimitMS {
		conflicts["aggregate_duration_exceeded"] = true
	}
	if input.PhaseElapsedMS > input.LeaseMaximumMS {
		conflicts["lease_maximum_exceeded"] = true
	}
	if input.PhaseElapsedMS > input.HardWallMS {
		conflicts["hard_wall_reached"] = true
	}
	return sortedSoakCanaryKeys(conflicts)
}

func soakCanaryPrimaryLimitConflict(conflicts []string) string {
	for _, code := range []string{
		"hard_wall_reached", "lease_maximum_exceeded", "per_attempt_timeout_exceeded",
		"retry_allowance_exceeded", "total_node_timeout_exceeded", "node_budget_exceeded",
		"aggregate_duration_exceeded",
	} {
		if soakCanaryStringPresent(conflicts, code) {
			return code
		}
	}
	return conflicts[0]
}

func soakCanaryExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	return -1
}

func soakCanaryProcessSignal(processState string) string {
	if strings.Contains(strings.ToLower(processState), "signal") {
		return processState
	}
	return ""
}

func soakCanaryNodeAttemptCount(attempts []SoakCanaryAttempt, nodeID string) int {
	count := 0
	for _, attempt := range attempts {
		if attempt.NodeID == nodeID {
			count++
		}
	}
	return count
}

func soakCanaryStringPresent(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func safeSoakCanaryFilename(value string) string {
	var builder strings.Builder
	for _, character := range value {
		switch {
		case character >= 'a' && character <= 'z':
			builder.WriteRune(character)
		case character >= 'A' && character <= 'Z':
			builder.WriteRune(character)
		case character >= '0' && character <= '9':
			builder.WriteRune(character)
		case character == '-', character == '_':
			builder.WriteRune(character)
		default:
			builder.WriteByte('_')
		}
	}
	return builder.String()
}
