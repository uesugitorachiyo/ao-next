package mission

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

var (
	beforeArtifactManifestStageWrite = func(string) error { return nil }
	beforeArtifactManifestPublish    = func(string) error { return nil }
)

func BuildArtifactManifest(r Record) ArtifactManifest {
	refs := append([]ArtifactRef(nil), r.ArtifactRefs...)
	for i := range refs {
		refs[i].ContentRef = ""
	}
	return FinalizeArtifactManifest(ArtifactManifest{
		Schema:        "ao.mission.artifact-manifest.v0.1",
		MissionID:     r.MissionID,
		ArtifactRefs:  refs,
		SafeToExecute: false,
		ExecutesWork:  false,
		ApprovesWork:  false,
	})
}

func FinalizeArtifactManifest(manifest ArtifactManifest) ArtifactManifest {
	if manifest.Schema == "" {
		manifest.Schema = "ao.mission.artifact-manifest.v0.1"
	}
	manifest.ManifestDigest = artifactManifestDigest(manifest)
	manifest.Signature = "ao-mission-local-digest:" + manifest.ManifestDigest
	manifest.SafeToExecute = false
	manifest.ExecutesWork = false
	manifest.ApprovesWork = false
	manifest.GeneratedAtUTC = now(nil)
	return manifest
}

func artifactManifestDigest(manifest ArtifactManifest) string {
	var body []byte
	if manifest.Schema == "ao.mission.artifact-manifest.v0.2" {
		body, _ = json.Marshal(struct {
			Schema       string        `json:"schema"`
			MissionID    string        `json:"mission_id"`
			ArtifactRefs []ArtifactRef `json:"artifact_refs"`
		}{Schema: manifest.Schema, MissionID: manifest.MissionID, ArtifactRefs: manifest.ArtifactRefs})
	} else {
		// Preserve the v0.1 digest representation for historical manifests.
		body, _ = json.Marshal(struct {
			MissionID    string        `json:"mission_id"`
			ArtifactRefs []ArtifactRef `json:"artifact_refs"`
		}{MissionID: manifest.MissionID, ArtifactRefs: manifest.ArtifactRefs})
	}
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func MaterializeArtifactManifest(r Record, outPath string) (ArtifactManifest, error) {
	refs := append([]ArtifactRef(nil), r.ArtifactRefs...)
	contents := make([][]byte, len(refs))
	for i := range refs {
		source := refs[i].ContentRef
		if source == "" {
			source = refs[i].Ref
		}
		data, err := readArtifactFile(source)
		if err != nil {
			return ArtifactManifest{}, fmt.Errorf("read retained artifact %s: %w", refs[i].Ref, err)
		}
		if digestBytes(data) != refs[i].Digest {
			return ArtifactManifest{}, fmt.Errorf("artifact digest mismatch for %s", refs[i].Ref)
		}
		refs[i].ContentRef = artifactManifestContentRef(refs[i].Digest)
		contents[i] = data
	}
	if err := publishArtifactManifestContent(outPath, refs, contents); err != nil {
		return ArtifactManifest{}, err
	}
	return FinalizeArtifactManifest(ArtifactManifest{
		Schema:       "ao.mission.artifact-manifest.v0.2",
		MissionID:    r.MissionID,
		ArtifactRefs: refs,
	}), nil
}

func ValidateArtifactManifestFile(path string) (ArtifactManifestValidation, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return ArtifactManifestValidation{Schema: "ao.mission.artifact-manifest-validation.v0.1", Status: "failed", GeneratedAtUTC: now(nil)}, err
	}
	manifest, err := decodeArtifactManifest(body)
	if err != nil {
		return ArtifactManifestValidation{Schema: "ao.mission.artifact-manifest-validation.v0.1", Status: "failed", GeneratedAtUTC: now(nil)}, err
	}
	result := ArtifactManifestValidation{
		Schema:         "ao.mission.artifact-manifest-validation.v0.1",
		Status:         "passed",
		MissionID:      manifest.MissionID,
		ArtifactCount:  len(manifest.ArtifactRefs),
		ManifestDigest: manifest.ManifestDigest,
		ExecutesWork:   false,
		ApprovesWork:   false,
		GeneratedAtUTC: now(nil),
	}
	if err := validateArtifactManifestEnvelope(manifest); err != nil {
		result.Status = "failed"
		return result, err
	}
	for _, ref := range manifest.ArtifactRefs {
		if err := validateArtifactManifestRef(ref, manifest.Schema); err != nil {
			result.Status = "failed"
			return result, err
		}
		data, err := readArtifactManifestReference(path, ref, manifest.Schema)
		if err != nil {
			result.Status = "failed"
			return result, err
		}
		got := digestBytes(data)
		if manifest.Schema == "ao.mission.artifact-manifest.v0.1" {
			sum := sha256.Sum256(normalizeTextArtifactDigestData(data))
			got = "sha256:" + hex.EncodeToString(sum[:])
		}
		if got != ref.Digest {
			result.Status = "failed"
			return result, fmt.Errorf("artifact digest mismatch for %s", ref.Ref)
		}
	}
	return result, nil
}

func RepairArtifactManifestFile(path string) (ArtifactManifest, error) {
	return repairArtifactManifestFile(path, path)
}

func repairArtifactManifestFile(path, outPath string) (ArtifactManifest, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return ArtifactManifest{}, err
	}
	manifest, err := decodeArtifactManifest(body)
	if err != nil {
		return ArtifactManifest{}, err
	}
	if err := validateArtifactManifestEnvelope(manifest); err != nil {
		return ArtifactManifest{}, err
	}
	refs := append([]ArtifactRef(nil), manifest.ArtifactRefs...)
	contents := make([][]byte, len(refs))
	for i, ref := range refs {
		if err := validateArtifactManifestRef(ref, manifest.Schema); err != nil {
			return ArtifactManifest{}, err
		}
		data, err := readArtifactManifestReference(path, ref, manifest.Schema)
		if err != nil {
			return ArtifactManifest{}, err
		}
		if digestBytes(data) != ref.Digest {
			return ArtifactManifest{}, fmt.Errorf("artifact digest mismatch for %s", ref.Ref)
		}
		refs[i].ContentRef = artifactManifestContentRef(ref.Digest)
		contents[i] = data
	}
	if err := publishArtifactManifestContent(outPath, refs, contents); err != nil {
		return ArtifactManifest{}, err
	}
	return FinalizeArtifactManifest(ArtifactManifest{
		Schema:       "ao.mission.artifact-manifest.v0.2",
		MissionID:    manifest.MissionID,
		ArtifactRefs: refs,
	}), nil
}

func validateArtifactManifestRef(ref ArtifactRef, schema string) error {
	if strings.TrimSpace(ref.Ref) == "" || strings.TrimSpace(ref.Digest) == "" {
		return fmt.Errorf("artifact manifest refs require ref and digest")
	}
	if !strings.HasPrefix(ref.Digest, "sha256:") {
		return fmt.Errorf("artifact manifest ref %s digest must start with sha256:", ref.Ref)
	}
	if schema == "ao.mission.artifact-manifest.v0.2" {
		if !isCanonicalSHA256Digest(ref.Digest) {
			return fmt.Errorf("artifact manifest ref %s digest must be a canonical sha256 digest", ref.Ref)
		}
		if ref.Schema != ArtifactRefSchema {
			return fmt.Errorf("artifact manifest ref %s artifact ref schema must be %s", ref.Ref, ArtifactRefSchema)
		}
		if ref.ContentRef != artifactManifestContentRef(ref.Digest) {
			return fmt.Errorf("artifact manifest ref %s content_ref must be contained and digest-addressed", ref.Ref)
		}
	}
	return nil
}

func readArtifactManifestReference(manifestPath string, ref ArtifactRef, schema string) ([]byte, error) {
	if schema == "ao.mission.artifact-manifest.v0.2" {
		return readArtifactManifestContent(manifestPath, ref.ContentRef)
	}
	actualPath := ref.Ref
	if !filepath.IsAbs(actualPath) {
		if _, err := os.Stat(actualPath); err != nil {
			actualPath = filepath.Join(filepath.Dir(manifestPath), actualPath)
		}
	}
	return readArtifactFile(actualPath)
}

func artifactManifestContentRef(digest string) string {
	return filepath.ToSlash(filepath.Join(retainedArtifactDirectory, strings.TrimPrefix(digest, "sha256:")))
}

func artifactManifestContentPath(contentRef string) (string, error) {
	if contentRef != artifactManifestContentRef("sha256:"+filepath.Base(filepath.FromSlash(contentRef))) {
		return "", fmt.Errorf("artifact manifest content_ref must be contained and digest-addressed")
	}
	return filepath.FromSlash(contentRef), nil
}

func publishArtifactManifestContent(outPath string, refs []ArtifactRef, contents [][]byte) error {
	if len(refs) != len(contents) {
		return fmt.Errorf("artifact manifest content count mismatch")
	}
	rootPath := filepath.Dir(outPath)
	if err := os.MkdirAll(rootPath, 0o755); err != nil {
		return fmt.Errorf("create artifact manifest root: %w", err)
	}
	stage, err := os.MkdirTemp(rootPath, "."+filepath.Base(outPath)+".bundle-")
	if err != nil {
		return fmt.Errorf("create artifact manifest staging bundle: %w", err)
	}
	defer os.RemoveAll(stage)
	stageContentRoot := filepath.Join(stage, retainedArtifactDirectory)
	if err := os.MkdirAll(stageContentRoot, 0o755); err != nil {
		return fmt.Errorf("create artifact manifest staging content directory: %w", err)
	}
	for i, ref := range refs {
		contentRef, err := artifactManifestContentPath(ref.ContentRef)
		if err != nil {
			return err
		}
		stagePath := filepath.Join(stage, contentRef)
		if err := beforeArtifactManifestStageWrite(stagePath); err != nil {
			return err
		}
		if err := writeAtomicFile(stagePath, contents[i], 0o644); err != nil {
			return fmt.Errorf("write artifact manifest staging object: %w", err)
		}
		staged, err := readArtifactFile(stagePath)
		if err != nil {
			return fmt.Errorf("verify artifact manifest staging object: %w", err)
		}
		if !bytes.Equal(staged, contents[i]) || digestBytes(staged) != ref.Digest {
			return fmt.Errorf("artifact manifest staging object digest mismatch for %s", ref.Ref)
		}
	}
	store := NewStore(rootPath)
	for _, ref := range refs {
		contentRef, err := artifactManifestContentPath(ref.ContentRef)
		if err != nil {
			return err
		}
		staged, err := readArtifactFile(filepath.Join(stage, contentRef))
		if err != nil {
			return fmt.Errorf("read artifact manifest staging object: %w", err)
		}
		_, digest, err := store.retainArtifact(staged)
		if err != nil {
			return fmt.Errorf("publish artifact manifest content: %w", err)
		}
		if digest != ref.Digest {
			return fmt.Errorf("artifact manifest published object digest mismatch for %s", ref.Ref)
		}
	}
	return nil
}

func writeArtifactManifestFile(path string, manifest ArtifactManifest) error {
	body, err := marshalIndentedLine(manifest)
	if err != nil {
		return err
	}
	if err := beforeArtifactManifestPublish(path); err != nil {
		return err
	}
	return writeAtomicFile(path, body, 0o644)
}

func readArtifactManifestContent(manifestPath, contentRef string) ([]byte, error) {
	contentPath, err := artifactManifestContentPath(contentRef)
	if err != nil {
		return nil, err
	}
	root, err := openRetainedArtifactRoot(filepath.Dir(manifestPath))
	if err != nil {
		return nil, fmt.Errorf("open artifact manifest root: %w", err)
	}
	defer root.Close()
	return readRetainedArtifact(root, contentPath)
}

func readRetainedArtifact(root retainedArtifactRoot, path string) ([]byte, error) {
	before, err := root.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect retained artifact: %w", err)
	}
	if !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("retained artifact must be a regular non-symlink file")
	}
	file, err := openRetainedArtifactFile(root, path)
	if err != nil {
		return nil, fmt.Errorf("open retained artifact: %w", err)
	}
	opened, statErr := file.Stat()
	if statErr != nil {
		_ = file.Close()
		return nil, fmt.Errorf("stat retained artifact: %w", statErr)
	}
	if !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		_ = file.Close()
		return nil, fmt.Errorf("retained artifact changed while opening")
	}
	body, readErr := io.ReadAll(file)
	closeErr := file.Close()
	if readErr != nil {
		return nil, fmt.Errorf("read retained artifact: %w", readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close retained artifact: %w", closeErr)
	}
	after, err := root.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("reinspect retained artifact: %w", err)
	}
	if !after.Mode().IsRegular() || !os.SameFile(opened, after) {
		return nil, fmt.Errorf("retained artifact changed while reading")
	}
	return body, nil
}

func decodeArtifactManifest(body []byte) (ArtifactManifest, error) {
	value, err := decodeExactJSON(body)
	if err != nil {
		return ArtifactManifest{}, err
	}
	document, ok := value.(map[string]any)
	if !ok || document == nil {
		return ArtifactManifest{}, fmt.Errorf("artifact manifest must be a JSON object")
	}
	schema, _ := document["schema"].(string)
	if schema != "ao.mission.artifact-manifest.v0.2" {
		var manifest ArtifactManifest
		if err := json.Unmarshal(body, &manifest); err != nil {
			return ArtifactManifest{}, err
		}
		return manifest, nil
	}
	var manifest ArtifactManifest
	if err := decodeStrictJSONObject(body, &manifest, "artifact manifest v0.2", map[string]string{
		"schema": "string", "mission_id": "string", "artifact_refs": "array", "manifest_digest": "string",
		"signature": "string", "safe_to_execute": "boolean", "executes_work": "boolean",
		"approves_work": "boolean", "generated_at_utc": "string",
	}, []string{"schema", "mission_id", "artifact_refs", "manifest_digest", "signature", "safe_to_execute", "executes_work", "approves_work"}); err != nil {
		return ArtifactManifest{}, err
	}
	refs, _ := document["artifact_refs"].([]any)
	for i, raw := range refs {
		refBody, err := json.Marshal(raw)
		if err != nil {
			return ArtifactManifest{}, err
		}
		var ref ArtifactRef
		if err := decodeStrictJSONObject(refBody, &ref, "artifact manifest v0.2 artifact ref", map[string]string{
			"schema": "string", "ref": "string", "content_ref": "string", "digest": "string", "kind": "string",
		}, []string{"schema", "ref", "content_ref", "digest"}); err != nil {
			return ArtifactManifest{}, err
		}
		manifest.ArtifactRefs[i] = ref
	}
	return manifest, nil
}

func validateArtifactManifestEnvelope(manifest ArtifactManifest) error {
	if manifest.Schema != "ao.mission.artifact-manifest.v0.1" && manifest.Schema != "ao.mission.artifact-manifest.v0.2" {
		return fmt.Errorf("artifact manifest schema must be ao.mission.artifact-manifest.v0.1 or ao.mission.artifact-manifest.v0.2")
	}
	if manifest.ExecutesWork || manifest.ApprovesWork || manifest.SafeToExecute {
		return fmt.Errorf("artifact manifest must not claim execution or approval authority")
	}
	if manifest.Schema == "ao.mission.artifact-manifest.v0.2" && !isCanonicalSHA256Digest(manifest.ManifestDigest) {
		return fmt.Errorf("artifact manifest digest must be a canonical sha256 digest")
	}
	expected := artifactManifestDigest(manifest)
	if manifest.ManifestDigest != expected {
		return fmt.Errorf("artifact manifest digest mismatch")
	}
	if manifest.Schema == "ao.mission.artifact-manifest.v0.2" && manifest.Signature != "ao-mission-local-digest:"+manifest.ManifestDigest {
		return fmt.Errorf("artifact manifest signature does not bind manifest digest")
	}
	return nil
}

func isCanonicalSHA256Digest(digest string) bool {
	encoded := strings.TrimPrefix(digest, "sha256:")
	if encoded == digest || len(encoded) != sha256.Size*2 || encoded != strings.ToLower(encoded) {
		return false
	}
	_, err := hex.DecodeString(encoded)
	return err == nil
}

func readArtifactFile(path string) ([]byte, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("artifact path is required")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("artifact must be a regular non-symlink file")
	}
	return os.ReadFile(path)
}

func normalizeTextArtifactDigestData(data []byte) []byte {
	if !utf8.Valid(data) {
		return data
	}
	return []byte(strings.ReplaceAll(string(data), "\r\n", "\n"))
}

func BuildCommandStatus(r Record) CommandStatus {
	var atlasRecommendation *AtlasRecommendationReadbackCounts
	if r.Evidence.AtlasRecommendation != nil {
		copy := *r.Evidence.AtlasRecommendation
		atlasRecommendation = &copy
	}
	var aoNextCandidate *AONextCandidateProjection
	if r.Evidence.AONextCandidate != nil {
		copy := *r.Evidence.AONextCandidate
		aoNextCandidate = &copy
	}
	var aoNextJournalProjection *AONextJournalProjection
	if r.Evidence.AONextJournalProjection != nil {
		copy := *r.Evidence.AONextJournalProjection
		aoNextJournalProjection = &copy
	}
	var goalLease *GoalLease
	if r.GoalLease != nil {
		copy := *r.GoalLease
		goalLease = &copy
	}
	gate := r.ReturnGate
	if gate == nil {
		evaluated := EvaluateReturnGate(r)
		gate = &evaluated
	}
	checkpointFreshness := "missing"
	if len(r.Checkpoints) > 0 {
		checkpointFreshness = "fresh"
	} else if gate != nil && !gate.FinalResponseAllowed {
		checkpointFreshness = "stale_or_missing"
	} else if gate != nil && gate.FinalResponseAllowed {
		checkpointFreshness = "not_required"
	}
	returnGateStatus := ""
	if gate != nil {
		returnGateStatus = gate.Status
	}
	return CommandStatus{
		Schema:                     "ao.command.mission-status.v0.1",
		MissionID:                  r.MissionID,
		CorrelationID:              r.CorrelationID,
		Status:                     r.Status,
		SourceRecordStatus:         r.SourceRecordStatus,
		TerminalProjectionStatus:   r.TerminalProjectionStatus,
		TerminalProjectionReadOnly: r.TerminalProjectionReadOnly,
		EffectiveOperatorStatus:    r.EffectiveOperatorStatus,
		CurrentRoute:               r.CurrentRoute,
		CurrentPhase:               r.CurrentPhase,
		ExactNextAction:            r.ExactNextAction,
		GoalLease:                  goalLease,
		CheckpointCount:            len(r.Checkpoints),
		CheckpointFreshnessStatus:  checkpointFreshness,
		ReturnGateStatus:           returnGateStatus,
		ReadOnly:                   true,
		SafeToExecute:              false,
		ExecutesWork:               false,
		ApprovesWork:               false,
		MutatesRepositories:        false,
		AtlasRecommendation:        atlasRecommendation,
		AONextCandidate:            aoNextCandidate,
		AONextJournalProjection:    aoNextJournalProjection,
		Blockers:                   r.Blockers,
		GeneratedAtUTC:             now(nil),
	}
}
