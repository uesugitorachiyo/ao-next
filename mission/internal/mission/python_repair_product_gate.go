package mission

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	PythonRepairProductGateManifestSchema = "ao.mission.python-repair-product-gate-manifest.v1"
	PythonRepairProductGateEvidenceSchema = "ao.mission.python-repair-product-gate-evidence.v1"
	PythonRepairProductGateResultSchema   = "ao.mission.python-repair-product-gate-result.v1"
	pythonRepairProductGateLimit          = 64 * 1024
)

var (
	pythonRepairRepositoryPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,99}/[A-Za-z0-9][A-Za-z0-9_.-]{0,99}$`)
	pythonRepairSourcePattern     = regexp.MustCompile(`^[0-9a-f]{40}$`)
	pythonRepairSHA256Pattern     = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

type PythonRepairProductGateManifest struct {
	SchemaVersion    string                          `json:"schema_version"`
	GateID           string                          `json:"gate_id"`
	Repository       string                          `json:"repository"`
	IssueNumber      int                             `json:"issue_number"`
	SourceSHA        string                          `json:"source_sha"`
	SourceTreeSHA256 string                          `json:"source_tree_sha256"`
	CandidateSHA256  string                          `json:"candidate_sha256"`
	CorrelationID    string                          `json:"correlation_id"`
	Bindings         PythonRepairProductGateBindings `json:"bindings"`
	Evidence         PythonRepairProductGateArtifact `json:"evidence"`
}

type PythonRepairProductGateArtifact struct {
	Path      string `json:"path"`
	SizeBytes int64  `json:"size_bytes"`
	SHA256    string `json:"sha256"`
}

type PythonRepairProductGateEvidence struct {
	SchemaVersion    string                          `json:"schema_version"`
	GateID           string                          `json:"gate_id"`
	Repository       string                          `json:"repository"`
	IssueNumber      int                             `json:"issue_number"`
	SourceSHA        string                          `json:"source_sha"`
	SourceTreeSHA256 string                          `json:"source_tree_sha256"`
	CandidateSHA256  string                          `json:"candidate_sha256"`
	CorrelationID    string                          `json:"correlation_id"`
	CompletedAt      string                          `json:"completed_at"`
	ExpiresAt        string                          `json:"expires_at"`
	Bindings         PythonRepairProductGateBindings `json:"bindings"`
	Selection        PythonRepairGateSelection       `json:"selection"`
	Reproduction     PythonRepairGateReproduction    `json:"reproduction"`
	Candidate        PythonRepairGateCandidate       `json:"candidate"`
	RepairPack       PythonRepairGateRepairPack      `json:"repair_pack"`
	Qualification    PythonRepairGateQualification   `json:"qualification"`
	Lifecycle        PythonRepairGateLifecycle       `json:"lifecycle"`
	Security         PythonRepairGateSecurity        `json:"security"`
	Evaluation       PythonRepairGateEvaluation      `json:"evaluation"`
	Authority        PythonRepairGateAuthority       `json:"authority"`
}

type PythonRepairProductGateBindings struct {
	IssueSnapshotSHA256         string `json:"issue_snapshot_sha256"`
	SelectionSHA256             string `json:"selection_sha256"`
	ReproductionSHA256          string `json:"reproduction_sha256"`
	CandidateSealSHA256         string `json:"candidate_seal_sha256"`
	RepairPackValidationSHA256  string `json:"repair_pack_validation_sha256"`
	GovernedQualificationSHA256 string `json:"governed_qualification_sha256"`
	ProcessLifecycleSHA256      string `json:"process_lifecycle_sha256"`
	SecurityRoutingSHA256       string `json:"security_routing_sha256"`
	IndependentScoreSHA256      string `json:"independent_score_sha256"`
	AuthorityLedgerSHA256       string `json:"authority_ledger_sha256"`
}

type PythonRepairGateSelection struct {
	Deterministic       bool   `json:"deterministic"`
	ExactSource         bool   `json:"exact_source"`
	OracleAccessPreSeal bool   `json:"oracle_access_before_seal"`
	SelectedAt          string `json:"selected_at"`
	RecordSHA256        string `json:"record_sha256"`
}

type PythonRepairGateReproduction struct {
	Result             string `json:"result"`
	BaselineExitCode   int    `json:"baseline_exit_code"`
	Network            string `json:"network"`
	GitHistoryPresent  bool   `json:"git_history_present"`
	CredentialsPresent bool   `json:"credentials_present"`
	ExternalEffects    int    `json:"external_effects"`
	FixtureSHA256      string `json:"fixture_sha256"`
	OutputSHA256       string `json:"output_sha256"`
	EvidenceSHA256     string `json:"evidence_sha256"`
}

type PythonRepairGateCandidate struct {
	Sealed                    bool   `json:"sealed"`
	FocusedCandidateExitCode  int    `json:"focused_candidate_exit_code"`
	ChangedFilePrecision      bool   `json:"changed_file_precision"`
	ApplicableSuite           string `json:"applicable_suite"`
	TreeSHA256                string `json:"tree_sha256"`
	PatchSHA256               string `json:"patch_sha256"`
	SealSHA256                string `json:"seal_sha256"`
	BaselineSuiteSHA256       string `json:"baseline_suite_sha256"`
	ApplicableSuiteSHA256     string `json:"applicable_suite_sha256"`
	DeterministicReplaySHA256 string `json:"deterministic_replay_sha256"`
	DeterministicReplayPassed bool   `json:"deterministic_replay_passed"`
	BaselineComparison        string `json:"baseline_comparison"`
}

type PythonRepairGateRepairPack struct {
	SchemaVersion     string `json:"schema_version"`
	Status            string `json:"status"`
	EligibilityStatus string `json:"eligibility_status"`
	FailedRows        int    `json:"failed_rows"`
	ValidationSHA256  string `json:"validation_sha256"`
}

type PythonRepairGateQualification struct {
	Required     bool   `json:"required"`
	Status       string `json:"status"`
	RecordSHA256 string `json:"record_sha256"`
}

type PythonRepairGateLifecycle struct {
	Required        bool   `json:"required"`
	Status          string `json:"status"`
	OrphanProcesses int    `json:"orphan_processes"`
	RecordSHA256    string `json:"record_sha256"`
}

type PythonRepairGateSecurity struct {
	PrivateRoutingPassed   bool   `json:"private_routing_passed"`
	PublicManifestExcluded bool   `json:"public_manifest_excluded"`
	CredentialsPresent     bool   `json:"credentials_present"`
	RoutingSHA256          string `json:"routing_sha256"`
}

type PythonRepairGateEvaluation struct {
	Score                   int    `json:"score"`
	Threshold               int    `json:"threshold"`
	Correct                 bool   `json:"correct"`
	NegativeMutationsPassed bool   `json:"negative_mutations_passed"`
	RecordSHA256            string `json:"record_sha256"`
}

type PythonRepairGateAuthority struct {
	Level                     string `json:"level"`
	ProviderCalls             int    `json:"provider_calls"`
	ExternalEffects           int    `json:"external_effects"`
	ThirdPartyRemoteMutations int    `json:"third_party_remote_mutations"`
	ReleaseAttempted          bool   `json:"release_attempted"`
	DeploymentAttempted       bool   `json:"deployment_attempted"`
	PublicationAttempted      bool   `json:"publication_attempted"`
	LedgerSHA256              string `json:"ledger_sha256"`
}

type PythonRepairProductGateResult struct {
	SchemaVersion                 string `json:"schema_version"`
	Status                        string `json:"status"`
	GateID                        string `json:"gate_id"`
	Repository                    string `json:"repository"`
	IssueNumber                   int    `json:"issue_number"`
	SourceSHA                     string `json:"source_sha"`
	CandidateSHA256               string `json:"candidate_sha256"`
	ManifestSHA256                string `json:"manifest_sha256"`
	EvidenceSHA256                string `json:"evidence_sha256"`
	TechnicalRepairDecision       string `json:"technical_repair_decision"`
	GovernedQualificationDecision string `json:"governed_qualification_decision"`
	ReleaseDecision               string `json:"release_decision"`
	ProcessLifecyclePassed        bool   `json:"process_lifecycle_passed"`
	IndependentScore              int    `json:"independent_score"`
	ProviderCalls                 int    `json:"provider_calls"`
	ExternalEffects               int    `json:"external_effects"`
	ThirdPartyRemoteMutations     int    `json:"third_party_remote_mutations"`
	ExecutesWork                  bool   `json:"executes_work"`
	ApprovesWork                  bool   `json:"approves_work"`
	MutatesRepositories           bool   `json:"mutates_repositories"`
	ReleaseAttempted              bool   `json:"release_attempted"`
	DeploymentAttempted           bool   `json:"deployment_attempted"`
	PublicationAttempted          bool   `json:"publication_attempted"`
	AuthorityAdvanced             bool   `json:"authority_advanced"`
	GeneratedAtUTC                string `json:"generated_at_utc"`
}

func EvaluatePythonRepairProductGate(root, manifestPath string, currentTime time.Time) (PythonRepairProductGateResult, error) {
	canonicalRoot, err := canonicalPythonRepairGateRoot(root)
	if err != nil {
		return PythonRepairProductGateResult{}, err
	}
	manifestBody, err := readPythonRepairGateFile(canonicalRoot, manifestPath)
	if err != nil {
		return PythonRepairProductGateResult{}, err
	}
	var manifest PythonRepairProductGateManifest
	if err := decodeStrictJSONObject(manifestBody, &manifest, "Python repair product gate manifest", map[string]string{
		"schema_version": "string", "gate_id": "string", "repository": "string",
		"issue_number": "integer", "source_sha": "string", "source_tree_sha256": "string",
		"candidate_sha256": "string", "correlation_id": "string", "bindings": "object",
		"evidence": "object",
	}, []string{
		"schema_version", "gate_id", "repository", "issue_number", "source_sha",
		"source_tree_sha256", "candidate_sha256", "correlation_id", "bindings", "evidence",
	}); err != nil {
		return PythonRepairProductGateResult{}, err
	}
	if manifest.SchemaVersion != PythonRepairProductGateManifestSchema {
		return PythonRepairProductGateResult{}, errors.New("Python repair product gate manifest schema is unsupported")
	}
	if err := validatePythonRepairGateArtifact(manifest.Evidence); err != nil {
		return PythonRepairProductGateResult{}, err
	}
	evidenceBody, err := readPythonRepairGateFile(canonicalRoot, manifest.Evidence.Path)
	if err != nil {
		return PythonRepairProductGateResult{}, err
	}
	if int64(len(evidenceBody)) != manifest.Evidence.SizeBytes ||
		pythonRepairDigest(evidenceBody) != manifest.Evidence.SHA256 {
		return PythonRepairProductGateResult{}, errors.New("Python repair product gate evidence size or digest mismatch")
	}
	var evidence PythonRepairProductGateEvidence
	if err := decodeStrictJSONObject(evidenceBody, &evidence, "Python repair product gate evidence", map[string]string{
		"schema_version": "string", "gate_id": "string", "repository": "string",
		"issue_number": "integer", "source_sha": "string", "source_tree_sha256": "string",
		"candidate_sha256": "string", "correlation_id": "string",
		"completed_at": "string", "expires_at": "string", "bindings": "object",
		"selection": "object", "reproduction": "object", "candidate": "object",
		"repair_pack": "object", "qualification": "object", "lifecycle": "object",
		"security": "object", "evaluation": "object", "authority": "object",
	}, []string{
		"schema_version", "gate_id", "repository", "issue_number", "source_sha",
		"source_tree_sha256", "candidate_sha256", "correlation_id",
		"completed_at", "expires_at", "bindings", "selection",
		"reproduction", "candidate", "repair_pack", "qualification", "lifecycle",
		"security", "evaluation", "authority",
	}); err != nil {
		return PythonRepairProductGateResult{}, err
	}
	if err := validatePythonRepairProductGateEvidence(manifest, evidence, currentTime.UTC()); err != nil {
		return PythonRepairProductGateResult{}, err
	}
	qualification := "not_run"
	releaseDecision := "not_qualified"
	if evidence.Qualification.Required {
		qualification = "passed"
		if evidence.Lifecycle.Required {
			releaseDecision = "eligible_for_separate_authorization"
		}
	}
	return PythonRepairProductGateResult{
		SchemaVersion: PythonRepairProductGateResultSchema, Status: "passed",
		GateID: evidence.GateID, Repository: evidence.Repository, IssueNumber: evidence.IssueNumber,
		SourceSHA: evidence.SourceSHA, CandidateSHA256: evidence.CandidateSHA256,
		ManifestSHA256: pythonRepairDigest(manifestBody), EvidenceSHA256: pythonRepairDigest(evidenceBody),
		TechnicalRepairDecision: "passed", GovernedQualificationDecision: qualification,
		ReleaseDecision: releaseDecision, ProcessLifecyclePassed: !evidence.Lifecycle.Required || evidence.Lifecycle.Status == "passed",
		IndependentScore: evidence.Evaluation.Score, ProviderCalls: 0, ExternalEffects: 0,
		ThirdPartyRemoteMutations: 0, GeneratedAtUTC: currentTime.UTC().Format(time.RFC3339),
	}, nil
}

func canonicalPythonRepairGateRoot(root string) (string, error) {
	absolute, err := filepath.Abs(strings.TrimSpace(root))
	if err != nil {
		return "", err
	}
	absolute = filepath.Clean(absolute)
	info, err := os.Lstat(absolute)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("Python repair product gate root must be a non-symlink directory")
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	canonical, err = filepath.Abs(canonical)
	if err != nil {
		return "", err
	}
	return filepath.Clean(canonical), nil
}

func readPythonRepairGateFile(root, path string) ([]byte, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("Python repair product gate file path is required")
	}
	if !filepath.IsAbs(path) {
		if filepath.Base(path) != path || path == "." || path == ".." {
			return nil, errors.New("Python repair product gate artifact must be a direct child")
		}
		path = filepath.Join(root, path)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	absolute = filepath.Clean(absolute)
	file, err := openCorrelationInput(absolute)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !opened.Mode().IsRegular() {
		return nil, errors.New("Python repair product gate artifact must be a regular file")
	}
	if multiple, err := pythonRepairGateHasMultipleLinks(file, opened); err != nil {
		return nil, err
	} else if multiple {
		return nil, errors.New("Python repair product gate artifact must not be hardlinked")
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, err
	}
	canonical, err = filepath.Abs(canonical)
	if err != nil {
		return nil, err
	}
	canonical = filepath.Clean(canonical)
	if filepath.Dir(canonical) != root {
		return nil, errors.New("Python repair product gate artifact escapes its canonical root")
	}
	pathBeforeRead, err := os.Lstat(canonical)
	if err != nil {
		return nil, err
	}
	if pathBeforeRead.Mode()&os.ModeSymlink != 0 || !pathBeforeRead.Mode().IsRegular() ||
		!os.SameFile(opened, pathBeforeRead) {
		return nil, errors.New("Python repair product gate artifact changed before read")
	}
	body, err := io.ReadAll(io.LimitReader(file, pythonRepairProductGateLimit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > pythonRepairProductGateLimit {
		return nil, fmt.Errorf("Python repair product gate artifact exceeds %d bytes", pythonRepairProductGateLimit)
	}
	afterRead, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if multiple, err := pythonRepairGateHasMultipleLinks(file, afterRead); err != nil {
		return nil, err
	} else if multiple {
		return nil, errors.New("Python repair product gate artifact must not be hardlinked")
	}
	pathAfterRead, err := os.Lstat(canonical)
	if err != nil {
		return nil, err
	}
	if !afterRead.Mode().IsRegular() || afterRead.Size() != int64(len(body)) ||
		!pathAfterRead.Mode().IsRegular() || !os.SameFile(opened, afterRead) ||
		!os.SameFile(opened, pathAfterRead) {
		return nil, errors.New("Python repair product gate artifact changed while reading")
	}
	return body, nil
}

func validatePythonRepairGateArtifact(artifact PythonRepairProductGateArtifact) error {
	if filepath.Base(artifact.Path) != artifact.Path || artifact.Path == "." || artifact.Path == ".." {
		return errors.New("Python repair product gate evidence path must be a direct child")
	}
	if artifact.SizeBytes < 0 || artifact.SizeBytes > pythonRepairProductGateLimit ||
		!pythonRepairSHA256Pattern.MatchString(artifact.SHA256) {
		return errors.New("Python repair product gate evidence artifact is malformed")
	}
	return nil
}

func validatePythonRepairProductGateEvidence(
	manifest PythonRepairProductGateManifest,
	e PythonRepairProductGateEvidence,
	currentTime time.Time,
) error {
	if e.SchemaVersion != PythonRepairProductGateEvidenceSchema ||
		!issueRepairIDPattern.MatchString(e.GateID) ||
		!pythonRepairRepositoryPattern.MatchString(e.Repository) || e.IssueNumber <= 0 ||
		!pythonRepairSourcePattern.MatchString(e.SourceSHA) ||
		!pythonRepairSHA256Pattern.MatchString(e.SourceTreeSHA256) ||
		!pythonRepairSHA256Pattern.MatchString(e.CandidateSHA256) {
		return errors.New("Python repair product gate identity is invalid")
	}
	completed, err := time.Parse(time.RFC3339, e.CompletedAt)
	if err != nil {
		return errors.New("Python repair product gate completed_at is invalid")
	}
	expires, err := time.Parse(time.RFC3339, e.ExpiresAt)
	if err != nil || expires.Before(completed) || expires.After(completed.Add(7*24*time.Hour)) ||
		!expires.After(currentTime) || completed.After(currentTime.Add(5*time.Minute)) ||
		completed.Before(currentTime.Add(-7*24*time.Hour)) {
		return errors.New("Python repair product gate evidence is stale or has an invalid freshness window")
	}
	selected, err := time.Parse(time.RFC3339, e.Selection.SelectedAt)
	if err != nil || selected.After(completed) || selected.Before(completed.Add(-7*24*time.Hour)) ||
		selected.After(currentTime.Add(5*time.Minute)) {
		return errors.New("Python repair product gate selection timestamp is invalid")
	}
	if err := validatePythonRepairGateManifestBinding(manifest, e); err != nil {
		return err
	}
	if err := validatePythonRepairGateBindings(e); err != nil {
		return err
	}
	if !e.Selection.Deterministic || !e.Selection.ExactSource || e.Selection.OracleAccessPreSeal ||
		e.Selection.RecordSHA256 != e.Bindings.SelectionSHA256 {
		return errors.New("Python repair product gate selection evidence failed")
	}
	if e.Reproduction.Result != "reproduced_failure" || e.Reproduction.BaselineExitCode < 1 ||
		e.Reproduction.BaselineExitCode > 255 || e.Reproduction.Network != "none" ||
		e.Reproduction.GitHistoryPresent || e.Reproduction.CredentialsPresent || e.Reproduction.ExternalEffects != 0 ||
		!pythonRepairSHA256Pattern.MatchString(e.Reproduction.FixtureSHA256) ||
		!pythonRepairSHA256Pattern.MatchString(e.Reproduction.OutputSHA256) ||
		e.Reproduction.EvidenceSHA256 != e.Bindings.ReproductionSHA256 {
		return errors.New("Python repair product gate reproduction evidence failed")
	}
	if !e.Candidate.Sealed || e.Candidate.FocusedCandidateExitCode != 0 ||
		!e.Candidate.ChangedFilePrecision ||
		(e.Candidate.ApplicableSuite != "passed" && e.Candidate.ApplicableSuite != "baseline_limitation_matched") ||
		e.Candidate.TreeSHA256 != e.CandidateSHA256 || e.Candidate.SealSHA256 != e.Bindings.CandidateSealSHA256 ||
		!pythonRepairSHA256Pattern.MatchString(e.Candidate.PatchSHA256) ||
		!pythonRepairSHA256Pattern.MatchString(e.Candidate.BaselineSuiteSHA256) ||
		!pythonRepairSHA256Pattern.MatchString(e.Candidate.ApplicableSuiteSHA256) ||
		!pythonRepairSHA256Pattern.MatchString(e.Candidate.DeterministicReplaySHA256) ||
		!e.Candidate.DeterministicReplayPassed ||
		(e.Candidate.BaselineComparison != "green" && e.Candidate.BaselineComparison != "matched_limitation") {
		return errors.New("Python repair product gate candidate evidence failed")
	}
	if e.RepairPack.SchemaVersion != "ao2.github-issue-repair-pack-validation.v3" ||
		e.RepairPack.Status != "passed" || e.RepairPack.EligibilityStatus != "reproduced" ||
		e.RepairPack.FailedRows != 0 || e.RepairPack.ValidationSHA256 != e.Bindings.RepairPackValidationSHA256 {
		return errors.New("Python repair product gate repair-pack evidence failed")
	}
	if (e.Qualification.Required && (e.Qualification.Status != "passed" ||
		e.Qualification.RecordSHA256 != e.Bindings.GovernedQualificationSHA256)) ||
		(!e.Qualification.Required && (e.Qualification.Status != "not_run" || e.Qualification.RecordSHA256 != "")) {
		return errors.New("Python repair product gate qualification evidence failed")
	}
	if (e.Lifecycle.Required && (e.Lifecycle.Status != "passed" || e.Lifecycle.OrphanProcesses != 0 ||
		e.Lifecycle.RecordSHA256 != e.Bindings.ProcessLifecycleSHA256)) ||
		(!e.Lifecycle.Required && (e.Lifecycle.Status != "not_applicable" || e.Lifecycle.OrphanProcesses != 0 ||
			e.Lifecycle.RecordSHA256 != "")) {
		return errors.New("Python repair product gate lifecycle evidence failed")
	}
	if !e.Security.PrivateRoutingPassed || !e.Security.PublicManifestExcluded || e.Security.CredentialsPresent ||
		e.Security.RoutingSHA256 != e.Bindings.SecurityRoutingSHA256 {
		return errors.New("Python repair product gate security routing evidence failed")
	}
	if e.Evaluation.Score < 0 || e.Evaluation.Score > 10 || e.Evaluation.Threshold < 1 ||
		e.Evaluation.Threshold > 10 || e.Evaluation.Score < e.Evaluation.Threshold ||
		!e.Evaluation.Correct || !e.Evaluation.NegativeMutationsPassed ||
		e.Evaluation.RecordSHA256 != e.Bindings.IndependentScoreSHA256 {
		return errors.New("Python repair product gate independent evaluation failed")
	}
	if e.Authority.Level != "L1" || e.Authority.ProviderCalls != 0 || e.Authority.ExternalEffects != 0 ||
		e.Authority.ThirdPartyRemoteMutations != 0 || e.Authority.ReleaseAttempted ||
		e.Authority.DeploymentAttempted || e.Authority.PublicationAttempted ||
		e.Authority.LedgerSHA256 != e.Bindings.AuthorityLedgerSHA256 {
		return errors.New("Python repair product gate authority boundary failed")
	}
	return nil
}

func validatePythonRepairGateManifestBinding(
	manifest PythonRepairProductGateManifest,
	e PythonRepairProductGateEvidence,
) error {
	if !issueRepairIDPattern.MatchString(manifest.GateID) ||
		!pythonRepairRepositoryPattern.MatchString(manifest.Repository) || manifest.IssueNumber <= 0 ||
		!pythonRepairSourcePattern.MatchString(manifest.SourceSHA) ||
		!pythonRepairSHA256Pattern.MatchString(manifest.SourceTreeSHA256) ||
		!pythonRepairSHA256Pattern.MatchString(manifest.CandidateSHA256) ||
		!issueRepairIDPattern.MatchString(manifest.CorrelationID) {
		return errors.New("Python repair product gate manifest identity is invalid")
	}
	if manifest.GateID != e.GateID || manifest.Repository != e.Repository ||
		manifest.IssueNumber != e.IssueNumber || manifest.SourceSHA != e.SourceSHA ||
		manifest.SourceTreeSHA256 != e.SourceTreeSHA256 || manifest.CandidateSHA256 != e.CandidateSHA256 ||
		manifest.CorrelationID != e.CorrelationID || manifest.Bindings != e.Bindings {
		return errors.New("Python repair product gate manifest and evidence identities do not match")
	}
	return nil
}

func validatePythonRepairGateBindings(e PythonRepairProductGateEvidence) error {
	required := []string{
		e.Bindings.IssueSnapshotSHA256, e.Bindings.SelectionSHA256,
		e.Bindings.ReproductionSHA256, e.Bindings.CandidateSealSHA256,
		e.Bindings.RepairPackValidationSHA256, e.Bindings.SecurityRoutingSHA256,
		e.Bindings.IndependentScoreSHA256, e.Bindings.AuthorityLedgerSHA256,
	}
	for _, digest := range required {
		if !pythonRepairSHA256Pattern.MatchString(digest) {
			return errors.New("Python repair product gate required binding is malformed")
		}
	}
	if e.Qualification.Required != pythonRepairSHA256Pattern.MatchString(e.Bindings.GovernedQualificationSHA256) ||
		e.Lifecycle.Required != pythonRepairSHA256Pattern.MatchString(e.Bindings.ProcessLifecycleSHA256) {
		return errors.New("Python repair product gate optional binding does not match applicability")
	}
	if (!e.Qualification.Required && e.Bindings.GovernedQualificationSHA256 != "") ||
		(!e.Lifecycle.Required && e.Bindings.ProcessLifecycleSHA256 != "") {
		return errors.New("Python repair product gate inapplicable binding must be empty")
	}
	return nil
}

func pythonRepairDigest(body []byte) string {
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}
