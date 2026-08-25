package mission

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	SoakPlanInputSchema       = "ao.mission.soak-plan-input.v1"
	SoakPlanReadbackSchema    = "ao.mission.soak-plan-readback.v1"
	soakPlanMaxFixtureBytes   = 1 << 20
	soakPlanMinHistorySamples = 3
	soakPlanMaxHistorySamples = 64
	soakPlanMaxDurationMS     = int64(7 * 24 * 60 * 60 * 1000)
)

type SoakPlanInput struct {
	Schema                                string                 `json:"schema"`
	PlanID                                string                 `json:"plan_id"`
	MissionID                             string                 `json:"mission_id"`
	SourceHead                            string                 `json:"source_head"`
	ExecutionProfile                      SoakExecutionProfile   `json:"execution_profile"`
	ClassificationBoundBeforePartitioning bool                   `json:"classification_bound_before_partitioning"`
	TestCatalog                           []SoakTestEntry        `json:"test_catalog"`
	DurationHistory                       []SoakDurationHistory  `json:"duration_history"`
	Partitions                            []SoakPartitionRequest `json:"partitions"`
	PartitionBudgets                      []SoakPartitionBudget  `json:"partition_budgets"`
	Budgets                               SoakPlanBudgets        `json:"budgets"`
	RepeatPolicy                          SoakRepeatPolicy       `json:"repeat_policy"`
	RetryPolicy                           *SoakRetryPolicy       `json:"retry_policy"`
	TimeoutPolicy                         SoakTimeoutPolicy      `json:"timeout_policy"`
	Lease                                 SoakLeaseBudget        `json:"lease"`
	Activation                            SoakActivationBinding  `json:"activation"`
	PolicyDigest                          string                 `json:"policy_digest"`
	SafetyBoundaries                      SoakSafetyBoundaries   `json:"safety_boundaries"`
}

type SoakExecutionProfile struct {
	ID     string `json:"id"`
	Digest string `json:"digest"`
	Mode   string `json:"mode"`
	Race   bool   `json:"race"`
}

type SoakTestEntry struct {
	ID                   string              `json:"id"`
	Classification       string              `json:"classification"`
	RequestedRepeatCount int                 `json:"requested_repeat_count"`
	ScaleDimension       *SoakScaleDimension `json:"scale_dimension,omitempty"`
}

type SoakScaleDimension struct {
	Unit  string `json:"unit"`
	Value int64  `json:"value"`
}

type SoakDurationHistory struct {
	TestID                 string  `json:"test_id"`
	SourceHead             string  `json:"source_head"`
	ExecutionProfileDigest string  `json:"execution_profile_digest"`
	Unit                   string  `json:"unit"`
	Samples                []int64 `json:"samples"`
}

type SoakPartitionRequest struct {
	PartitionID string   `json:"partition_id"`
	NodeID      string   `json:"node_id"`
	TestIDs     []string `json:"test_ids"`
}

type SoakPartitionBudget struct {
	PartitionID  string `json:"partition_id"`
	NodeBudgetMS int64  `json:"node_budget_ms"`
}

type SoakPlanBudgets struct {
	MaximumTests      int   `json:"maximum_tests"`
	MaximumPartitions int   `json:"maximum_partitions"`
	SetupOverheadMS   int64 `json:"setup_overhead_ms"`
	SafetyOverheadMS  int64 `json:"safety_overhead_ms"`
}

type SoakRepeatPolicy struct {
	MaximumRegularRepeatCount int  `json:"maximum_regular_repeat_count"`
	ScaleRepeatCount          int  `json:"scale_repeat_count"`
	ApplyPartitionRepeatToAll bool `json:"apply_partition_repeat_to_all"`
}

type SoakRetryPolicy struct {
	MaximumAttempts               int      `json:"maximum_attempts"`
	MaximumTotalRetries           *int     `json:"maximum_total_retries,omitempty"`
	RetryableOutcomeClasses       []string `json:"retryable_outcome_classes"`
	NonRetryableOutcomeClasses    []string `json:"non_retryable_outcome_classes"`
	CheckpointBehavior            string   `json:"checkpoint_behavior"`
	PreservesNodeIdentity         bool     `json:"preserves_node_identity"`
	PreservesTestSet              bool     `json:"preserves_test_set"`
	PreservesScaleDimension       bool     `json:"preserves_scale_dimension"`
	PreservesRepeatCount          bool     `json:"preserves_repeat_count"`
	PreservesSourceHead           bool     `json:"preserves_source_head"`
	PreservesExecutionProfile     bool     `json:"preserves_execution_profile"`
	PreservesPhaseStart           bool     `json:"preserves_phase_start"`
	PreservesAuthorityBoundaries  bool     `json:"preserves_authority_boundaries"`
	PreservesSideEffectBoundaries bool     `json:"preserves_side_effect_boundaries"`
	PhaseClockResetProhibited     bool     `json:"phase_clock_reset_prohibited"`
	EvidenceRequiredAfterFailure  string   `json:"evidence_required_after_failure"`
}

type SoakTimeoutPolicy struct {
	PerAttemptTimeoutMS int64 `json:"per_attempt_timeout_ms"`
	TotalNodeTimeoutMS  int64 `json:"total_node_timeout_ms"`
}

type SoakLeaseBudget struct {
	MinimumMS int64 `json:"minimum_ms"`
	TargetMS  int64 `json:"target_ms"`
	MaximumMS int64 `json:"maximum_ms"`
}

type SoakActivationBinding struct {
	State                       string `json:"state"`
	PolicyBoundBeforeActivation bool   `json:"policy_bound_before_activation"`
	BoundPolicyDigest           string `json:"bound_policy_digest"`
}

type SoakSafetyBoundaries struct {
	SafeToExecute       bool `json:"safe_to_execute"`
	ExecutesWork        bool `json:"executes_work"`
	ApprovesWork        bool `json:"approves_work"`
	MutatesRepositories bool `json:"mutates_repositories"`
	CallsProviders      bool `json:"calls_providers"`
	Publishes           bool `json:"publishes"`
	Releases            bool `json:"releases"`
	Deploys             bool `json:"deploys"`
	AdvancesAuthority   bool `json:"advances_authority"`
	RSIRemainsDenied    bool `json:"rsi_remains_denied"`
}

type SoakPlanReadback struct {
	Schema                 string                    `json:"schema"`
	PlanID                 string                    `json:"plan_id"`
	MissionID              string                    `json:"mission_id"`
	SourceHead             string                    `json:"source_head"`
	ExecutionProfileDigest string                    `json:"execution_profile_digest"`
	InputDigest            string                    `json:"input_digest"`
	PolicyDigest           string                    `json:"policy_digest"`
	Classification         SoakClassificationSummary `json:"classification"`
	DurationHistory        SoakDurationSummary       `json:"duration_history"`
	Partitions             []SoakPlannedPartition    `json:"planned_partitions"`
	RetryPolicy            *SoakRetryPolicy          `json:"retry_policy"`
	TimeoutPolicy          SoakTimeoutPolicy         `json:"timeout_policy"`
	LeaseBudget            SoakLeaseSummary          `json:"lease_budget"`
	ActivationAllowed      bool                      `json:"activation_allowed"`
	ConflictCodes          []string                  `json:"conflict_codes"`
	ExactNextAction        string                    `json:"exact_next_action"`
	ReadOnly               bool                      `json:"read_only"`
	SafetyBoundaries       SoakSafetyBoundaries      `json:"safety_boundaries"`
}

type SoakClassificationSummary struct {
	BoundBeforePartitioning bool                   `json:"bound_before_partitioning"`
	RegularTests            []string               `json:"regular_tests"`
	ScaleTests              []SoakScaleTestSummary `json:"scale_tests"`
}

type SoakScaleTestSummary struct {
	TestID               string `json:"test_id"`
	WorkloadUnit         string `json:"workload_unit"`
	WorkloadValue        int64  `json:"workload_value"`
	ClassificationReason string `json:"classification_reason"`
}

type SoakDurationSummary struct {
	Unit        string `json:"unit"`
	Estimator   string `json:"estimator"`
	HistorySets int    `json:"history_sets"`
	SampleCount int    `json:"sample_count"`
}

type SoakPlannedPartition struct {
	PartitionID           string   `json:"partition_id"`
	NodeID                string   `json:"node_id"`
	Classification        string   `json:"classification"`
	Tests                 []string `json:"tests"`
	RequestedRepeatCount  int      `json:"requested_repeat_count"`
	EffectiveRepeatCount  int      `json:"effective_repeat_count"`
	AmplificationDecision string   `json:"amplification_decision"`
	EstimatedDurationMS   int64    `json:"estimated_duration_ms"`
	RetryAllowanceMS      int64    `json:"retry_allowance_ms"`
	NodeBudgetMS          int64    `json:"node_budget_ms"`
}

type SoakLeaseSummary struct {
	MinimumMS               int64 `json:"minimum_ms"`
	TargetMS                int64 `json:"target_ms"`
	MaximumMS               int64 `json:"maximum_ms"`
	TotalPlannedMS          int64 `json:"total_planned_ms"`
	TotalPlannedWithRetryMS int64 `json:"total_planned_with_retry_ms"`
	Fits                    bool  `json:"fits"`
}

type soakPolicyBinding struct {
	Budgets          SoakPlanBudgets       `json:"budgets"`
	PartitionBudgets []SoakPartitionBudget `json:"partition_budgets"`
	RepeatPolicy     SoakRepeatPolicy      `json:"repeat_policy"`
	RetryPolicy      *SoakRetryPolicy      `json:"retry_policy"`
	TimeoutPolicy    SoakTimeoutPolicy     `json:"timeout_policy"`
	Lease            SoakLeaseBudget       `json:"lease"`
	SafetyBoundaries SoakSafetyBoundaries  `json:"safety_boundaries"`
}

func LoadSoakPlanInput(root, fixturePath string) (SoakPlanInput, error) {
	var input SoakPlanInput
	if strings.TrimSpace(root) == "" {
		return input, errors.New("soak plan requires a fixture root")
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return input, err
	}
	pathAbs, err := filepath.Abs(fixturePath)
	if err != nil {
		return input, err
	}
	relative, err := filepath.Rel(rootAbs, pathAbs)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return input, errors.New("soak plan requires a safe relative fixture path within its root")
	}
	info, err := os.Lstat(pathAbs)
	if err != nil {
		return input, err
	}
	if !info.Mode().IsRegular() {
		return input, errors.New("soak plan fixture must be a regular non-symlink file")
	}
	if info.Size() > soakPlanMaxFixtureBytes {
		return input, errors.New("soak plan fixture exceeds size limit")
	}
	body, err := os.ReadFile(pathAbs)
	if err != nil {
		return input, err
	}
	if err := validateNoDuplicateJSONKeys(body); err != nil {
		if strings.Contains(err.Error(), "duplicate JSON key") {
			return input, err
		}
		return input, fmt.Errorf("invalid JSON: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return input, fmt.Errorf("invalid JSON: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return input, errors.New("soak plan fixture contains trailing JSON")
		}
		return input, fmt.Errorf("invalid JSON: %w", err)
	}
	return input, nil
}

func BuildSoakPlan(input SoakPlanInput) (SoakPlanReadback, error) {
	policyDigest := soakPolicyDigest(input)
	readback := SoakPlanReadback{
		Schema:                 SoakPlanReadbackSchema,
		PlanID:                 input.PlanID,
		MissionID:              input.MissionID,
		SourceHead:             input.SourceHead,
		ExecutionProfileDigest: input.ExecutionProfile.Digest,
		InputDigest:            canonicalSoakInputDigest(input),
		PolicyDigest:           policyDigest,
		RetryPolicy:            input.RetryPolicy,
		TimeoutPolicy:          input.TimeoutPolicy,
		ReadOnly:               true,
		SafetyBoundaries:       deniedSoakSafety(),
		ConflictCodes:          []string{},
	}
	conflicts := map[string]bool{}
	addConflict := func(code string) { conflicts[code] = true }

	validateSoakEnvelope(input, policyDigest, addConflict)
	catalog := classifySoakTests(input, &readback, addConflict)
	history := validateSoakHistory(input, &readback, addConflict)
	readback.Partitions = planSoakPartitions(input, catalog, history, addConflict)

	maxAttempts := 0
	if input.RetryPolicy != nil {
		maxAttempts = input.RetryPolicy.MaximumAttempts
	}
	var total, totalWithRetry int64
	for index := range readback.Partitions {
		partition := &readback.Partitions[index]
		total = checkedSoakAdd(total, partition.EstimatedDurationMS, addConflict)
		withRetry := checkedSoakMultiply(partition.EstimatedDurationMS, int64(maxAttempts), addConflict)
		partition.RetryAllowanceMS = withRetry
		totalWithRetry = checkedSoakAdd(totalWithRetry, withRetry, addConflict)
		if partition.EstimatedDurationMS > input.TimeoutPolicy.PerAttemptTimeoutMS {
			addConflict("timeout_below_estimate")
		}
		if withRetry > input.TimeoutPolicy.TotalNodeTimeoutMS {
			addConflict("retry_total_timeout_exceeded")
		}
		if partition.EstimatedDurationMS > partition.NodeBudgetMS {
			addConflict("partition_node_budget_exceeded")
		}
		if withRetry > partition.NodeBudgetMS {
			addConflict("retry_node_budget_exceeded")
		}
	}
	if input.RetryPolicy != nil && input.RetryPolicy.MaximumTotalRetries != nil {
		maximumTotalRetries := *input.RetryPolicy.MaximumTotalRetries
		retrySlotsPerPartition := 0
		if maxAttempts > 1 {
			retrySlotsPerPartition = maxAttempts - 1
		}
		maximumInt := int(^uint(0) >> 1)
		retrySlotCapacity := 0
		if len(readback.Partitions) > 0 && retrySlotsPerPartition > 0 {
			if retrySlotsPerPartition > maximumInt/len(readback.Partitions) {
				retrySlotCapacity = maximumInt
			} else {
				retrySlotCapacity = len(readback.Partitions) * retrySlotsPerPartition
			}
		}
		switch {
		case maximumTotalRetries < 0:
			addConflict("retry_budget_invalid")
		case maximumTotalRetries > retrySlotCapacity:
			addConflict("retry_budget_exceeds_attempt_capacity")
		default:
			retrySlotCosts := make([]int64, len(readback.Partitions))
			for index, partition := range readback.Partitions {
				retrySlotCosts[index] = partition.EstimatedDurationMS
			}
			sort.Slice(retrySlotCosts, func(i, j int) bool {
				return retrySlotCosts[i] > retrySlotCosts[j]
			})
			totalWithRetry = total
			remainingRetrySlots := maximumTotalRetries
			for _, retrySlotCost := range retrySlotCosts {
				retrySlots := retrySlotsPerPartition
				if retrySlots > remainingRetrySlots {
					retrySlots = remainingRetrySlots
				}
				totalWithRetry = checkedSoakAdd(
					totalWithRetry,
					checkedSoakMultiply(retrySlotCost, int64(retrySlots), addConflict),
					addConflict,
				)
				remainingRetrySlots -= retrySlots
				if remainingRetrySlots == 0 {
					break
				}
			}
		}
	}
	readback.LeaseBudget = SoakLeaseSummary{
		MinimumMS: input.Lease.MinimumMS, TargetMS: input.Lease.TargetMS,
		MaximumMS: input.Lease.MaximumMS, TotalPlannedMS: total,
		TotalPlannedWithRetryMS: totalWithRetry,
	}
	readback.LeaseBudget.Fits = totalWithRetry <= input.Lease.MaximumMS
	if !readback.LeaseBudget.Fits {
		addConflict("lease_budget_exceeded")
	}

	for code := range conflicts {
		readback.ConflictCodes = append(readback.ConflictCodes, code)
	}
	sort.Strings(readback.ConflictCodes)
	readback.ActivationAllowed = len(readback.ConflictCodes) == 0
	if readback.ActivationAllowed {
		readback.ExactNextAction = "Review this read-only plan and activate it only through a separately authorized execution system."
	} else {
		readback.ExactNextAction = "Correct the listed conflict codes and rerun this read-only planner; do not activate or execute this plan."
	}
	return readback, nil
}

func canonicalSoakInputDigest(input SoakPlanInput) string {
	normalized := input
	normalized.DurationHistory = append([]SoakDurationHistory(nil), input.DurationHistory...)
	for index := range normalized.DurationHistory {
		normalized.DurationHistory[index].Samples = append([]int64(nil), normalized.DurationHistory[index].Samples...)
		sort.Slice(normalized.DurationHistory[index].Samples, func(i, j int) bool {
			return normalized.DurationHistory[index].Samples[i] < normalized.DurationHistory[index].Samples[j]
		})
	}
	sort.Slice(normalized.DurationHistory, func(i, j int) bool {
		return normalized.DurationHistory[i].TestID < normalized.DurationHistory[j].TestID
	})
	body, _ := json.Marshal(normalized)
	return digestBytes(body)
}

func soakPolicyDigest(input SoakPlanInput) string {
	binding := soakPolicyBinding{
		Budgets: input.Budgets, PartitionBudgets: input.PartitionBudgets,
		RepeatPolicy: input.RepeatPolicy, RetryPolicy: input.RetryPolicy,
		TimeoutPolicy: input.TimeoutPolicy, Lease: input.Lease,
		SafetyBoundaries: input.SafetyBoundaries,
	}
	body, _ := json.Marshal(binding)
	return digestBytes(body)
}

func validateSoakEnvelope(input SoakPlanInput, policyDigest string, addConflict func(string)) {
	if input.Schema != SoakPlanInputSchema {
		addConflict("schema_version_unsupported")
	}
	if strings.TrimSpace(input.PlanID) == "" || strings.TrimSpace(input.MissionID) == "" ||
		!validSoakHexDigest(input.SourceHead, 40, "") || strings.TrimSpace(input.ExecutionProfile.ID) == "" ||
		!validSoakHexDigest(input.ExecutionProfile.Digest, 71, "sha256:") {
		addConflict("plan_identity_incomplete")
	}
	if (input.ExecutionProfile.Mode != "race" && input.ExecutionProfile.Mode != "non-race") ||
		(input.ExecutionProfile.Race != (input.ExecutionProfile.Mode == "race")) {
		addConflict("execution_profile_mode_invalid")
	}
	if len(input.TestCatalog) == 0 || input.Budgets.MaximumTests <= 0 ||
		len(input.TestCatalog) > input.Budgets.MaximumTests || len(input.TestCatalog) > 128 {
		addConflict("test_catalog_limit_exceeded")
	}
	if len(input.Partitions) == 0 || input.Budgets.MaximumPartitions <= 0 ||
		len(input.Partitions) > input.Budgets.MaximumPartitions || len(input.Partitions) > 64 {
		addConflict("partition_limit_exceeded")
	}
	if input.Budgets.SetupOverheadMS < 0 || input.Budgets.SafetyOverheadMS < 0 {
		addConflict("partition_overhead_invalid")
	}
	if input.RepeatPolicy.MaximumRegularRepeatCount < 1 || input.RepeatPolicy.ScaleRepeatCount != 1 {
		addConflict("repeat_policy_invalid")
	}
	if input.RepeatPolicy.ApplyPartitionRepeatToAll {
		addConflict("mixed_partition_scale_amplification")
	}
	validateSoakRetryPolicy(input.RetryPolicy, addConflict)
	if input.TimeoutPolicy.PerAttemptTimeoutMS <= 0 || input.TimeoutPolicy.TotalNodeTimeoutMS <= 0 {
		addConflict("timeout_policy_incomplete")
	}
	if input.Lease.MinimumMS <= 0 || input.Lease.MinimumMS > input.Lease.TargetMS ||
		input.Lease.TargetMS > input.Lease.MaximumMS {
		addConflict("lease_budget_invalid")
	}
	if input.PolicyDigest != policyDigest || input.Activation.BoundPolicyDigest != policyDigest {
		addConflict("activation_policy_digest_mismatch")
	}
	if !input.Activation.PolicyBoundBeforeActivation {
		addConflict("activation_predates_policy")
	}
	if input.Activation.State != "pre_activation" {
		addConflict("activation_state_invalid")
	}
	if soakSafetyEnabled(input.SafetyBoundaries) || !input.SafetyBoundaries.RSIRemainsDenied {
		addConflict("unsafe_authority_boundary")
	}
}

func validateSoakRetryPolicy(policy *SoakRetryPolicy, addConflict func(string)) {
	if policy == nil {
		addConflict("retry_policy_missing")
		return
	}
	if policy.MaximumAttempts < 1 || policy.MaximumAttempts > 4 ||
		len(policy.RetryableOutcomeClasses) == 0 || len(policy.NonRetryableOutcomeClasses) == 0 ||
		strings.TrimSpace(policy.CheckpointBehavior) == "" ||
		strings.TrimSpace(policy.EvidenceRequiredAfterFailure) == "" {
		addConflict("retry_policy_incomplete")
	}
	if !policy.PreservesNodeIdentity {
		addConflict("retry_node_identity_changed")
	}
	if !policy.PreservesTestSet {
		addConflict("retry_test_set_changed")
	}
	if !policy.PreservesScaleDimension {
		addConflict("retry_scale_dimension_changed")
	}
	if !policy.PreservesRepeatCount {
		addConflict("retry_repeat_count_changed")
	}
	if !policy.PreservesSourceHead {
		addConflict("retry_source_head_changed")
	}
	if !policy.PreservesExecutionProfile {
		addConflict("retry_execution_profile_changed")
	}
	if !policy.PreservesPhaseStart {
		addConflict("retry_phase_start_changed")
	}
	if !policy.PreservesAuthorityBoundaries {
		addConflict("retry_authority_broadened")
	}
	if !policy.PreservesSideEffectBoundaries {
		addConflict("retry_side_effects_broadened")
	}
	if !policy.PhaseClockResetProhibited {
		addConflict("retry_phase_clock_reset")
	}
}

func classifySoakTests(input SoakPlanInput, readback *SoakPlanReadback, addConflict func(string)) map[string]SoakTestEntry {
	readback.Classification.BoundBeforePartitioning = input.ClassificationBoundBeforePartitioning
	if !input.ClassificationBoundBeforePartitioning {
		addConflict("classification_after_partitioning")
	}
	catalog := make(map[string]SoakTestEntry, len(input.TestCatalog))
	for _, entry := range input.TestCatalog {
		if strings.TrimSpace(entry.ID) == "" || catalog[entry.ID].ID != "" {
			addConflict("test_catalog_identity_invalid")
			continue
		}
		catalog[entry.ID] = entry
		switch entry.Classification {
		case "":
			addConflict("classification_missing")
		case "regular":
			readback.Classification.RegularTests = append(readback.Classification.RegularTests, entry.ID)
			if entry.ScaleDimension != nil {
				addConflict("classification_contradictory")
			}
			if entry.RequestedRepeatCount < 1 || entry.RequestedRepeatCount > input.RepeatPolicy.MaximumRegularRepeatCount {
				addConflict("repeat_limit_exceeded")
			}
		case "scale":
			if entry.ScaleDimension == nil || strings.TrimSpace(entry.ScaleDimension.Unit) == "" ||
				entry.ScaleDimension.Value <= 0 || entry.ScaleDimension.Value > 1_000_000_000 {
				addConflict("scale_dimension_invalid")
			} else {
				readback.Classification.ScaleTests = append(readback.Classification.ScaleTests, SoakScaleTestSummary{
					TestID: entry.ID, WorkloadUnit: entry.ScaleDimension.Unit,
					WorkloadValue:        entry.ScaleDimension.Value,
					ClassificationReason: fmt.Sprintf("explicit bounded workload: %d %s", entry.ScaleDimension.Value, entry.ScaleDimension.Unit),
				})
			}
			if entry.RequestedRepeatCount != 1 {
				addConflict("scale_repeat_amplification")
			}
		default:
			addConflict("classification_unknown")
		}
	}
	sort.Strings(readback.Classification.RegularTests)
	sort.Slice(readback.Classification.ScaleTests, func(i, j int) bool {
		return readback.Classification.ScaleTests[i].TestID < readback.Classification.ScaleTests[j].TestID
	})
	return catalog
}

func validateSoakHistory(input SoakPlanInput, readback *SoakPlanReadback, addConflict func(string)) map[string]int64 {
	readback.DurationHistory = SoakDurationSummary{
		Unit: "milliseconds", Estimator: "nearest_rank_p95_plus_partition_overhead_ms",
		HistorySets: len(input.DurationHistory),
	}
	history := make(map[string]int64, len(input.DurationHistory))
	for _, entry := range input.DurationHistory {
		readback.DurationHistory.SampleCount += len(entry.Samples)
		if _, duplicate := history[entry.TestID]; duplicate {
			addConflict("duration_history_duplicate")
			continue
		}
		if entry.SourceHead != input.SourceHead {
			addConflict("duration_history_source_head_mismatch")
		}
		if entry.ExecutionProfileDigest != input.ExecutionProfile.Digest {
			addConflict("duration_history_profile_mismatch")
		}
		if entry.Unit != "milliseconds" {
			addConflict("duration_history_unit_mismatch")
		}
		if len(entry.Samples) == 0 {
			addConflict("duration_history_empty")
			continue
		}
		if len(entry.Samples) < soakPlanMinHistorySamples {
			addConflict("duration_history_insufficient")
			continue
		}
		if len(entry.Samples) > soakPlanMaxHistorySamples {
			addConflict("duration_history_sample_limit_exceeded")
			continue
		}
		samples := append([]int64(nil), entry.Samples...)
		valid := true
		for _, sample := range samples {
			if sample <= 0 {
				addConflict("duration_history_non_positive")
				valid = false
			}
			if sample > soakPlanMaxDurationMS {
				addConflict("duration_history_overflow_risk")
				valid = false
			}
		}
		if !valid {
			continue
		}
		sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
		rank := (95*len(samples) + 99) / 100
		history[entry.TestID] = samples[rank-1]
	}
	for _, test := range input.TestCatalog {
		if _, exists := history[test.ID]; !exists {
			addConflict("duration_history_missing")
		}
	}
	return history
}

func planSoakPartitions(input SoakPlanInput, catalog map[string]SoakTestEntry, history map[string]int64, addConflict func(string)) []SoakPlannedPartition {
	budgets := make(map[string]int64, len(input.PartitionBudgets))
	for _, budget := range input.PartitionBudgets {
		if budget.PartitionID == "" || budget.NodeBudgetMS <= 0 {
			addConflict("partition_budget_invalid")
			continue
		}
		if _, duplicate := budgets[budget.PartitionID]; duplicate {
			addConflict("partition_budget_duplicate")
		}
		budgets[budget.PartitionID] = budget.NodeBudgetMS
	}
	assigned := map[string]int{}
	var planned []SoakPlannedPartition
	for _, request := range input.Partitions {
		if strings.TrimSpace(request.PartitionID) == "" || strings.TrimSpace(request.NodeID) == "" || len(request.TestIDs) == 0 {
			addConflict("partition_identity_invalid")
		}
		nodeBudget, budgetFound := budgets[request.PartitionID]
		if !budgetFound {
			addConflict("partition_budget_missing")
		}
		seen := map[string]bool{}
		var regular []SoakTestEntry
		var scales []SoakTestEntry
		for _, testID := range request.TestIDs {
			if seen[testID] {
				addConflict("partition_test_duplicate")
				continue
			}
			seen[testID] = true
			assigned[testID]++
			entry, exists := catalog[testID]
			if !exists {
				addConflict("partition_test_unknown")
				continue
			}
			switch entry.Classification {
			case "regular":
				regular = append(regular, entry)
			case "scale":
				scales = append(scales, entry)
			}
		}
		sort.Slice(scales, func(i, j int) bool { return scales[i].ID < scales[j].ID })
		for _, entry := range scales {
			decision := "scale_repeat_one_preserved"
			if entry.RequestedRepeatCount != 1 {
				decision = "scale_repeat_rejected_effective_one"
			}
			planned = append(planned, buildSoakPartition(
				request.PartitionID+"-scale-"+entry.ID, request.NodeID+"-scale-"+entry.ID,
				"scale", []SoakTestEntry{entry}, entry.RequestedRepeatCount, 1, decision,
				nodeBudget, input, history, addConflict,
			))
		}
		if len(regular) > 0 {
			sort.Slice(regular, func(i, j int) bool { return regular[i].ID < regular[j].ID })
			repeat := regular[0].RequestedRepeatCount
			for _, entry := range regular {
				if entry.RequestedRepeatCount != repeat {
					addConflict("regular_repeat_partition_mismatch")
				}
			}
			planned = append(planned, buildSoakPartition(
				request.PartitionID+"-regular", request.NodeID+"-regular", "regular",
				regular, repeat, repeat, "bounded_regular_repeat_preserved", nodeBudget,
				input, history, addConflict,
			))
		}
	}
	for testID := range catalog {
		switch assigned[testID] {
		case 0:
			addConflict("partition_test_missing")
		case 1:
		default:
			addConflict("partition_test_duplicate")
		}
	}
	if len(planned) > input.Budgets.MaximumPartitions {
		addConflict("planned_partition_limit_exceeded")
	}
	return planned
}

func buildSoakPartition(partitionID, nodeID, classification string, tests []SoakTestEntry, requestedRepeat, effectiveRepeat int, decision string, nodeBudget int64, input SoakPlanInput, history map[string]int64, addConflict func(string)) SoakPlannedPartition {
	partition := SoakPlannedPartition{
		PartitionID: partitionID, NodeID: nodeID, Classification: classification,
		RequestedRepeatCount: requestedRepeat, EffectiveRepeatCount: effectiveRepeat,
		AmplificationDecision: decision, NodeBudgetMS: nodeBudget,
	}
	for _, test := range tests {
		partition.Tests = append(partition.Tests, test.ID)
		duration := checkedSoakMultiply(history[test.ID], int64(effectiveRepeat), addConflict)
		partition.EstimatedDurationMS = checkedSoakAdd(partition.EstimatedDurationMS, duration, addConflict)
	}
	partition.EstimatedDurationMS = checkedSoakAdd(partition.EstimatedDurationMS, input.Budgets.SetupOverheadMS, addConflict)
	partition.EstimatedDurationMS = checkedSoakAdd(partition.EstimatedDurationMS, input.Budgets.SafetyOverheadMS, addConflict)
	return partition
}

func checkedSoakMultiply(left, right int64, addConflict func(string)) int64 {
	if left < 0 || right < 0 || (left != 0 && right > math.MaxInt64/left) {
		addConflict("duration_arithmetic_overflow")
		return math.MaxInt64
	}
	return left * right
}

func checkedSoakAdd(left, right int64, addConflict func(string)) int64 {
	if left < 0 || right < 0 || right > math.MaxInt64-left {
		addConflict("duration_arithmetic_overflow")
		return math.MaxInt64
	}
	return left + right
}

func deniedSoakSafety() SoakSafetyBoundaries {
	return SoakSafetyBoundaries{RSIRemainsDenied: true}
}

func soakSafetyEnabled(safety SoakSafetyBoundaries) bool {
	return safety.SafeToExecute || safety.ExecutesWork || safety.ApprovesWork ||
		safety.MutatesRepositories || safety.CallsProviders || safety.Publishes ||
		safety.Releases || safety.Deploys || safety.AdvancesAuthority
}

func validSoakHexDigest(value string, length int, prefix string) bool {
	if len(value) != length || !strings.HasPrefix(value, prefix) {
		return false
	}
	for _, character := range strings.TrimPrefix(value, prefix) {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}
