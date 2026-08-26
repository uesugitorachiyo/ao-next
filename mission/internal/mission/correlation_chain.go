package mission

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	CorrelationChainSchema                 = "ao.mission.correlation-chain.v0.1"
	CorrelationChainValidationSchema       = "ao.mission.correlation-chain-validation.v0.1"
	CorrelationChainReferenceSchema        = "ao.mission.correlation-chain-reference.v0.1"
	CorrelationBindingNativeField          = "native_field"
	CorrelationBindingDigestLink           = "digest_link"
	maxCorrelationArtifactBytes            = int64(16 << 20)
	correlationRedactedPathSentinel        = "<local-path-redacted>"
	correlationLocatorStateLive            = "live"
	correlationLocatorStateArchiveRedacted = "archive_redacted"
	ao2EvidencePackSchema                  = "ao2.evidence-pack.v1"
)

var (
	correlationRolePattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	sha256DigestPattern       = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	rawSHA256DigestPattern    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	correlationContractFields = map[string]struct{}{
		"schema": {}, "schema_version": {}, "contract_version": {},
		"mission_id": {}, "correlation_id": {}, "entries": {},
		"role": {}, "artifact_path": {}, "artifact_name": {},
		"digest": {}, "producer": {}, "binding_mode": {},
		"native_identifier": {}, "native_field": {},
		"parent_role": {}, "parent_digest": {}, "parent_digest_field": {},
		"safe_to_execute": {}, "executes_work": {}, "approves_work": {},
		"mutates_repositories": {}, "widens_policy": {}, "publishes_artifacts": {},
		"chain_digest": {}, "reference_digest": {},
		"locator_state": {}, "locator_digest": {}, "archive_source_locator_digest": {},
		"action_digest": {}, "request_id": {}, "result_id": {}, "sha256": {},
	}
)

type CorrelationArtifactSpec struct {
	Role string
	Path string
}

type CorrelationChain struct {
	Schema              string                  `json:"schema"`
	MissionID           string                  `json:"mission_id"`
	CorrelationID       string                  `json:"correlation_id"`
	Entries             []CorrelationChainEntry `json:"entries"`
	SafeToExecute       bool                    `json:"safe_to_execute"`
	ExecutesWork        bool                    `json:"executes_work"`
	ApprovesWork        bool                    `json:"approves_work"`
	MutatesRepositories bool                    `json:"mutates_repositories"`
	WidensPolicy        bool                    `json:"widens_policy"`
	PublishesArtifacts  bool                    `json:"publishes_artifacts"`
}

func (chain *CorrelationChain) UnmarshalJSON(data []byte) error {
	type alias CorrelationChain
	var decoded alias
	if err := decodeStrictJSONObject(data, &decoded, "correlation chain", map[string]string{
		"schema": "string", "mission_id": "string", "correlation_id": "string",
		"entries": "array", "safe_to_execute": "boolean", "executes_work": "boolean",
		"approves_work": "boolean", "mutates_repositories": "boolean",
		"widens_policy": "boolean", "publishes_artifacts": "boolean",
	}, []string{
		"schema", "mission_id", "correlation_id", "entries",
		"safe_to_execute", "executes_work", "approves_work",
		"mutates_repositories", "widens_policy", "publishes_artifacts",
	}); err != nil {
		return err
	}
	*chain = CorrelationChain(decoded)
	return nil
}

type CorrelationChainEntry struct {
	Role                string `json:"role"`
	ArtifactPath        string `json:"artifact_path,omitempty"`
	ArtifactName        string `json:"artifact_name,omitempty"`
	Digest              string `json:"digest"`
	Producer            string `json:"producer"`
	BindingMode         string `json:"binding_mode"`
	NativeIdentifier    string `json:"native_identifier,omitempty"`
	NativeField         string `json:"native_field,omitempty"`
	ParentRole          string `json:"parent_role,omitempty"`
	ParentDigest        string `json:"parent_digest,omitempty"`
	ParentDigestField   string `json:"parent_digest_field,omitempty"`
	SafeToExecute       bool   `json:"safe_to_execute"`
	ExecutesWork        bool   `json:"executes_work"`
	ApprovesWork        bool   `json:"approves_work"`
	MutatesRepositories bool   `json:"mutates_repositories"`
	WidensPolicy        bool   `json:"widens_policy"`
	PublishesArtifacts  bool   `json:"publishes_artifacts"`
}

func (entry *CorrelationChainEntry) UnmarshalJSON(data []byte) error {
	type alias CorrelationChainEntry
	var decoded alias
	if err := decodeStrictJSONObject(data, &decoded, "correlation chain entry", map[string]string{
		"role": "string", "artifact_path": "string", "artifact_name": "string",
		"digest": "string", "producer": "string", "binding_mode": "string",
		"native_identifier": "string", "native_field": "string",
		"parent_role": "string", "parent_digest": "string", "parent_digest_field": "string",
		"safe_to_execute": "boolean", "executes_work": "boolean",
		"approves_work": "boolean", "mutates_repositories": "boolean",
		"widens_policy": "boolean", "publishes_artifacts": "boolean",
	}, []string{
		"role", "digest", "producer", "binding_mode",
		"safe_to_execute", "executes_work", "approves_work",
		"mutates_repositories", "widens_policy", "publishes_artifacts",
	}); err != nil {
		return err
	}
	*entry = CorrelationChainEntry(decoded)
	return nil
}

type CorrelationChainValidation struct {
	Schema              string `json:"schema"`
	Status              string `json:"status"`
	MissionID           string `json:"mission_id,omitempty"`
	CorrelationID       string `json:"correlation_id,omitempty"`
	ChainDigest         string `json:"chain_digest,omitempty"`
	ArtifactCount       int    `json:"artifact_count"`
	SafeToExecute       bool   `json:"safe_to_execute"`
	ExecutesWork        bool   `json:"executes_work"`
	ApprovesWork        bool   `json:"approves_work"`
	MutatesRepositories bool   `json:"mutates_repositories"`
	WidensPolicy        bool   `json:"widens_policy"`
	PublishesArtifacts  bool   `json:"publishes_artifacts"`
}

type CorrelationChainReferenceEntry struct {
	Role                       string `json:"role"`
	Digest                     string `json:"digest"`
	LocatorState               string `json:"locator_state"`
	LocatorDigest              string `json:"locator_digest"`
	ArchiveSourceLocatorDigest string `json:"archive_source_locator_digest,omitempty"`
	Producer                   string `json:"producer"`
	BindingMode                string `json:"binding_mode"`
	NativeIdentifier           string `json:"native_identifier,omitempty"`
	NativeField                string `json:"native_field,omitempty"`
	ParentRole                 string `json:"parent_role,omitempty"`
	ParentDigest               string `json:"parent_digest,omitempty"`
	ParentDigestField          string `json:"parent_digest_field,omitempty"`
	SafeToExecute              bool   `json:"safe_to_execute"`
	ExecutesWork               bool   `json:"executes_work"`
	ApprovesWork               bool   `json:"approves_work"`
	MutatesRepositories        bool   `json:"mutates_repositories"`
	WidensPolicy               bool   `json:"widens_policy"`
	PublishesArtifacts         bool   `json:"publishes_artifacts"`
}

func (entry *CorrelationChainReferenceEntry) UnmarshalJSON(data []byte) error {
	type alias CorrelationChainReferenceEntry
	var decoded alias
	if err := decodeStrictJSONObject(data, &decoded, "correlation chain reference entry", map[string]string{
		"role": "string", "digest": "string", "locator_state": "string",
		"locator_digest": "string", "archive_source_locator_digest": "string",
		"producer": "string", "binding_mode": "string",
		"native_identifier": "string", "native_field": "string",
		"parent_role": "string", "parent_digest": "string", "parent_digest_field": "string",
		"safe_to_execute": "boolean", "executes_work": "boolean",
		"approves_work": "boolean", "mutates_repositories": "boolean",
		"widens_policy": "boolean", "publishes_artifacts": "boolean",
	}, []string{
		"role", "digest", "locator_state", "locator_digest", "producer", "binding_mode",
		"safe_to_execute", "executes_work", "approves_work",
		"mutates_repositories", "widens_policy", "publishes_artifacts",
	}); err != nil {
		return err
	}
	*entry = CorrelationChainReferenceEntry(decoded)
	return nil
}

type CorrelationChainReference struct {
	Schema              string                           `json:"schema"`
	MissionID           string                           `json:"mission_id"`
	CorrelationID       string                           `json:"correlation_id"`
	ChainDigest         string                           `json:"chain_digest"`
	ReferenceDigest     string                           `json:"reference_digest"`
	Entries             []CorrelationChainReferenceEntry `json:"entries"`
	SafeToExecute       bool                             `json:"safe_to_execute"`
	ExecutesWork        bool                             `json:"executes_work"`
	ApprovesWork        bool                             `json:"approves_work"`
	MutatesRepositories bool                             `json:"mutates_repositories"`
	WidensPolicy        bool                             `json:"widens_policy"`
	PublishesArtifacts  bool                             `json:"publishes_artifacts"`
}

func (reference *CorrelationChainReference) UnmarshalJSON(data []byte) error {
	type alias CorrelationChainReference
	var decoded alias
	if err := decodeStrictJSONObject(data, &decoded, "correlation chain reference", map[string]string{
		"schema": "string", "mission_id": "string", "correlation_id": "string",
		"chain_digest": "string", "reference_digest": "string", "entries": "array",
		"safe_to_execute": "boolean", "executes_work": "boolean",
		"approves_work": "boolean", "mutates_repositories": "boolean",
		"widens_policy": "boolean", "publishes_artifacts": "boolean",
	}, []string{
		"schema", "mission_id", "correlation_id", "chain_digest", "reference_digest", "entries",
		"safe_to_execute", "executes_work", "approves_work",
		"mutates_repositories", "widens_policy", "publishes_artifacts",
	}); err != nil {
		return err
	}
	*reference = CorrelationChainReference(decoded)
	return nil
}

type CorrelatedImportBinding struct {
	Role                       string `json:"role"`
	Digest                     string `json:"digest"`
	ArtifactPath               string `json:"artifact_path"`
	LocatorState               string `json:"locator_state"`
	LocatorDigest              string `json:"locator_digest"`
	ArchiveSourceLocatorDigest string `json:"archive_source_locator_digest,omitempty"`
	ChainDigest                string `json:"chain_digest"`
	ReferenceDigest            string `json:"reference_digest"`
}

func (binding *CorrelatedImportBinding) UnmarshalJSON(data []byte) error {
	type alias CorrelatedImportBinding
	var decoded alias
	if err := decodeStrictJSONObject(data, &decoded, "correlated import", map[string]string{
		"role": "string", "digest": "string", "artifact_path": "string",
		"locator_state": "string", "locator_digest": "string",
		"archive_source_locator_digest": "string",
		"chain_digest":                  "string", "reference_digest": "string",
	}, []string{
		"role", "digest", "artifact_path", "locator_state", "locator_digest",
		"chain_digest", "reference_digest",
	}); err != nil {
		return err
	}
	*binding = CorrelatedImportBinding(decoded)
	return nil
}

type correlationArtifactDraft struct {
	entry    CorrelationChainEntry
	document map[string]any
}

func BuildCorrelationChain(record Record, specs []CorrelationArtifactSpec) (CorrelationChain, error) {
	chain := CorrelationChain{
		Schema:        CorrelationChainSchema,
		MissionID:     strings.TrimSpace(record.MissionID),
		CorrelationID: strings.TrimSpace(record.CorrelationID),
		Entries:       []CorrelationChainEntry{},
	}
	if chain.MissionID == "" {
		return CorrelationChain{}, errors.New("correlation chain requires a mission ID")
	}
	if !correlationIDPattern.MatchString(chain.CorrelationID) {
		return CorrelationChain{}, errors.New("correlation chain requires the Mission record correlation_id")
	}
	if len(specs) == 0 {
		return CorrelationChain{}, errors.New("correlation chain requires at least one artifact")
	}

	drafts := make([]correlationArtifactDraft, 0, len(specs))
	roles := make(map[string]struct{}, len(specs))
	locators := make(map[string]struct{}, len(specs))
	for _, spec := range specs {
		role := strings.TrimSpace(spec.Role)
		if !correlationRolePattern.MatchString(role) {
			return CorrelationChain{}, fmt.Errorf("artifact role %q is invalid", spec.Role)
		}
		if _, duplicate := roles[role]; duplicate {
			return CorrelationChain{}, fmt.Errorf("duplicate artifact role %q", role)
		}
		roles[role] = struct{}{}

		canonicalPath, body, err := readCanonicalCorrelationArtifact(spec.Path)
		if err != nil {
			return CorrelationChain{}, fmt.Errorf("artifact %q: %w", role, err)
		}
		if _, duplicate := locators[canonicalPath]; duplicate {
			return CorrelationChain{}, fmt.Errorf("duplicate artifact path %q", canonicalPath)
		}
		locators[canonicalPath] = struct{}{}
		document, err := decodeJSONObject(body)
		if err != nil {
			return CorrelationChain{}, fmt.Errorf("artifact %q must be one JSON object: %w", role, err)
		}
		producer, err := correlationProducer(document)
		if err != nil {
			return CorrelationChain{}, fmt.Errorf("artifact %q: %w", role, err)
		}
		if err := rejectArtifactIdentityMismatch(document, record); err != nil {
			return CorrelationChain{}, fmt.Errorf("artifact %q: %w", role, err)
		}
		drafts = append(drafts, correlationArtifactDraft{
			entry: CorrelationChainEntry{
				Role:         role,
				ArtifactPath: canonicalPath,
				Digest:       digestBytes(body),
				Producer:     producer,
			},
			document: document,
		})
	}

	sort.Slice(drafts, func(i, j int) bool {
		return drafts[i].entry.Role < drafts[j].entry.Role
	})
	for i := range drafts {
		binding, err := deriveCorrelationBinding(drafts[i], drafts, record)
		if err != nil {
			return CorrelationChain{}, fmt.Errorf("artifact %q: %w", drafts[i].entry.Role, err)
		}
		applyCorrelationBinding(&drafts[i].entry, binding)
	}
	for _, draft := range drafts {
		chain.Entries = append(chain.Entries, draft.entry)
	}
	return chain, nil
}

func ValidateCorrelationChainFile(path string) (CorrelationChainValidation, error) {
	chain, canonicalPath, err := loadCorrelationChain(path)
	if err != nil {
		return blockedCorrelationChainValidation(CorrelationChain{}, err)
	}
	if err := validateCorrelationChain(chain, filepath.Dir(canonicalPath)); err != nil {
		return blockedCorrelationChainValidation(chain, err)
	}
	return readyCorrelationChainValidation(chain)
}

func ValidateCorrelationChainForRecord(path string, record Record) (CorrelationChainValidation, error) {
	chain, canonicalPath, err := loadCorrelationChain(path)
	if err != nil {
		return blockedCorrelationChainValidation(CorrelationChain{}, err)
	}
	if chain.MissionID != record.MissionID {
		return blockedCorrelationChainValidation(chain, errors.New("correlation chain mission_id does not match Mission record"))
	}
	if chain.CorrelationID != record.CorrelationID {
		return blockedCorrelationChainValidation(chain, errors.New("correlation chain correlation_id does not match Mission record"))
	}
	if err := validateCorrelationChain(chain, filepath.Dir(canonicalPath)); err != nil {
		return blockedCorrelationChainValidation(chain, err)
	}
	return readyCorrelationChainValidation(chain)
}

func loadValidatedCorrelationChainForRecord(path string, record Record) (CorrelationChain, CorrelationChainValidation, error) {
	chain, canonicalPath, err := loadCorrelationChain(path)
	if err != nil {
		validation, validationErr := blockedCorrelationChainValidation(CorrelationChain{}, err)
		return CorrelationChain{}, validation, validationErr
	}
	if chain.MissionID != record.MissionID {
		validation, validationErr := blockedCorrelationChainValidation(chain, errors.New("correlation chain mission_id does not match Mission record"))
		return CorrelationChain{}, validation, validationErr
	}
	if chain.CorrelationID != record.CorrelationID {
		validation, validationErr := blockedCorrelationChainValidation(chain, errors.New("correlation chain correlation_id does not match Mission record"))
		return CorrelationChain{}, validation, validationErr
	}
	if err := validateCorrelationChain(chain, filepath.Dir(canonicalPath)); err != nil {
		validation, validationErr := blockedCorrelationChainValidation(chain, err)
		return CorrelationChain{}, validation, validationErr
	}
	validation, err := readyCorrelationChainValidation(chain)
	return chain, validation, err
}

func loadCorrelationChain(path string) (CorrelationChain, string, error) {
	var chain CorrelationChain
	canonicalPath, body, err := readCanonicalCorrelationArtifact(path)
	if err != nil {
		return chain, "", err
	}
	if _, err := decodeExactJSON(body); err != nil {
		return chain, "", err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&chain); err != nil {
		return chain, "", err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return chain, "", errors.New("correlation chain contains trailing JSON")
		}
		return chain, "", err
	}
	return chain, canonicalPath, nil
}

func validateCorrelationChain(chain CorrelationChain, chainDir string) error {
	switch {
	case chain.Schema != CorrelationChainSchema:
		return fmt.Errorf("correlation chain schema must be %s", CorrelationChainSchema)
	case strings.TrimSpace(chain.MissionID) == "":
		return errors.New("correlation chain mission_id is required")
	case !correlationIDPattern.MatchString(chain.CorrelationID):
		return errors.New("correlation chain correlation_id is invalid")
	case len(chain.Entries) == 0:
		return errors.New("correlation chain entries are required")
	case chain.SafeToExecute || chain.ExecutesWork || chain.ApprovesWork ||
		chain.MutatesRepositories || chain.WidensPolicy || chain.PublishesArtifacts:
		return errors.New("correlation chain must not claim execution, approval, mutation, policy, or publication authority")
	}

	entries := make(map[string]CorrelationChainEntry, len(chain.Entries))
	locators := make(map[string]string, len(chain.Entries))
	artifactDocuments := make(map[string]map[string]any, len(chain.Entries))
	for _, entry := range chain.Entries {
		if err := validateCorrelationEntryShape(entry); err != nil {
			return fmt.Errorf("entry %q: %w", entry.Role, err)
		}
		if _, duplicate := entries[entry.Role]; duplicate {
			return fmt.Errorf("duplicate artifact role %q", entry.Role)
		}
		entries[entry.Role] = entry

		artifactPath, err := resolveCorrelationArtifactPath(entry, chainDir)
		if err != nil {
			return fmt.Errorf("entry %q: %w", entry.Role, err)
		}
		canonicalPath, body, err := readCanonicalCorrelationArtifact(artifactPath)
		if err != nil {
			return fmt.Errorf("entry %q: %w", entry.Role, err)
		}
		if entry.ArtifactPath != "" && entry.ArtifactPath != canonicalPath {
			return fmt.Errorf("entry %q artifact_path is not canonical", entry.Role)
		}
		if previousRole, duplicate := locators[canonicalPath]; duplicate {
			return fmt.Errorf("entries %q and %q use the same artifact", previousRole, entry.Role)
		}
		locators[canonicalPath] = entry.Role
		if actual := digestBytes(body); actual != entry.Digest {
			return fmt.Errorf("entry %q artifact digest mismatch: got %s want %s", entry.Role, actual, entry.Digest)
		}
		document, err := decodeJSONObject(body)
		if err != nil {
			return fmt.Errorf("entry %q artifact must be one JSON object: %w", entry.Role, err)
		}
		producer, err := correlationProducer(document)
		if err != nil {
			return fmt.Errorf("entry %q: %w", entry.Role, err)
		}
		if producer != entry.Producer {
			return fmt.Errorf("entry %q producer does not match artifact schema: got %q want %q", entry.Role, entry.Producer, producer)
		}
		if err := rejectArtifactIdentityMismatch(document, Record{
			MissionID:     chain.MissionID,
			CorrelationID: chain.CorrelationID,
		}); err != nil {
			return fmt.Errorf("entry %q: %w", entry.Role, err)
		}
		artifactDocuments[entry.Role] = document
	}

	drafts := make([]correlationArtifactDraft, 0, len(chain.Entries))
	for _, entry := range chain.Entries {
		drafts = append(drafts, correlationArtifactDraft{
			entry:    entry,
			document: artifactDocuments[entry.Role],
		})
	}
	record := Record{MissionID: chain.MissionID, CorrelationID: chain.CorrelationID}
	for _, draft := range drafts {
		derived, err := deriveCorrelationBinding(draft, drafts, record)
		if err != nil {
			return fmt.Errorf("entry %q: %w", draft.entry.Role, err)
		}
		if err := validateDerivedCorrelationBinding(draft.entry, derived); err != nil {
			return fmt.Errorf("entry %q: %w", draft.entry.Role, err)
		}
	}
	return validateCorrelationParentGraph(chainParentRoles(chain.Entries))
}

func validateCorrelationEntryShape(entry CorrelationChainEntry) error {
	switch {
	case !correlationRolePattern.MatchString(entry.Role):
		return errors.New("role is invalid")
	case (entry.ArtifactPath == "") == (entry.ArtifactName == ""):
		return errors.New("exactly one of artifact_path or artifact_name is required")
	case !validSHA256Digest(entry.Digest):
		return errors.New("digest must be lowercase sha256:<64 hex>")
	case !correlationRolePattern.MatchString(entry.Producer):
		return errors.New("producer is invalid")
	case entry.SafeToExecute || entry.ExecutesWork || entry.ApprovesWork ||
		entry.MutatesRepositories || entry.WidensPolicy || entry.PublishesArtifacts:
		return errors.New("entry must not claim execution, approval, mutation, policy, or publication authority")
	}
	switch entry.BindingMode {
	case CorrelationBindingNativeField:
		if strings.TrimSpace(entry.NativeIdentifier) == "" || !validCorrelationJSONPointer(entry.NativeField) {
			return errors.New("native_field binding requires native_identifier and native_field")
		}
		if entry.ParentRole != "" || entry.ParentDigest != "" || entry.ParentDigestField != "" {
			return errors.New("native_field binding must not include parent provenance")
		}
	case CorrelationBindingDigestLink:
		if !correlationRolePattern.MatchString(entry.ParentRole) ||
			!validSHA256Digest(entry.ParentDigest) ||
			!validCorrelationJSONPointer(entry.ParentDigestField) {
			return errors.New("digest_link binding requires parent_role, lowercase parent_digest, and parent_digest_field")
		}
		if entry.ParentRole == entry.Role {
			return errors.New("digest_link binding cannot reference itself")
		}
		if entry.NativeIdentifier != "" || entry.NativeField != "" {
			return errors.New("digest_link binding must not include native provenance")
		}
	default:
		return fmt.Errorf("binding_mode must be %q or %q", CorrelationBindingNativeField, CorrelationBindingDigestLink)
	}
	return nil
}

func readyCorrelationChainValidation(chain CorrelationChain) (CorrelationChainValidation, error) {
	digest, err := correlationChainDigest(chain)
	if err != nil {
		return CorrelationChainValidation{}, err
	}
	return CorrelationChainValidation{
		Schema:        CorrelationChainValidationSchema,
		Status:        "ready",
		MissionID:     chain.MissionID,
		CorrelationID: chain.CorrelationID,
		ChainDigest:   digest,
		ArtifactCount: len(chain.Entries),
	}, nil
}

func blockedCorrelationChainValidation(chain CorrelationChain, cause error) (CorrelationChainValidation, error) {
	return CorrelationChainValidation{
		Schema:        CorrelationChainValidationSchema,
		Status:        "blocked",
		MissionID:     chain.MissionID,
		CorrelationID: chain.CorrelationID,
		ArtifactCount: len(chain.Entries),
	}, cause
}

func correlationChainDigest(chain CorrelationChain) (string, error) {
	body, err := json.Marshal(chain)
	if err != nil {
		return "", err
	}
	return digestBytes(body), nil
}

func WriteCorrelationChainFile(path string, chain CorrelationChain) error {
	return writeCorrelationChainFileWithCreate(path, chain, openExclusiveCorrelationOutput)
}

func writeCorrelationChainFileWithCreate(
	path string,
	chain CorrelationChain,
	create func(string) (*os.File, error),
) error {
	outputPath, err := canonicalCorrelationOutputPath(path)
	if err != nil {
		return err
	}
	for _, entry := range chain.Entries {
		artifactPath := entry.ArtifactPath
		if artifactPath == "" {
			artifactPath = filepath.Join(filepath.Dir(outputPath), entry.ArtifactName)
		}
		canonicalArtifactPath, err := filepath.EvalSymlinks(artifactPath)
		if err != nil {
			return err
		}
		canonicalArtifactPath, err = filepath.Abs(canonicalArtifactPath)
		if err != nil {
			return err
		}
		if filepath.Clean(canonicalArtifactPath) == outputPath {
			return fmt.Errorf("correlation chain output must not overwrite artifact role %q", entry.Role)
		}
	}
	body, err := json.MarshalIndent(chain, "", "  ")
	if err != nil {
		return err
	}
	file, err := create(outputPath)
	if err != nil {
		return fmt.Errorf("create correlation chain output exclusively: %w", err)
	}
	if _, err := file.Write(append(body, '\n')); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func openExclusiveCorrelationOutput(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
}

func canonicalCorrelationOutputPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("correlation chain output path is required")
	}
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	absolutePath = filepath.Clean(absolutePath)
	_, err = os.Lstat(absolutePath)
	switch {
	case err == nil:
		return "", errors.New("correlation chain output already exists")
	case !os.IsNotExist(err):
		return "", err
	}
	parentPath, err := filepath.EvalSymlinks(filepath.Dir(absolutePath))
	if err != nil {
		return "", err
	}
	parentInfo, err := os.Stat(parentPath)
	if err != nil {
		return "", err
	}
	if !parentInfo.IsDir() {
		return "", errors.New("correlation chain output parent must be a directory")
	}
	return filepath.Join(parentPath, filepath.Base(absolutePath)), nil
}

func correlationChainReference(chain CorrelationChain, chainDigest string, chainDirs ...string) CorrelationChainReference {
	reference := CorrelationChainReference{
		Schema:        CorrelationChainReferenceSchema,
		MissionID:     chain.MissionID,
		CorrelationID: chain.CorrelationID,
		ChainDigest:   chainDigest,
		Entries:       make([]CorrelationChainReferenceEntry, 0, len(chain.Entries)),
	}
	for _, entry := range chain.Entries {
		artifactPath := entry.ArtifactPath
		if artifactPath == "" && len(chainDirs) > 0 {
			artifactPath = filepath.Join(chainDirs[0], entry.ArtifactName)
		}
		if artifactPath != "" {
			artifactPath = filepath.Clean(artifactPath)
		}
		reference.Entries = append(reference.Entries, CorrelationChainReferenceEntry{
			Role:                entry.Role,
			Digest:              entry.Digest,
			LocatorState:        correlationLocatorStateLive,
			LocatorDigest:       correlationLiveLocatorDigest(entry.Role, entry.Digest, artifactPath),
			Producer:            entry.Producer,
			BindingMode:         entry.BindingMode,
			NativeIdentifier:    entry.NativeIdentifier,
			NativeField:         entry.NativeField,
			ParentRole:          entry.ParentRole,
			ParentDigest:        entry.ParentDigest,
			ParentDigestField:   entry.ParentDigestField,
			SafeToExecute:       entry.SafeToExecute,
			ExecutesWork:        entry.ExecutesWork,
			ApprovesWork:        entry.ApprovesWork,
			MutatesRepositories: entry.MutatesRepositories,
			WidensPolicy:        entry.WidensPolicy,
			PublishesArtifacts:  entry.PublishesArtifacts,
		})
	}
	reference.ReferenceDigest = correlationReferenceDigest(reference)
	return reference
}

func correlationReferenceDigest(reference CorrelationChainReference) string {
	reference.ReferenceDigest = ""
	body, _ := json.Marshal(reference)
	return digestBytes(body)
}

func correlationChainEntry(chain CorrelationChain, role string) (CorrelationChainEntry, bool) {
	for _, entry := range chain.Entries {
		if entry.Role == role {
			return entry, true
		}
	}
	return CorrelationChainEntry{}, false
}

func recordCorrelationChainImport(record *Record, reference CorrelationChainReference, binding CorrelatedImportBinding) error {
	for _, existing := range record.CorrelatedImports {
		if existing.Role == binding.Role {
			return fmt.Errorf("correlation-bound role %q was already imported", binding.Role)
		}
	}
	foundReference := false
	for _, existing := range record.CorrelationChainReferences {
		if existing.ChainDigest == reference.ChainDigest {
			foundReference = true
			break
		}
	}
	if !foundReference {
		record.CorrelationChainReferences = append(record.CorrelationChainReferences, reference)
	}
	for i := range record.CorrelatedImports {
		entry, covered := correlationReferenceEntry(reference, record.CorrelatedImports[i].Role)
		if !covered || entry.Digest != record.CorrelatedImports[i].Digest {
			continue
		}
		record.CorrelatedImports[i].ChainDigest = reference.ChainDigest
		record.CorrelatedImports[i].ReferenceDigest = reference.ReferenceDigest
		record.CorrelatedImports[i].LocatorState = entry.LocatorState
		record.CorrelatedImports[i].LocatorDigest = entry.LocatorDigest
		record.CorrelatedImports[i].ArchiveSourceLocatorDigest = entry.ArchiveSourceLocatorDigest
	}
	entry, covered := correlationReferenceEntry(reference, binding.Role)
	if !covered {
		return fmt.Errorf("correlation-bound role %q is absent from its reference", binding.Role)
	}
	binding.LocatorState = entry.LocatorState
	binding.LocatorDigest = entry.LocatorDigest
	binding.ArchiveSourceLocatorDigest = entry.ArchiveSourceLocatorDigest
	record.CorrelatedImports = append(record.CorrelatedImports, binding)
	usedReferenceDigests := make(map[string]struct{}, len(record.CorrelatedImports))
	for _, correlatedImport := range record.CorrelatedImports {
		usedReferenceDigests[correlatedImport.ReferenceDigest] = struct{}{}
	}
	references := record.CorrelationChainReferences[:0]
	for _, existing := range record.CorrelationChainReferences {
		if _, used := usedReferenceDigests[existing.ReferenceDigest]; used {
			references = append(references, existing)
		}
	}
	record.CorrelationChainReferences = references
	return validateRecordCorrelationState(*record)
}

func validateRecordCorrelationState(record Record) error {
	if len(record.CorrelationChainReferences) == 0 && len(record.CorrelatedImports) == 0 {
		return nil
	}
	if record.MissionID == "" || record.CorrelationID == "" {
		return errors.New("correlation chain state requires Mission identity")
	}
	references := make(map[string]CorrelationChainReference, len(record.CorrelationChainReferences))
	referencesByDigest := make(map[string]CorrelationChainReference, len(record.CorrelationChainReferences))
	for _, reference := range record.CorrelationChainReferences {
		if err := validateCorrelationChainReference(reference, record); err != nil {
			return err
		}
		if _, duplicate := references[reference.ChainDigest]; duplicate {
			return fmt.Errorf("duplicate correlation chain reference %q", reference.ChainDigest)
		}
		references[reference.ChainDigest] = reference
		if _, duplicate := referencesByDigest[reference.ReferenceDigest]; duplicate {
			return fmt.Errorf("duplicate correlation reference_digest %q", reference.ReferenceDigest)
		}
		referencesByDigest[reference.ReferenceDigest] = reference
	}
	importedRoles := make(map[string]struct{}, len(record.CorrelatedImports))
	usedReferences := make(map[string]struct{}, len(record.CorrelationChainReferences))
	for _, binding := range record.CorrelatedImports {
		switch {
		case !correlationRolePattern.MatchString(binding.Role):
			return fmt.Errorf("correlated import role %q is invalid", binding.Role)
		case !validSHA256Digest(binding.Digest):
			return fmt.Errorf("correlated import %q digest is invalid", binding.Role)
		case strings.TrimSpace(binding.ArtifactPath) == "":
			return fmt.Errorf("correlated import %q artifact_path is required", binding.Role)
		case !validSHA256Digest(binding.LocatorDigest):
			return fmt.Errorf("correlated import %q locator_digest is invalid", binding.Role)
		case binding.LocatorDigest != correlatedImportLocatorDigest(binding):
			return fmt.Errorf("correlated import %q locator integrity mismatch", binding.Role)
		case !validSHA256Digest(binding.ChainDigest):
			return fmt.Errorf("correlated import %q chain_digest is invalid", binding.Role)
		case !validSHA256Digest(binding.ReferenceDigest):
			return fmt.Errorf("correlated import %q reference_digest is invalid", binding.Role)
		}
		switch binding.LocatorState {
		case correlationLocatorStateLive:
			if binding.ArtifactPath == correlationRedactedPathSentinel ||
				binding.ArchiveSourceLocatorDigest != "" {
				return fmt.Errorf("correlated import %q live locator state is invalid", binding.Role)
			}
		case correlationLocatorStateArchiveRedacted:
			if binding.ArtifactPath != correlationRedactedPathSentinel ||
				!validSHA256Digest(binding.ArchiveSourceLocatorDigest) {
				return fmt.Errorf("correlated import %q archive locator restoration provenance is invalid", binding.Role)
			}
		default:
			return fmt.Errorf("correlated import %q locator_state is invalid", binding.Role)
		}
		if _, duplicate := importedRoles[binding.Role]; duplicate {
			return fmt.Errorf("duplicate correlated import role %q", binding.Role)
		}
		importedRoles[binding.Role] = struct{}{}
		reference, ok := referencesByDigest[binding.ReferenceDigest]
		if !ok || reference.ChainDigest != binding.ChainDigest {
			return fmt.Errorf("correlated import %q references a missing chain", binding.Role)
		}
		entry, ok := correlationReferenceEntry(reference, binding.Role)
		if !ok || entry.Digest != binding.Digest {
			return fmt.Errorf("correlated import %q is not digest-bound in its chain reference", binding.Role)
		}
		if entry.LocatorState != binding.LocatorState ||
			entry.LocatorDigest != binding.LocatorDigest ||
			entry.ArchiveSourceLocatorDigest != binding.ArchiveSourceLocatorDigest {
			return fmt.Errorf("correlated import %q locator does not match its chain reference commitment", binding.Role)
		}
		usedReferences[binding.ReferenceDigest] = struct{}{}
	}
	for digest := range referencesByDigest {
		if _, used := usedReferences[digest]; !used {
			return fmt.Errorf("correlation reference_digest %q is not bound to an import", digest)
		}
	}
	return nil
}

func validateCorrelationChainReference(reference CorrelationChainReference, record Record) error {
	switch {
	case reference.Schema != CorrelationChainReferenceSchema:
		return fmt.Errorf("correlation chain reference schema must be %s", CorrelationChainReferenceSchema)
	case reference.MissionID != record.MissionID:
		return errors.New("correlation chain reference mission_id does not match Mission record")
	case reference.CorrelationID != record.CorrelationID:
		return errors.New("correlation chain reference correlation_id does not match Mission record")
	case !validSHA256Digest(reference.ChainDigest):
		return errors.New("correlation chain reference chain_digest is invalid")
	case !validSHA256Digest(reference.ReferenceDigest):
		return errors.New("correlation chain reference reference_digest is invalid")
	case correlationReferenceDigest(reference) != reference.ReferenceDigest:
		return errors.New("correlation chain reference reference_digest mismatch")
	case len(reference.Entries) == 0:
		return errors.New("correlation chain reference entries are required")
	case reference.SafeToExecute || reference.ExecutesWork || reference.ApprovesWork ||
		reference.MutatesRepositories || reference.WidensPolicy || reference.PublishesArtifacts:
		return errors.New("correlation chain reference must not widen authority")
	}
	entries := make(map[string]CorrelationChainReferenceEntry, len(reference.Entries))
	for _, entry := range reference.Entries {
		switch {
		case !correlationRolePattern.MatchString(entry.Role):
			return fmt.Errorf("correlation chain reference role %q is invalid", entry.Role)
		case !validSHA256Digest(entry.Digest):
			return fmt.Errorf("correlation chain reference role %q digest is invalid", entry.Role)
		case !validSHA256Digest(entry.LocatorDigest):
			return fmt.Errorf("correlation chain reference role %q locator_digest is invalid", entry.Role)
		case !correlationRolePattern.MatchString(entry.Producer):
			return fmt.Errorf("correlation chain reference role %q producer is invalid", entry.Role)
		case entry.SafeToExecute || entry.ExecutesWork || entry.ApprovesWork ||
			entry.MutatesRepositories || entry.WidensPolicy || entry.PublishesArtifacts:
			return fmt.Errorf("correlation chain reference role %q widens authority", entry.Role)
		}
		switch entry.LocatorState {
		case correlationLocatorStateLive:
			if entry.ArchiveSourceLocatorDigest != "" {
				return fmt.Errorf("correlation chain reference role %q live locator state is invalid", entry.Role)
			}
		case correlationLocatorStateArchiveRedacted:
			if !validSHA256Digest(entry.ArchiveSourceLocatorDigest) {
				return fmt.Errorf("correlation chain reference role %q archive locator state is invalid", entry.Role)
			}
		default:
			return fmt.Errorf("correlation chain reference role %q locator_state is invalid", entry.Role)
		}
		if _, duplicate := entries[entry.Role]; duplicate {
			return fmt.Errorf("duplicate correlation chain reference role %q", entry.Role)
		}
		switch entry.BindingMode {
		case CorrelationBindingNativeField:
			if entry.NativeIdentifier == "" || !validCorrelationJSONPointer(entry.NativeField) ||
				entry.ParentRole != "" || entry.ParentDigest != "" || entry.ParentDigestField != "" {
				return fmt.Errorf("correlation chain reference role %q has invalid native provenance", entry.Role)
			}
		case CorrelationBindingDigestLink:
			if !correlationRolePattern.MatchString(entry.ParentRole) ||
				!validSHA256Digest(entry.ParentDigest) ||
				!validCorrelationJSONPointer(entry.ParentDigestField) ||
				entry.ParentRole == entry.Role ||
				entry.NativeIdentifier != "" || entry.NativeField != "" {
				return fmt.Errorf("correlation chain reference role %q has invalid digest provenance", entry.Role)
			}
		default:
			return fmt.Errorf("correlation chain reference role %q has invalid binding_mode", entry.Role)
		}
		entries[entry.Role] = entry
	}
	for _, entry := range reference.Entries {
		if entry.BindingMode != CorrelationBindingDigestLink {
			continue
		}
		parent, ok := entries[entry.ParentRole]
		if !ok || parent.Digest != entry.ParentDigest {
			return fmt.Errorf("correlation chain reference role %q has invalid parent link", entry.Role)
		}
	}
	parentRoles := make(map[string]string, len(reference.Entries))
	for _, entry := range reference.Entries {
		parentRoles[entry.Role] = entry.ParentRole
	}
	return validateCorrelationParentGraph(parentRoles)
}

func correlationReferenceEntry(reference CorrelationChainReference, role string) (CorrelationChainReferenceEntry, bool) {
	for _, entry := range reference.Entries {
		if entry.Role == role {
			return entry, true
		}
	}
	return CorrelationChainReferenceEntry{}, false
}

func correlationReferenceComplete(reference CorrelationChainReference, imports []CorrelatedImportBinding) bool {
	for _, binding := range imports {
		entry, ok := correlationReferenceEntry(reference, binding.Role)
		if !ok || entry.Digest != binding.Digest {
			return false
		}
	}
	return true
}

func bindArchiveCorrelationLocators(live Record, archived *Record) {
	for referenceIndex := range archived.CorrelationChainReferences {
		reference := &archived.CorrelationChainReferences[referenceIndex]
		originalReferenceDigest := reference.ReferenceDigest
		for entryIndex := range reference.Entries {
			entry := &reference.Entries[entryIndex]
			for bindingIndex := range archived.CorrelatedImports {
				binding := &archived.CorrelatedImports[bindingIndex]
				if binding.ReferenceDigest != originalReferenceDigest ||
					binding.Role != entry.Role ||
					binding.ArtifactPath != correlationRedactedPathSentinel {
					continue
				}
				var liveBinding *CorrelatedImportBinding
				for index := range live.CorrelatedImports {
					candidate := &live.CorrelatedImports[index]
					if candidate.ReferenceDigest == originalReferenceDigest &&
						candidate.Role == binding.Role {
						liveBinding = candidate
						break
					}
				}
				if liveBinding == nil || liveBinding.LocatorState != correlationLocatorStateLive {
					continue
				}
				binding.LocatorState = correlationLocatorStateArchiveRedacted
				binding.ArchiveSourceLocatorDigest = liveBinding.LocatorDigest
				binding.LocatorDigest = correlatedImportLocatorDigest(*binding)
				entry.LocatorState = binding.LocatorState
				entry.ArchiveSourceLocatorDigest = binding.ArchiveSourceLocatorDigest
				entry.LocatorDigest = binding.LocatorDigest
			}
		}
		reference.ReferenceDigest = correlationReferenceDigest(*reference)
		for bindingIndex := range archived.CorrelatedImports {
			if archived.CorrelatedImports[bindingIndex].ReferenceDigest == originalReferenceDigest {
				archived.CorrelatedImports[bindingIndex].ReferenceDigest = reference.ReferenceDigest
			}
		}
	}
}

func validateCorrelationReferenceCurrentArtifacts(reference CorrelationChainReference, imports []CorrelatedImportBinding) error {
	return validateCorrelationReferenceCurrentArtifactsWithChain(reference, imports, nil, "")
}

func validateCorrelationReferenceCurrentArtifactsWithChain(
	reference CorrelationChainReference,
	imports []CorrelatedImportBinding,
	chain *CorrelationChain,
	chainDir string,
) error {
	for _, binding := range imports {
		entry, ok := correlationReferenceEntry(reference, binding.Role)
		if !ok || entry.Digest != binding.Digest {
			return fmt.Errorf("complete correlation chain is missing role %q digest %s", binding.Role, binding.Digest)
		}
		artifactPath := binding.ArtifactPath
		if binding.LocatorState == correlationLocatorStateArchiveRedacted {
			if chain == nil ||
				binding.ChainDigest != reference.ChainDigest ||
				binding.ReferenceDigest != reference.ReferenceDigest {
				return fmt.Errorf("correlation-bound import %q redacted locator does not match supplied chain and reference digests", binding.Role)
			}
			chainEntry, present := correlationChainEntry(*chain, binding.Role)
			if !present || chainEntry.Digest != binding.Digest {
				return fmt.Errorf("correlation-bound import %q redacted locator is absent from supplied chain", binding.Role)
			}
			callerReference := correlationChainReference(*chain, reference.ChainDigest, chainDir)
			callerEntry, present := correlationReferenceEntry(callerReference, binding.Role)
			if !present ||
				callerEntry.LocatorState != correlationLocatorStateLive ||
				callerEntry.LocatorDigest != binding.ArchiveSourceLocatorDigest ||
				entry.ArchiveSourceLocatorDigest != binding.ArchiveSourceLocatorDigest {
				return fmt.Errorf("correlation-bound import %q redacted locator does not match its committed live source", binding.Role)
			}
			var err error
			artifactPath, err = resolveCorrelationArtifactPath(chainEntry, chainDir)
			if err != nil {
				return fmt.Errorf("correlation-bound import %q redacted locator: %w", binding.Role, err)
			}
		} else if chain != nil {
			chainEntry, present := correlationChainEntry(*chain, binding.Role)
			if !present || chainEntry.Digest != binding.Digest {
				return fmt.Errorf("correlation-bound import %q live locator is absent from supplied chain", binding.Role)
			}
			chainPath, err := resolveCorrelationArtifactPath(chainEntry, chainDir)
			if err != nil {
				return fmt.Errorf("correlation-bound import %q live locator: %w", binding.Role, err)
			}
			canonicalChainPath, _, err := readCanonicalCorrelationArtifact(chainPath)
			if err != nil {
				return fmt.Errorf("correlation-bound import %q live locator: %w", binding.Role, err)
			}
			if canonicalChainPath != binding.ArtifactPath {
				return fmt.Errorf("correlation-bound import %q live locator does not match supplied chain", binding.Role)
			}
		}
		_, body, err := readCanonicalCorrelationArtifact(artifactPath)
		if err != nil {
			return fmt.Errorf("correlation-bound import %q: %w", binding.Role, err)
		}
		if actual := digestBytes(body); actual != binding.Digest {
			return fmt.Errorf("correlation-bound import %q digest mismatch: got %s want %s", binding.Role, actual, binding.Digest)
		}
		if entry.BindingMode != CorrelationBindingNativeField {
			document, err := decodeJSONObject(body)
			if err != nil {
				return fmt.Errorf("correlation-bound import %q digest-link artifact is invalid: %w", binding.Role, err)
			}
			value, present := correlationStringField(document, entry.ParentDigestField)
			normalized, valid := normalizeSHA256Digest(value)
			if !present || !valid || normalized != entry.ParentDigest {
				return fmt.Errorf("correlation-bound import %q parent provenance changed", binding.Role)
			}
			continue
		}
		document, err := decodeJSONObject(body)
		if err != nil {
			return fmt.Errorf("correlation-bound import %q native artifact is invalid: %w", binding.Role, err)
		}
		value, present := correlationStringField(document, entry.NativeField)
		identifier, valid := normalizeNativeIdentifier(entry.NativeField, value)
		if !present || !valid || identifier != entry.NativeIdentifier {
			return fmt.Errorf("correlation-bound import %q native provenance changed", binding.Role)
		}
	}
	return nil
}

func correlatedImportLocatorDigest(binding CorrelatedImportBinding) string {
	if binding.LocatorState == correlationLocatorStateLive {
		return correlationLiveLocatorDigest(
			binding.Role,
			binding.Digest,
			binding.ArtifactPath,
		)
	}
	body, _ := json.Marshal(struct {
		Role                       string `json:"role"`
		Digest                     string `json:"digest"`
		ArtifactPath               string `json:"artifact_path"`
		LocatorState               string `json:"locator_state"`
		ArchiveSourceLocatorDigest string `json:"archive_source_locator_digest,omitempty"`
		ChainDigest                string `json:"chain_digest"`
	}{
		Role:                       binding.Role,
		Digest:                     binding.Digest,
		ArtifactPath:               binding.ArtifactPath,
		LocatorState:               binding.LocatorState,
		ArchiveSourceLocatorDigest: binding.ArchiveSourceLocatorDigest,
		ChainDigest:                binding.ChainDigest,
	})
	return digestBytes(body)
}

func correlationLiveLocatorDigest(role, digest, artifactPath string) string {
	body, _ := json.Marshal(struct {
		Role         string `json:"role"`
		Digest       string `json:"digest"`
		ArtifactPath string `json:"artifact_path"`
		LocatorState string `json:"locator_state"`
	}{
		Role:         role,
		Digest:       digest,
		ArtifactPath: artifactPath,
		LocatorState: correlationLocatorStateLive,
	})
	return digestBytes(body)
}

func chainParentRoles(entries []CorrelationChainEntry) map[string]string {
	parentRoles := make(map[string]string, len(entries))
	for _, entry := range entries {
		parentRoles[entry.Role] = entry.ParentRole
	}
	return parentRoles
}

func validateCorrelationParentGraph(parentRoles map[string]string) error {
	const (
		visiting = 1
		visited  = 2
	)
	states := make(map[string]int, len(parentRoles))
	var visit func(string) error
	visit = func(role string) error {
		switch states[role] {
		case visiting:
			return fmt.Errorf("correlation chain parent cycle includes role %q", role)
		case visited:
			return nil
		}
		states[role] = visiting
		if parentRole := parentRoles[role]; parentRole != "" {
			if err := visit(parentRole); err != nil {
				return err
			}
		}
		states[role] = visited
		return nil
	}
	for role := range parentRoles {
		if err := visit(role); err != nil {
			return err
		}
	}
	return nil
}

func readCanonicalCorrelationArtifact(path string) (string, []byte, error) {
	return readCanonicalCorrelationArtifactWithOpen(path, openCorrelationInput)
}

func readCanonicalCorrelationArtifactWithOpen(
	path string,
	open func(string) (*os.File, error),
) (string, []byte, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", nil, errors.New("artifact path is required")
	}
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return "", nil, err
	}
	absolutePath = filepath.Clean(absolutePath)

	file, err := open(absolutePath)
	if err != nil {
		return "", nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return "", nil, err
	}
	if !opened.Mode().IsRegular() {
		return "", nil, errors.New("artifact must be a regular file")
	}
	if opened.Size() > maxCorrelationArtifactBytes {
		return "", nil, fmt.Errorf("artifact exceeds %d-byte size limit", maxCorrelationArtifactBytes)
	}

	canonicalPath, err := filepath.EvalSymlinks(absolutePath)
	if err != nil {
		return "", nil, err
	}
	canonicalPath, err = filepath.Abs(canonicalPath)
	if err != nil {
		return "", nil, err
	}
	canonicalPath = filepath.Clean(canonicalPath)
	pathBeforeRead, err := os.Lstat(canonicalPath)
	if err != nil {
		return "", nil, err
	}
	if pathBeforeRead.Mode()&os.ModeSymlink != 0 ||
		!pathBeforeRead.Mode().IsRegular() ||
		!os.SameFile(opened, pathBeforeRead) {
		return "", nil, errors.New("artifact changed before read")
	}
	body, err := io.ReadAll(io.LimitReader(file, maxCorrelationArtifactBytes+1))
	if err != nil {
		return "", nil, err
	}
	if int64(len(body)) > maxCorrelationArtifactBytes {
		return "", nil, fmt.Errorf("artifact exceeds %d-byte size limit", maxCorrelationArtifactBytes)
	}
	afterRead, err := file.Stat()
	if err != nil {
		return "", nil, err
	}
	pathAfterRead, err := os.Lstat(canonicalPath)
	if err != nil {
		return "", nil, err
	}
	if !afterRead.Mode().IsRegular() || afterRead.Size() > maxCorrelationArtifactBytes ||
		afterRead.Size() != int64(len(body)) || !pathAfterRead.Mode().IsRegular() ||
		!os.SameFile(opened, afterRead) || !os.SameFile(opened, pathAfterRead) {
		return "", nil, errors.New("artifact changed while reading")
	}
	return canonicalPath, body, nil
}

func resolveCorrelationArtifactPath(entry CorrelationChainEntry, chainDir string) (string, error) {
	if entry.ArtifactPath != "" {
		return entry.ArtifactPath, nil
	}
	if filepath.Base(entry.ArtifactName) != entry.ArtifactName ||
		entry.ArtifactName == "." || entry.ArtifactName == ".." {
		return "", errors.New("artifact_name must be an unambiguous file name")
	}
	return filepath.Join(chainDir, entry.ArtifactName), nil
}

func decodeJSONObject(body []byte) (map[string]any, error) {
	value, err := decodeExactJSON(body)
	if err != nil {
		return nil, err
	}
	document, ok := value.(map[string]any)
	if !ok || document == nil {
		return nil, errors.New("JSON object is required")
	}
	return document, nil
}

func decodeStrictJSONObject(
	body []byte,
	target any,
	name string,
	fields map[string]string,
	required []string,
) error {
	value, err := decodeExactJSON(body)
	if err != nil {
		return err
	}
	document, ok := value.(map[string]any)
	if !ok || document == nil {
		return fmt.Errorf("%s must be a JSON object", name)
	}
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
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return fmt.Errorf("%s contains trailing JSON", name)
		}
		return err
	}
	return nil
}

func strictJSONTypeMatches(value any, want string) bool {
	matched := false
	for _, allowed := range strings.Split(want, "|") {
		switch allowed {
		case "null":
			if value == nil {
				matched = true
			}
		case "string":
			_, ok := value.(string)
			if ok {
				matched = true
			}
		case "boolean":
			_, ok := value.(bool)
			if ok {
				matched = true
			}
		case "object":
			_, ok := value.(map[string]any)
			if ok {
				matched = true
			}
		case "array":
			_, ok := value.([]any)
			if ok {
				matched = true
			}
		case "integer":
			number, ok := value.(json.Number)
			if ok {
				if _, err := number.Int64(); err == nil {
					matched = true
				}
			}
		default:
			return false
		}
	}
	return matched
}

func decodeExactJSON(body []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	value, err := decodeExactJSONValue(decoder, "$")
	if err != nil {
		return nil, err
	}
	if token, err := decoder.Token(); err != io.EOF {
		if err == nil {
			_ = token
			return nil, errors.New("trailing JSON is not allowed")
		}
		return nil, err
	}
	return value, nil
}

func decodeExactJSONValue(decoder *json.Decoder, path string) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return token, nil
	}
	switch delimiter {
	case '{':
		object := make(map[string]any)
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			key, ok := keyToken.(string)
			if !ok {
				return nil, fmt.Errorf("%s object key must be a string", path)
			}
			if _, duplicate := object[key]; duplicate {
				return nil, fmt.Errorf("%s contains duplicate key %q", path, key)
			}
			if canonical, contractField := canonicalCorrelationContractField(key); contractField && key != canonical {
				return nil, fmt.Errorf("%s contract field %q must use exact lowercase spelling %q", path, key, canonical)
			}
			child, err := decodeExactJSONValue(decoder, path+"."+key)
			if err != nil {
				return nil, err
			}
			object[key] = child
		}
		if _, err := decoder.Token(); err != nil {
			return nil, err
		}
		return object, nil
	case '[':
		array := make([]any, 0)
		for decoder.More() {
			child, err := decodeExactJSONValue(decoder, fmt.Sprintf("%s[%d]", path, len(array)))
			if err != nil {
				return nil, err
			}
			array = append(array, child)
		}
		if _, err := decoder.Token(); err != nil {
			return nil, err
		}
		return array, nil
	default:
		return nil, fmt.Errorf("%s contains unexpected JSON delimiter %q", path, delimiter)
	}
}

func canonicalCorrelationContractField(field string) (string, bool) {
	canonical := strings.ToLower(field)
	if _, known := correlationContractFields[canonical]; known {
		return canonical, true
	}
	if isDigestBearingField(canonical) || isNativeIdentifierField(canonical) {
		return canonical, true
	}
	return "", false
}

func correlationProducer(document map[string]any) (string, error) {
	_, schema, err := correlationSchemaIdentifier(document)
	if err != nil {
		return "", err
	}
	parts := strings.Split(schema, ".")
	var producer string
	if len(parts) >= 2 && parts[0] == "ao" {
		producer = "ao-" + parts[1]
	} else {
		producer = parts[0]
	}
	if !correlationRolePattern.MatchString(producer) {
		return "", fmt.Errorf("artifact schema %q does not identify a valid producer", schema)
	}
	return producer, nil
}

func correlationSchemaIdentifier(document map[string]any) (string, string, error) {
	candidates := make([]correlationFieldCandidate, 0, 3)
	for _, field := range []string{"schema", "schema_version", "contract_version"} {
		value, present := document[field]
		if !present {
			continue
		}
		schema, ok := value.(string)
		schema = strings.TrimSpace(schema)
		if !ok || schema == "" {
			return "", "", fmt.Errorf("artifact %s must be a nonempty string", field)
		}
		candidates = append(candidates, correlationFieldCandidate{Path: field, Field: field, Value: schema})
	}
	switch len(candidates) {
	case 0:
		return "", "", errors.New("artifact schema, schema_version, or contract_version is required to identify producer")
	case 1:
		return candidates[0].Path, candidates[0].Value, nil
	default:
		return "", "", errors.New("artifact producer schema key is ambiguous")
	}
}

func rejectArtifactIdentityMismatch(document map[string]any, record Record) error {
	return rejectNestedArtifactIdentityMismatch(document, "$", record)
}

func rejectNestedArtifactIdentityMismatch(value any, path string, record Record) error {
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			childPath := path + "." + key
			var want string
			switch key {
			case "mission_id":
				want = record.MissionID
			case "correlation_id":
				want = record.CorrelationID
			default:
				if err := rejectNestedArtifactIdentityMismatch(typed[key], childPath, record); err != nil {
					return err
				}
				continue
			}
			identifier, ok := typed[key].(string)
			if !ok || strings.TrimSpace(identifier) == "" {
				return fmt.Errorf("%s must be a nonempty string", childPath)
			}
			if identifier != want {
				return fmt.Errorf("%s does not match Mission record", childPath)
			}
		}
	case []any:
		for i, child := range typed {
			if err := rejectNestedArtifactIdentityMismatch(
				child,
				fmt.Sprintf("%s[%d]", path, i),
				record,
			); err != nil {
				return err
			}
		}
	}
	return nil
}

type correlationFieldCandidate struct {
	Path  string
	Field string
	Value string
}

type correlationBinding struct {
	mode              string
	nativeIdentifier  string
	nativeField       string
	parentRole        string
	parentDigest      string
	parentDigestField string
}

func validateAO2EvidencePackRunIdentity(
	document map[string]any,
) (correlationFieldCandidate, bool, error) {
	_, schema, err := correlationSchemaIdentifier(document)
	if err != nil {
		return correlationFieldCandidate{}, false, err
	}
	if schema != ao2EvidencePackSchema {
		return correlationFieldCandidate{}, false, nil
	}

	raw, found := document["run_id"]
	if !found {
		return correlationFieldCandidate{}, true, errors.New(
			"AO2 evidence pack requires a top-level run_id",
		)
	}
	value, ok := raw.(string)
	canonical, valid := normalizeNativeIdentifier("run_id", value)
	if !ok || !valid {
		return correlationFieldCandidate{}, true, errors.New(
			"AO2 evidence pack run_id \"/run_id\" must be a valid string",
		)
	}
	if err := validateAO2RunIDValues(document, "", canonical); err != nil {
		return correlationFieldCandidate{}, true, err
	}
	return correlationFieldCandidate{Path: "/run_id", Field: "run_id", Value: canonical}, true, nil
}

func validateAO2RunIDValues(value any, path, canonical string) error {
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			childPath := appendCorrelationJSONPointer(path, key)
			if key == "run_id" {
				runID, ok := typed[key].(string)
				normalized, valid := normalizeNativeIdentifier("run_id", runID)
				if !ok || !valid {
					return fmt.Errorf("AO2 evidence pack run_id %q must be a valid string", childPath)
				}
				if normalized != canonical {
					return fmt.Errorf("AO2 evidence pack run_id %q conflicts with %q", childPath, "/run_id")
				}
				continue
			}
			if err := validateAO2RunIDValues(typed[key], childPath, canonical); err != nil {
				return err
			}
		}
	case []any:
		for i, child := range typed {
			if err := validateAO2RunIDValues(
				child,
				appendCorrelationJSONPointer(path, fmt.Sprintf("%d", i)),
				canonical,
			); err != nil {
				return err
			}
		}
	}
	return nil
}

func deriveCorrelationBinding(
	current correlationArtifactDraft,
	drafts []correlationArtifactDraft,
	record Record,
) (correlationBinding, error) {
	// AO2 run identity is mandatory artifact validity even when a digest link
	// subsequently takes precedence as the correlation binding.
	ao2RunID, isAO2EvidencePack, err := validateAO2EvidencePackRunIdentity(current.document)
	if err != nil {
		return correlationBinding{}, err
	}

	parentCandidates := digestLinkedParents(current, drafts)
	if len(parentCandidates) > 1 {
		return correlationBinding{}, errors.New("ambiguous multiple candidate parent links")
	}
	if len(parentCandidates) == 1 {
		return correlationBinding{
			mode:              CorrelationBindingDigestLink,
			parentRole:        parentCandidates[0].parentRole,
			parentDigest:      parentCandidates[0].parentDigest,
			parentDigestField: parentCandidates[0].parentDigestField,
		}, nil
	}

	fields := correlationStringFields(current.document)
	candidates := make([]correlationFieldCandidate, 0)
	for _, candidate := range fields {
		if !isNativeIdentifierField(candidate.Field) {
			continue
		}
		identifier, valid := normalizeNativeIdentifier(candidate.Field, candidate.Value)
		if !valid {
			return correlationBinding{}, fmt.Errorf("native identifier field %q is invalid", candidate.Path)
		}
		candidate.Value = identifier
		candidates = append(candidates, candidate)
	}
	if isAO2EvidencePack {
		return correlationBinding{
			mode:             CorrelationBindingNativeField,
			nativeField:      ao2RunID.Path,
			nativeIdentifier: ao2RunID.Value,
		}, nil
	}
	if len(candidates) > 1 {
		return correlationBinding{}, errors.New("ambiguous multiple candidate native identifiers")
	}
	if len(candidates) == 1 {
		return correlationBinding{
			mode:             CorrelationBindingNativeField,
			nativeField:      candidates[0].Path,
			nativeIdentifier: candidates[0].Value,
		}, nil
	}

	return correlationBinding{}, errors.New(
		"artifact requires real native instance provenance or an exact digest link",
	)
}

func applyCorrelationBinding(entry *CorrelationChainEntry, binding correlationBinding) {
	entry.BindingMode = binding.mode
	entry.NativeIdentifier = binding.nativeIdentifier
	entry.NativeField = binding.nativeField
	entry.ParentRole = binding.parentRole
	entry.ParentDigest = binding.parentDigest
	entry.ParentDigestField = binding.parentDigestField
}

func validateDerivedCorrelationBinding(entry CorrelationChainEntry, derived correlationBinding) error {
	switch entry.BindingMode {
	case CorrelationBindingNativeField:
		if derived.mode != CorrelationBindingNativeField ||
			entry.NativeIdentifier != derived.nativeIdentifier ||
			entry.NativeField != derived.nativeField {
			return errors.New("native provenance does not match exact artifact field")
		}
	case CorrelationBindingDigestLink:
		if derived.mode != CorrelationBindingDigestLink ||
			entry.ParentRole != derived.parentRole ||
			entry.ParentDigest != derived.parentDigest ||
			entry.ParentDigestField != derived.parentDigestField {
			return errors.New("parent provenance is not proven by the child artifact")
		}
	}
	return nil
}

type correlationParentCandidate struct {
	parentRole        string
	parentDigest      string
	parentDigestField string
}

func digestLinkedParents(
	current correlationArtifactDraft,
	drafts []correlationArtifactDraft,
) []correlationParentCandidate {
	digests := correlationDigestFields(current.document)
	candidates := make([]correlationParentCandidate, 0)
	seen := make(map[string]struct{})
	for _, digest := range digests {
		for i := range drafts {
			if drafts[i].entry.Role == current.entry.Role ||
				drafts[i].entry.Digest != digest.Value {
				continue
			}
			key := digest.Path + "\x00" + drafts[i].entry.Role
			if _, duplicate := seen[key]; duplicate {
				continue
			}
			seen[key] = struct{}{}
			candidates = append(candidates, correlationParentCandidate{
				parentRole:        drafts[i].entry.Role,
				parentDigest:      drafts[i].entry.Digest,
				parentDigestField: digest.Path,
			})
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].parentDigestField == candidates[j].parentDigestField {
			return candidates[i].parentRole < candidates[j].parentRole
		}
		return candidates[i].parentDigestField < candidates[j].parentDigestField
	})
	return candidates
}

func correlationStringFields(document map[string]any) []correlationFieldCandidate {
	fields := make([]correlationFieldCandidate, 0)
	collectCorrelationStringFields(document, "", "", &fields)
	sort.Slice(fields, func(i, j int) bool {
		return fields[i].Path < fields[j].Path
	})
	return fields
}

func collectCorrelationStringFields(
	value any,
	path string,
	field string,
	fields *[]correlationFieldCandidate,
) {
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			childPath := appendCorrelationJSONPointer(path, key)
			collectCorrelationStringFields(typed[key], childPath, key, fields)
		}
	case []any:
		for i, child := range typed {
			collectCorrelationStringFields(
				child,
				appendCorrelationJSONPointer(path, fmt.Sprintf("%d", i)),
				field,
				fields,
			)
		}
	case string:
		*fields = append(*fields, correlationFieldCandidate{
			Path:  path,
			Field: field,
			Value: typed,
		})
	}
}

func appendCorrelationJSONPointer(path, token string) string {
	token = strings.ReplaceAll(token, "~", "~0")
	token = strings.ReplaceAll(token, "/", "~1")
	return path + "/" + token
}

func validCorrelationJSONPointer(value string) bool {
	if !strings.HasPrefix(value, "/") {
		return false
	}
	for i := 0; i < len(value); i++ {
		if value[i] != '~' {
			continue
		}
		if i+1 >= len(value) || (value[i+1] != '0' && value[i+1] != '1') {
			return false
		}
		i++
	}
	return true
}

func correlationStringField(document map[string]any, path string) (string, bool) {
	for _, candidate := range correlationStringFields(document) {
		if candidate.Path == path {
			return candidate.Value, true
		}
	}
	return "", false
}

func correlationDigestFields(document map[string]any) []correlationFieldCandidate {
	fields := correlationStringFields(document)
	digests := make([]correlationFieldCandidate, 0)
	for _, candidate := range fields {
		if !isDigestBearingField(candidate.Field) {
			continue
		}
		normalized, valid := normalizeSHA256Digest(candidate.Value)
		if !valid {
			continue
		}
		candidate.Value = normalized
		digests = append(digests, candidate)
	}
	return digests
}

func isNativeIdentifierField(field string) bool {
	return field == "id" ||
		strings.HasSuffix(field, "_id") ||
		field == "action_digest"
}

func normalizeNativeIdentifier(field, value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	if isDigestBearingField(field) {
		return normalizeSHA256Digest(value)
	}
	return value, true
}

func normalizeSHA256Digest(value string) (string, bool) {
	switch {
	case sha256DigestPattern.MatchString(value):
		return value, true
	case rawSHA256DigestPattern.MatchString(value):
		return "sha256:" + value, true
	default:
		return "", false
	}
}

func isDigestBearingField(field string) bool {
	return field == "sha256" ||
		strings.HasSuffix(field, "_sha256") ||
		strings.HasSuffix(field, "_digest")
}

func validSHA256Digest(value string) bool {
	return sha256DigestPattern.MatchString(value)
}
