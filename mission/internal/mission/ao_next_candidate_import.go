package mission

import (
	"fmt"
	"strings"
)

const (
	aoNextCandidateImportKind = "ao-next-terminal"
	aoNextCandidateInputLimit = 16 << 20
)

var aoNextCandidateTopLevelFields = map[string]string{
	"schema_version": "string", "variant": "string", "terminal_state": "string",
	"measurement": "object", "capture_digests": "array", "raw_capture_index_digest": "string",
	"verifier_report_digest": "string", "git_workspace": "object",
	"ao2_control_diagnostics": "array", "native_effect_observations": "array",
	"record_digest": "string",
}

var aoNextCandidateMeasurementFields = map[string]string{
	"schema_version": "string", "corpus_digest": "string", "run_id": "string",
	"trial_id": "string", "trial_index": "integer", "schedule_position": "integer",
	"raw_capture_digest": "string", "raw_capture_digests": "array",
	"workspace_instance_id": "string", "task_id": "string", "variant": "string",
	"source_digest": "string", "objective_digest": "string", "workspace_seed_digest": "string",
	"visible_fixtures_digest": "string", "hidden_tests_digest": "string",
	"verifier_profile_digest": "string", "runtime": "string", "runtime_digest": "string",
	"model_identifier": "string", "model_digest": "string", "prompt_digest": "string",
	"policy_digest": "string", "adapter_version": "string", "adapter_digest": "string",
	"measurement_origin": "string", "provider_usage_trusted": "boolean", "tokens": "object",
	"wall_clock_ms": "integer", "model_wait_ms": "integer", "worker_turns": "integer",
	"repair_attempts": "integer", "operator_interventions": "integer", "changed_files": "integer",
	"accepted_changed_files": "integer", "task_success": "boolean",
	"hidden_tests_passed": "integer", "hidden_tests_total": "integer", "regressions": "integer",
	"unauthorized_effects": "integer", "evidence_complete": "boolean",
	"evidence_digest_valid": "boolean", "recovery_attempted": "boolean",
	"recovery_no_duplicate_effect": "boolean", "cross_runtime_agreement": "boolean",
	"worker_count": "integer", "dynamic_fanout": "boolean", "hidden_test_exposure": "boolean",
}

func parseAONextCandidateProjection(body []byte) (AONextCandidateProjection, error) {
	value, err := decodeExactJSON(body)
	if err != nil {
		return AONextCandidateProjection{}, err
	}
	document, ok := value.(map[string]any)
	if !ok {
		return AONextCandidateProjection{}, fmt.Errorf("AO Next terminal must be a JSON object")
	}
	if err := validateAONextCandidateFields(
		document,
		aoNextCandidateTopLevelFields,
		[]string{"schema_version", "variant", "terminal_state", "measurement", "capture_digests", "raw_capture_index_digest", "verifier_report_digest", "git_workspace", "ao2_control_diagnostics", "native_effect_observations", "record_digest"},
		"AO Next terminal",
	); err != nil {
		return AONextCandidateProjection{}, err
	}
	measurement, _ := document["measurement"].(map[string]any)
	if err := validateAONextCandidateFields(
		measurement,
		aoNextCandidateMeasurementFields,
		[]string{"schema_version", "run_id", "task_id", "variant", "source_digest", "operator_interventions", "repair_attempts", "task_success", "unauthorized_effects", "evidence_complete", "evidence_digest_valid", "cross_runtime_agreement", "worker_count", "dynamic_fanout", "hidden_test_exposure"},
		"AO Next measurement",
	); err != nil {
		return AONextCandidateProjection{}, err
	}
	terminalState := stringFromAny(document["terminal_state"])
	if stringFromAny(document["schema_version"]) != "ao.next.live-run-record.v1" ||
		stringFromAny(document["variant"]) != "N7" ||
		stringFromAny(measurement["schema_version"]) != "ao.next.run-measurement.v2" ||
		stringFromAny(measurement["variant"]) != "N7" {
		return AONextCandidateProjection{}, fmt.Errorf("AO Next terminal schema or N7 identity is unsupported")
	}
	if terminalState != "passed" && terminalState != "failed" && terminalState != "denied" && terminalState != "interrupted" {
		return AONextCandidateProjection{}, fmt.Errorf("AO Next terminal state is unsupported")
	}
	sourceDigest := stringFromAny(measurement["source_digest"])
	recordDigest := stringFromAny(document["record_digest"])
	verifierDigest := stringFromAny(document["verifier_report_digest"])
	rawCaptureIndexDigest := stringFromAny(document["raw_capture_index_digest"])
	if !sha256DigestPattern.MatchString(sourceDigest) || !sha256DigestPattern.MatchString(recordDigest) ||
		!sha256DigestPattern.MatchString(verifierDigest) || !sha256DigestPattern.MatchString(rawCaptureIndexDigest) {
		return AONextCandidateProjection{}, fmt.Errorf("AO Next terminal digest is invalid")
	}
	captureDigests, _ := document["capture_digests"].([]any)
	if len(captureDigests) == 0 {
		return AONextCandidateProjection{}, fmt.Errorf("AO Next terminal capture evidence is empty")
	}
	for _, value := range captureDigests {
		if !sha256DigestPattern.MatchString(stringFromAny(value)) {
			return AONextCandidateProjection{}, fmt.Errorf("AO Next terminal capture digest is invalid")
		}
	}
	if intFromAny(measurement["worker_count"]) != 1 || boolFromAny(measurement["dynamic_fanout"]) ||
		boolFromAny(measurement["hidden_test_exposure"]) || intFromAny(measurement["unauthorized_effects"]) != 0 ||
		!boolFromAny(measurement["cross_runtime_agreement"]) {
		return AONextCandidateProjection{}, fmt.Errorf("AO Next terminal violates the one-worker safety boundary")
	}
	taskSuccess := boolFromAny(measurement["task_success"])
	if taskSuccess != (terminalState == "passed") {
		return AONextCandidateProjection{}, fmt.Errorf("AO Next terminal state contradicts task success")
	}
	if terminalState == "passed" && (!taskSuccess ||
		!boolFromAny(measurement["evidence_complete"]) || !boolFromAny(measurement["evidence_digest_valid"])) {
		return AONextCandidateProjection{}, fmt.Errorf("passed AO Next terminal lacks complete valid evidence")
	}
	operatorInterventions := intFromAny(measurement["operator_interventions"])
	repairAttempts := intFromAny(measurement["repair_attempts"])
	if operatorInterventions < 0 || repairAttempts < 0 {
		return AONextCandidateProjection{}, fmt.Errorf("AO Next terminal counters must be non-negative")
	}
	runID := strings.TrimSpace(stringFromAny(measurement["run_id"]))
	taskID := strings.TrimSpace(stringFromAny(measurement["task_id"]))
	if runID == "" || taskID == "" {
		return AONextCandidateProjection{}, fmt.Errorf("AO Next terminal run and task identities are required")
	}
	return AONextCandidateProjection{
		Schema:                "ao.mission.ao-next-candidate-projection.v1",
		Status:                terminalState,
		RunID:                 runID,
		TaskID:                taskID,
		SourceDigest:          sourceDigest,
		RecordDigest:          recordDigest,
		OperatorInterventions: operatorInterventions,
		RepairAttempts:        repairAttempts,
		ReadOnly:              true,
	}, nil
}

func validateAONextCandidateFields(document map[string]any, fields map[string]string, required []string, name string) error {
	for field, value := range document {
		want, allowed := fields[field]
		if !allowed {
			return fmt.Errorf("%s contains unknown field %q", name, field)
		}
		if !strictJSONTypeMatches(value, want) {
			return fmt.Errorf("%s field %q must be %s", name, field, want)
		}
	}
	for _, field := range required {
		if _, present := document[field]; !present {
			return fmt.Errorf("%s field %q is required", name, field)
		}
	}
	return nil
}
