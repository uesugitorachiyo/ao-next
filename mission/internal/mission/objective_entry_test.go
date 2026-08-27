package mission

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCLIObjectiveStartGeneratesAndPersistsCorrelation(t *testing.T) {
	home := t.TempDir()
	var out, errOut bytes.Buffer
	code := Run([]string{
		"--home", home,
		"objective", "start",
		"--objective", "Update the README typo in the command examples",
	}, &out, &errOut)
	if code != 0 {
		t.Fatalf("objective start failed: %s", errOut.String())
	}

	var contract map[string]any
	if err := json.Unmarshal(out.Bytes(), &contract); err != nil {
		t.Fatal(err)
	}
	if contract["schema"] != "ao.mission.objective-workflow-contract.v0.1" {
		t.Fatalf("unexpected contract schema: %#v", contract["schema"])
	}
	if contract["status"] != "ready" || contract["routing_class"] != "reduced" ||
		contract["acceptance_status"] != "accepted" || contract["initial_route"] != "ao-foundry" {
		t.Fatalf("unexpected reduced workflow contract: %#v", contract)
	}
	missionID, _ := contract["mission_id"].(string)
	correlationID, _ := contract["correlation_id"].(string)
	if missionID == "" || correlationID != "corr-"+strings.TrimPrefix(missionID, "mission-") {
		t.Fatalf("unstable generated correlation: mission=%q correlation=%q", missionID, correlationID)
	}
	if contract["safe_to_execute"] != false || contract["executes_work"] != false ||
		contract["approves_work"] != false || contract["mutates_repositories"] != false {
		t.Fatalf("workflow contract widened authority: %#v", contract)
	}
	commands, ok := contract["lifecycle_commands"].([]any)
	if !ok || len(commands) < 5 {
		t.Fatalf("workflow contract lacks lifecycle commands: %#v", contract["lifecycle_commands"])
	}
	assertObjectiveStageStatus(t, contract, "ao-blueprint", "omitted")
	assertObjectiveStageStatus(t, contract, "ao-atlas", "omitted")
	assertObjectiveStageStatus(t, contract, "ao-foundry", "required")
	assertObjectiveStageStatus(t, contract, "ao-mission-reconciliation", "required")

	record, err := NewStore(home).Load(missionID)
	if err != nil {
		t.Fatal(err)
	}
	if record.MissionID != missionID {
		t.Fatalf("persisted wrong mission: %+v", record)
	}
	body, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	var persisted map[string]any
	if err := json.Unmarshal(body, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted["correlation_id"] != correlationID {
		t.Fatalf("persisted correlation mismatch: %#v", persisted)
	}
	persistedContract, ok := persisted["workflow_contract"].(map[string]any)
	if !ok || !reflect.DeepEqual(persistedContract, contract) {
		t.Fatalf("persisted workflow contract differs from command output:\noutput=%#v\npersisted=%#v", contract, persistedContract)
	}
}

func TestCLIObjectiveStartPreservesProvidedCorrelation(t *testing.T) {
	home := t.TempDir()
	var out, errOut bytes.Buffer
	code := Run([]string{
		"--home", home,
		"objective", "start",
		"--objective", "Implement the bounded multi-file reference objective workgraph",
		"--correlation-id", "corr-month3.reference:001",
	}, &out, &errOut)
	if code != 0 {
		t.Fatalf("objective start failed: %s", errOut.String())
	}
	var contract map[string]any
	if err := json.Unmarshal(out.Bytes(), &contract); err != nil {
		t.Fatal(err)
	}
	if contract["correlation_id"] != "corr-month3.reference:001" ||
		contract["routing_class"] != "complex" ||
		contract["initial_route"] != "ao-atlas" {
		t.Fatalf("provided correlation or complex route changed: %#v", contract)
	}
	assertObjectiveStageStatus(t, contract, "ao-blueprint", "omitted")
	assertObjectiveStageStatus(t, contract, "ao-atlas", "required")
	assertObjectiveStageStatus(t, contract, "ao-foundry", "conditional")
}

func TestObjectiveWorkflowRoutesPendingBlueprint(t *testing.T) {
	home := t.TempDir()
	var out, errOut bytes.Buffer
	code := Run([]string{
		"--home", home,
		"objective", "start",
		"--objective", "Figure it out",
	}, &out, &errOut)
	if code != 0 {
		t.Fatalf("objective start failed: %s", errOut.String())
	}
	var contract map[string]any
	if err := json.Unmarshal(out.Bytes(), &contract); err != nil {
		t.Fatal(err)
	}
	if contract["routing_class"] != "pending_blueprint" ||
		contract["acceptance_status"] != "pending_blueprint" ||
		contract["initial_route"] != "ao-blueprint" {
		t.Fatalf("underspecified objective did not fail closed to Blueprint: %#v", contract)
	}
	assertObjectiveStageStatus(t, contract, "ao-blueprint", "required")
	assertObjectiveStageStatus(t, contract, "ao-atlas", "conditional")
}

func TestObjectiveStartRejectsInvalidCorrelationAndUnexpectedArguments(t *testing.T) {
	for name, args := range map[string][]string{
		"invalid correlation": {
			"objective", "start", "--objective", "Update one documentation example",
			"--correlation-id", "bad correlation",
		},
		"unexpected argument": {
			"objective", "start", "--objective", "Update one documentation example", "extra",
		},
	} {
		t.Run(name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			if code := Run(args, &out, &errOut); code == 0 {
				t.Fatalf("unsafe objective start input accepted: %s", out.String())
			}
		})
	}
}

func TestLegacyStartOutputRemainsCompatible(t *testing.T) {
	home := t.TempDir()
	var out, errOut bytes.Buffer
	if code := Run([]string{"--home", home, "start", "small objective"}, &out, &errOut); code != 0 {
		t.Fatalf("legacy start failed: %s", errOut.String())
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if _, exists := got["correlation_id"]; exists {
		t.Fatalf("legacy start unexpectedly gained correlation_id: %#v", got)
	}
	if _, exists := got["workflow_contract"]; exists {
		t.Fatalf("legacy start unexpectedly gained workflow_contract: %#v", got)
	}
	if got["current_route"] != "ao-blueprint" {
		t.Fatalf("legacy routing changed: %#v", got)
	}
}

func TestObjectiveCorrelationSurvivesCoreLifecycleAndEvidence(t *testing.T) {
	home := t.TempDir()
	store := NewStore(home)
	contract, err := store.StartObjective(
		"Implement one bounded multi-file reference objective",
		ObjectiveStartOptions{CorrelationID: "corr-reference-001"},
	)
	if err != nil {
		t.Fatal(err)
	}
	continued, err := Continue(store, contract.MissionID, ContinueOptions{MaxIterations: 1})
	if err != nil {
		t.Fatal(err)
	}
	if continued.CorrelationID != contract.CorrelationID || len(continued.Steps) != 1 ||
		continued.Steps[0].CorrelationID != contract.CorrelationID ||
		len(continued.Checkpoints) != 1 ||
		continued.Checkpoints[0].CorrelationID != contract.CorrelationID {
		t.Fatalf("continue lost correlation: %+v", continued)
	}
	decision, err := store.LoadEventLoopDecision(contract.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	if decision.CorrelationID != contract.CorrelationID {
		t.Fatalf("event-loop decision lost correlation: %+v", decision)
	}
	checkpointBundle, err := store.LoadCheckpointBundle(contract.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	if checkpointBundle.CorrelationID != contract.CorrelationID ||
		checkpointBundle.LatestCheckpoint == nil ||
		checkpointBundle.LatestCheckpoint.CorrelationID != contract.CorrelationID {
		t.Fatalf("checkpoint bundle lost correlation: %+v", checkpointBundle)
	}

	paused, err := Pause(store, contract.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := Resume(store, contract.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	if paused.CorrelationID != contract.CorrelationID || resumed.CorrelationID != contract.CorrelationID {
		t.Fatalf("pause/resume changed correlation: paused=%+v resumed=%+v", paused, resumed)
	}
	if resumed.ExactNextAction != contract.ExactNextAction {
		t.Fatalf("resume did not restore workflow action: got %q want %q", resumed.ExactNextAction, contract.ExactNextAction)
	}
}

func TestCorrelatedImportRequiresExactCorrelationWithoutMutationOnReject(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "home"))
	contract, err := store.StartObjective(
		"Implement one bounded multi-file reference objective",
		ObjectiveStartOptions{CorrelationID: "corr-reference-002"},
	)
	if err != nil {
		t.Fatal(err)
	}
	for name, correlation := range map[string]string{
		"missing":  "",
		"mismatch": "corr-other",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(dir, name+".json")
			doc := map[string]any{
				"schema":  "ao.blueprint.build-authorization.v0.1",
				"status":  "ready",
				"project": "reference",
			}
			if correlation != "" {
				doc["correlation_id"] = correlation
			}
			writeJSONForTest(t, path, doc)
			if _, err := ImportArtifact(store, contract.MissionID, "blueprint-authorization", path); err == nil {
				t.Fatalf("%s correlated import accepted", name)
			}
			record, err := store.Load(contract.MissionID)
			if err != nil {
				t.Fatal(err)
			}
			if len(record.ArtifactRefs) != 0 || record.CurrentRoute != contract.InitialRoute {
				t.Fatalf("rejected import mutated mission: %+v", record)
			}
		})
	}

	validPath := filepath.Join(dir, "valid.json")
	writeJSONForTest(t, validPath, map[string]any{
		"schema":         "ao.blueprint.build-authorization.v0.1",
		"status":         "ready",
		"project":        "reference",
		"correlation_id": contract.CorrelationID,
	})
	readback, err := ImportArtifact(store, contract.MissionID, "blueprint-authorization", validPath)
	if err != nil {
		t.Fatal(err)
	}
	if readback.CorrelationID != contract.CorrelationID {
		t.Fatalf("import readback lost correlation: %+v", readback)
	}
}

func TestObjectiveCorrelationAppearsInDashboardAndVerificationBundle(t *testing.T) {
	home := t.TempDir()
	store := NewStore(home)
	contract, err := store.StartObjective(
		"Update the README typo in the command examples",
		ObjectiveStartOptions{CorrelationID: "corr-reference-003"},
	)
	if err != nil {
		t.Fatal(err)
	}
	dashboard, err := BuildMissionDashboardReadback(store, contract.MissionID, true)
	if err != nil {
		t.Fatal(err)
	}
	if dashboard.CorrelationID != contract.CorrelationID || len(dashboard.RecentEvents) == 0 ||
		dashboard.RecentEvents[0].CorrelationID != contract.CorrelationID {
		t.Fatalf("dashboard evidence lost correlation: %+v", dashboard)
	}
	bundle, err := BuildMissionVerificationBundleReadback(store, contract.MissionID, MissionVerificationBundleOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if bundle.CorrelationID != contract.CorrelationID {
		t.Fatalf("verification bundle lost correlation: %+v", bundle)
	}
	foundWorkflow := false
	for _, component := range bundle.Components {
		if component.Name == "workflow_contract" {
			foundWorkflow = component.Schema == ObjectiveWorkflowSchema &&
				component.Status == "ready" &&
				strings.HasPrefix(component.SHA256, "sha256:")
		}
	}
	if !foundWorkflow {
		t.Fatalf("verification bundle lacks workflow contract component: %+v", bundle)
	}
}

func TestObjectiveNextAndTerminalReadbacksPreserveWorkflowCorrelation(t *testing.T) {
	home := t.TempDir()
	store := NewStore(home)
	contract, err := store.StartObjective(
		"Update the README typo in the command examples",
		ObjectiveStartOptions{CorrelationID: "corr-terminal-001"},
	)
	if err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	if code := Run([]string{"--home", home, "next", "--mission", contract.MissionID, "--json"}, &out, &errOut); code != 0 {
		t.Fatalf("next failed: %s", errOut.String())
	}
	var decision RouteDecision
	if err := json.Unmarshal(out.Bytes(), &decision); err != nil {
		t.Fatal(err)
	}
	if decision.Route != contract.InitialRoute || decision.ExactNextAction != contract.ExactNextAction {
		t.Fatalf("next contradicted persisted workflow: %+v", decision)
	}

	record, err := store.Load(contract.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	if got := BuildCommandStatus(record).CorrelationID; got != contract.CorrelationID {
		t.Fatalf("command status correlation=%q", got)
	}
	if got := BuildRouteReconciliation(record).CorrelationID; got != contract.CorrelationID {
		t.Fatalf("route reconciliation correlation=%q", got)
	}
	if got := BuildFinalReconciliationPacket(record).CorrelationID; got != contract.CorrelationID {
		t.Fatalf("final reconciliation correlation=%q", got)
	}
	if got := BuildFinalRollup(record).CorrelationID; got != contract.CorrelationID {
		t.Fatalf("final rollup correlation=%q", got)
	}
	if got := Snapshot(record).CorrelationID; got != contract.CorrelationID {
		t.Fatalf("governance snapshot correlation=%q", got)
	}
}

func TestCorrelatedArchiveRejectsMismatchedOrAuthorityWideningWorkflowContract(t *testing.T) {
	store := NewStore(t.TempDir())
	contract, err := store.StartObjective(
		"Implement one bounded multi-file reference objective",
		ObjectiveStartOptions{CorrelationID: "corr-archive-001"},
	)
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Load(contract.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := BuildMissionArchive(record)
	if err != nil {
		t.Fatal(err)
	}

	for name, mutate := range map[string]func(*MissionArchive){
		"foreign correlation": func(archive *MissionArchive) {
			archive.Record.WorkflowContract.CorrelationID = "corr-foreign"
		},
		"foreign mission": func(archive *MissionArchive) {
			archive.Record.WorkflowContract.MissionID = "mission-foreign"
		},
		"foreign objective": func(archive *MissionArchive) {
			archive.Record.WorkflowContract.ObjectiveDigest = "sha256:" + strings.Repeat("a", 64)
		},
		"authority widening": func(archive *MissionArchive) {
			archive.Record.WorkflowContract.ApprovesWork = true
		},
		"unknown routing class": func(archive *MissionArchive) {
			archive.Record.WorkflowContract.RoutingClass = "unknown"
		},
		"missing stages": func(archive *MissionArchive) {
			archive.Record.WorkflowContract.Stages = nil
		},
		"missing lifecycle commands": func(archive *MissionArchive) {
			archive.Record.WorkflowContract.LifecycleCommands = nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			tampered := archive
			contractCopy := *archive.Record.WorkflowContract
			tampered.Record.WorkflowContract = &contractCopy
			mutate(&tampered)
			tampered.ArchiveDigest = ""
			body, err := json.Marshal(tampered)
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(body)
			tampered.ArchiveDigest = "sha256:" + hex.EncodeToString(sum[:])
			path := filepath.Join(t.TempDir(), "archive.json")
			writeJSONForTest(t, path, tampered)

			importStore := NewStore(t.TempDir())
			if _, err := ImportMissionArchive(importStore, path); err == nil {
				t.Fatal("internally inconsistent workflow contract was imported")
			}
			if _, err := importStore.Load(record.MissionID); !os.IsNotExist(err) {
				t.Fatalf("rejected archive mutated destination store: %v", err)
			}
		})
	}

	body, err := json.Marshal(archive)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	recordDoc := raw["record"].(map[string]any)
	workflowDoc := recordDoc["workflow_contract"].(map[string]any)
	workflowDoc["unexpected"] = true
	path := filepath.Join(t.TempDir(), "unknown-nested-field.json")
	writeJSONForTest(t, path, raw)
	if _, err := ImportMissionArchive(NewStore(t.TempDir()), path); err == nil {
		t.Fatal("archive accepted an unknown nested workflow contract field")
	}
}

func TestCorrelatedPublicSafeRedactedArchiveImportsWithoutIdentityLoss(t *testing.T) {
	exportStore := NewStore(t.TempDir())
	contract, err := exportStore.StartObjective(
		"Update /Users/example/private/repository in the bounded documentation fixture",
		ObjectiveStartOptions{CorrelationID: "corr-redacted-archive-001"},
	)
	if err != nil {
		t.Fatal(err)
	}
	record, err := exportStore.Load(contract.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := BuildMissionArchive(record)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(archive.Record.Objective, "<local-path-redacted>") {
		t.Fatalf("archive objective was not redacted: %q", archive.Record.Objective)
	}
	if archive.Record.ObjectiveDigest != record.ObjectiveDigest ||
		archive.Record.WorkflowContract.ObjectiveDigest != record.ObjectiveDigest {
		t.Fatalf("archive lost stable objective identity: %+v", archive.Record)
	}
	path := filepath.Join(t.TempDir(), "archive.json")
	writeJSONForTest(t, path, archive)
	importStore := NewStore(t.TempDir())
	if _, err := ImportMissionArchive(importStore, path); err != nil {
		t.Fatalf("redacted archive import failed: %v", err)
	}
	imported, err := importStore.Load(record.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	if imported.CorrelationID != record.CorrelationID ||
		imported.WorkflowContract == nil ||
		imported.WorkflowContract.ObjectiveDigest != record.ObjectiveDigest {
		t.Fatalf("redacted archive import lost identity: %+v", imported)
	}

	rearchive, err := BuildMissionArchive(imported)
	if err != nil {
		t.Fatal(err)
	}
	if !rearchive.Record.ObjectiveRedacted {
		t.Fatal("re-archive lost persistent objective redaction state")
	}
	rearchivePath := filepath.Join(t.TempDir(), "rearchive.json")
	writeJSONForTest(t, rearchivePath, rearchive)
	if _, err := ImportMissionArchive(NewStore(t.TempDir()), rearchivePath); err != nil {
		t.Fatalf("second-generation redacted archive import failed: %v", err)
	}
}

func TestObjectiveWorkflowContractFixtureValidatesStrictly(t *testing.T) {
	fixture := filepath.Join("..", "..", "examples", "valid", "objective-workflow-contract.json")
	if _, err := ValidateContractFile(fixture); err != nil {
		t.Fatalf("valid workflow contract fixture rejected: %v", err)
	}
	body, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	var contract map[string]any
	if err := json.Unmarshal(body, &contract); err != nil {
		t.Fatal(err)
	}
	contract["unexpected"] = true
	unknownPath := filepath.Join(t.TempDir(), "unknown.json")
	writeJSONForTest(t, unknownPath, contract)
	if _, err := ValidateContractFile(unknownPath); err == nil {
		t.Fatal("strict workflow contract accepted an unknown field")
	}

	delete(contract, "unexpected")
	contract["initial_route"] = "ao-foundry"
	mismatchPath := filepath.Join(t.TempDir(), "routing-mismatch.json")
	writeJSONForTest(t, mismatchPath, contract)
	if _, err := ValidateContractFile(mismatchPath); err == nil {
		t.Fatal("strict workflow contract accepted a routing-class mismatch")
	}
}

func assertObjectiveStageStatus(t *testing.T, contract map[string]any, name, status string) {
	t.Helper()
	stages, ok := contract["stages"].([]any)
	if !ok {
		t.Fatalf("stages are missing: %#v", contract["stages"])
	}
	for _, raw := range stages {
		stage, ok := raw.(map[string]any)
		if ok && stage["name"] == name {
			if stage["status"] != status {
				t.Fatalf("stage %s status=%#v, want %s", name, stage["status"], status)
			}
			if strings.TrimSpace(stage["reason"].(string)) == "" {
				t.Fatalf("stage %s lacks reason", name)
			}
			return
		}
	}
	t.Fatalf("stage %s not found in %#v", name, stages)
}
