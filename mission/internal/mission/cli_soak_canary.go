package mission

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

type soakCanaryCLIDependencies struct {
	provenanceProvider SoakCanarySourceProvenanceProvider
	snapshotter        SoakCanaryRepositorySnapshotter
	gitVerifier        SoakCanaryGitVerifier
	executor           SoakCanaryExecutor
	clock              SoakCanaryClock
	persistCompletion  func(string, SoakCanarySummary) error
}

func defaultSoakCanaryCLIDependencies() soakCanaryCLIDependencies {
	return soakCanaryCLIDependencies{
		provenanceProvider: BuildInfoSoakCanarySourceProvenanceProvider{},
		snapshotter:        PureGoSoakCanaryRepositorySnapshotter{},
		gitVerifier:        InProcessSoakCanaryGitVerifier{},
		executor:           OSExecSoakCanaryExecutor{},
		persistCompletion:  PersistSoakCanaryCompletion,
	}
}

func runSoakCanaryCLI(args []string, stdout io.Writer) error {
	return runSoakCanaryCLIWithDependencies(args, stdout, defaultSoakCanaryCLIDependencies())
}

func runSoakCanaryCLIWithDependencies(
	args []string,
	stdout io.Writer,
	dependencies soakCanaryCLIDependencies,
) error {
	fs := flag.NewFlagSet("qualification soak-canary", flag.ContinueOnError)
	planPath := fs.String("plan", "", "")
	authorityPath := fs.String("authority", "", "")
	catalogPath := fs.String("catalog", "", "")
	activationPath := fs.String("activation", "", "")
	checkpointPath := fs.String("checkpoint", "", "")
	evidenceRoot := fs.String("evidence-root", "", "")
	repositoryRoot := fs.String("repository-root", "", "")
	validateOnly := fs.Bool("validate-only", false, "")
	jsonOut := fs.Bool("json", false, "json output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || soakCanaryCLIValueMissing(
		*planPath, *authorityPath, *catalogPath, *activationPath,
		*checkpointPath, *evidenceRoot, *repositoryRoot,
	) {
		return errors.New("qualification soak-canary requires --plan, --authority, --catalog, --activation, --checkpoint, --evidence-root, and --repository-root")
	}
	rootAbs, err := filepath.Abs(*repositoryRoot)
	if err != nil {
		return err
	}
	if dependencies.provenanceProvider == nil ||
		dependencies.snapshotter == nil ||
		dependencies.gitVerifier == nil {
		return errors.New("qualification soak-canary source provenance dependencies are required")
	}
	provenance, err := dependencies.provenanceProvider.SourceProvenance()
	if err != nil {
		return err
	}
	repositorySnapshot, err := dependencies.snapshotter.Snapshot(rootAbs)
	if err != nil {
		return err
	}
	planBody, err := readBoundedRegularFile(*planPath, soakCanaryMaxInputBytes)
	if err != nil {
		return err
	}
	input, err := decodeSoakCanaryJSON[SoakPlanInput](planBody)
	if err != nil {
		return err
	}
	plan, err := BuildSoakPlan(input)
	if err != nil {
		return err
	}
	authority, err := LoadSoakCanaryAuthority(*authorityPath)
	if err != nil {
		return err
	}
	catalog, err := LoadSoakCanaryCommandCatalog(*catalogPath)
	if err != nil {
		return err
	}
	activation, err := LoadSoakCanaryActivation(*activationPath)
	if err != nil {
		return err
	}
	request := SoakCanaryRunRequest{
		PlanInput: input, PlanReadback: plan, PlanFixtureSHA256: digestBytes(planBody),
		Authority: authority, Catalog: catalog, Activation: activation,
		SourceProvenance: provenance, RepositorySnapshot: repositorySnapshot,
		Snapshotter: dependencies.snapshotter, GitVerifier: dependencies.gitVerifier,
		RepositoryRoot: rootAbs,
		EvidenceRoot:   *evidenceRoot, CheckpointPath: *checkpointPath,
		OutputLimitBytes: soakCanaryDefaultOutputBytes,
	}
	validation := ValidateSoakCanaryActivation(request)
	if *validateOnly {
		if *jsonOut {
			if err := printJSON(stdout, validation); err != nil {
				return err
			}
		} else {
			fmt.Fprintf(stdout, "activation_allowed=%t\nplanner_activation_eligible=%t\ncanary_execution_authorized=%t\nchild_process_launches=0\nconflicts=%s\nnext=%s\n",
				validation.ActivationAllowed,
				validation.PlannerActivationEligible,
				validation.CanaryExecutionAuthorized,
				strings.Join(validation.ConflictCodes, ","),
				validation.ExactNextAction,
			)
		}
		if !validation.ActivationAllowed {
			return fmt.Errorf("soak canary activation rejected: %s", strings.Join(validation.ConflictCodes, ","))
		}
		return nil
	}
	if dependencies.executor == nil || dependencies.persistCompletion == nil {
		return errors.New("qualification soak-canary execution dependencies are required")
	}
	request.Executor = dependencies.executor
	request.Clock = dependencies.clock
	summary, runErr := RunSoakCanary(context.Background(), request)
	if runErr != nil {
		if *jsonOut {
			if printErr := printJSON(stdout, summary); printErr != nil {
				return printErr
			}
		}
		return runErr
	}
	if err := dependencies.persistCompletion(request.EvidenceRoot, summary); err != nil {
		provisional := provisionalSoakCanarySummary(summary)
		if *jsonOut {
			if printErr := printJSON(stdout, provisional); printErr != nil {
				return printErr
			}
		}
		return err
	}
	if *jsonOut {
		return printJSON(stdout, summary)
	}
	fmt.Fprintf(stdout, "status=%s\nnodes=%d/%d\nattempts=%d\nchild_process_launches=%d\nscale_launches=%d\ncontrolled_retries=%d\nlocal_test_execution_performed=%t\nsummary_digest=%s\n",
		summary.Status, summary.CompletedNodes, summary.PlannedNodes,
		summary.TotalAttempts, summary.ChildProcessLaunches,
		summary.ScaleLaunches, summary.ControlledRetryCount,
		summary.LocalTestExecutionPerformed, summary.SummaryDigest,
	)
	return nil
}

func provisionalSoakCanarySummary(summary SoakCanarySummary) SoakCanarySummary {
	provisional := summary
	provisional.Status = "terminal_reconciliation_pending"
	provisional.TerminalIndexDigest = ""
	signSoakCanarySummary(&provisional)
	return provisional
}

func soakCanaryCLIValueMissing(values ...string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return true
		}
	}
	return false
}
