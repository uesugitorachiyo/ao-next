package mission

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	MissionCheckpointSchema  = "ao.mission.checkpoint.v0.3"
	CheckpointBundleSchema   = "ao.mission.checkpoint-resume-bundle.v0.3"
	maximumSliceEvidenceSize = 16 * 1024 * 1024
)

var (
	sliceCheckpointPattern = regexp.MustCompile(`^S0[1-7]$`)
	sliceEvidencePattern   = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	sliceResultPattern     = regexp.MustCompile(`^slice_pass:(S0[1-7]):(sha256:[0-9a-f]{64})$`)
)

var sliceAuthorityFields = map[string]struct{}{
	"safe_to_execute": {}, "executes_work": {}, "approves_work": {},
	"mutates_repositories": {}, "provider_calls": {}, "credential_use": {},
	"release": {}, "publication": {}, "deployment": {}, "promotion": {},
	"compatibility_activation": {}, "external_beta": {}, "rsi": {},
}

type SliceCheckpointOptions struct {
	Slice          string
	EvidenceDigest string
}

func appendMissionCheckpoint(r *Record, step ContinuationStep) MissionCheckpoint {
	checkpoint := MissionCheckpoint{
		Schema:          MissionCheckpointSchema,
		MissionID:       r.MissionID,
		CorrelationID:   r.CorrelationID,
		Sequence:        len(r.Checkpoints) + 1,
		Iteration:       step.Iteration,
		Route:           step.Route,
		Phase:           r.CurrentPhase,
		Result:          step.Result,
		ExactNextAction: step.ExactNextAction,
		ResumeCommand:   fmt.Sprintf("ao-mission continue --mission %s --until-done --max-iterations 10", r.MissionID),
		GeneratedAtUTC:  step.GeneratedAtUTC,
	}
	r.Checkpoints = append(r.Checkpoints, checkpoint)
	return checkpoint
}

func CreateMissionCheckpoint(s Store, missionID string) (MissionCheckpointBundle, error) {
	record, err := s.Update(missionID, func(record *Record) error {
		if count := len(record.Checkpoints); count > 0 {
			latest := record.Checkpoints[count-1]
			if latest.Result == "checkpoint_created" && latest.Route == record.CurrentRoute &&
				latest.Phase == record.CurrentPhase && latest.ExactNextAction == record.ExactNextAction {
				return nil
			}
		}
		appendMissionCheckpoint(record, ContinuationStep{
			Iteration:       len(record.Steps),
			Route:           record.CurrentRoute,
			Result:          "checkpoint_created",
			ExactNextAction: record.ExactNextAction,
			GeneratedAtUTC:  now(s.Clock),
		})
		return nil
	})
	if err != nil {
		return MissionCheckpointBundle{}, err
	}
	return BuildCheckpointBundle(record), nil
}

func CreateSliceCheckpoint(s Store, missionID string, options SliceCheckpointOptions) (MissionCheckpointBundle, error) {
	if !sliceCheckpointPattern.MatchString(options.Slice) {
		return MissionCheckpointBundle{}, fmt.Errorf("slice must be one of S01 through S07")
	}
	if !sliceEvidencePattern.MatchString(options.EvidenceDigest) {
		return MissionCheckpointBundle{}, fmt.Errorf("evidence digest must be sha256 followed by 64 lowercase hex characters")
	}
	record, err := s.Update(missionID, func(record *Record) error {
		if err := validateSliceCheckpointEvidence(s, *record, options); err != nil {
			return err
		}
		appendCheckpoint, err := validateSliceCheckpointOrder(*record, options)
		if err != nil {
			return err
		}
		if !appendCheckpoint {
			return nil
		}
		appendMissionCheckpoint(record, ContinuationStep{
			Iteration:       len(record.Steps),
			Route:           record.CurrentRoute,
			Result:          "slice_pass:" + options.Slice + ":" + options.EvidenceDigest,
			ExactNextAction: record.ExactNextAction,
			GeneratedAtUTC:  now(s.Clock),
		})
		return nil
	})
	if err != nil {
		return MissionCheckpointBundle{}, err
	}
	return BuildCheckpointBundle(record), nil
}

func validateSliceCheckpointOrder(record Record, options SliceCheckpointOptions) (bool, error) {
	latestSlice := 0
	for _, checkpoint := range record.Checkpoints {
		match := sliceResultPattern.FindStringSubmatch(checkpoint.Result)
		if match == nil {
			continue
		}
		if match[1] == options.Slice {
			if match[2] == options.EvidenceDigest {
				return false, nil
			}
			return false, fmt.Errorf("slice %s already checkpointed with a different evidence digest", options.Slice)
		}
		current := int(match[1][2] - '0')
		if current > latestSlice {
			latestSlice = current
		}
	}
	requested := int(options.Slice[2] - '0')
	if requested != latestSlice+1 {
		return false, fmt.Errorf("slice checkpoint is out of order: got %s after S%02d", options.Slice, latestSlice)
	}
	return true, nil
}

func validateSliceCheckpointEvidence(s Store, record Record, options SliceCheckpointOptions) error {
	var ref *ArtifactRef
	for index := range record.ArtifactRefs {
		if record.ArtifactRefs[index].Digest == options.EvidenceDigest {
			if ref != nil {
				return fmt.Errorf("evidence digest resolves to multiple artifact references")
			}
			ref = &record.ArtifactRefs[index]
		}
	}
	if ref == nil {
		return fmt.Errorf("evidence digest is not retained by Mission")
	}
	objectName := filepath.Join(retainedArtifactDirectory, strings.TrimPrefix(options.EvidenceDigest, "sha256:"))
	expectedRef := filepath.Join(s.Root, objectName)
	if filepath.Clean(ref.ContentRef) != filepath.Clean(expectedRef) {
		return fmt.Errorf("evidence content_ref is not the expected retained object")
	}
	root, err := openRetainedArtifactRoot(s.Root)
	if err != nil {
		return fmt.Errorf("open retained evidence root: %w", err)
	}
	defer root.Close()
	info, err := root.Lstat(objectName)
	if err != nil {
		return fmt.Errorf("inspect retained slice evidence: %w", err)
	}
	if info.Size() > maximumSliceEvidenceSize {
		return fmt.Errorf("slice evidence exceeds %d bytes", maximumSliceEvidenceSize)
	}
	body, err := readRetainedArtifact(root, objectName)
	if err != nil {
		return err
	}
	if digestBytes(body) != options.EvidenceDigest {
		return fmt.Errorf("retained evidence digest mismatch")
	}
	if err := validateNoDuplicateJSONKeys(body); err != nil {
		return fmt.Errorf("slice evidence: %w", err)
	}
	var document map[string]any
	if err := json.Unmarshal(body, &document); err != nil {
		return fmt.Errorf("slice evidence must be one JSON object: %w", err)
	}
	if schema, ok := document["schema"].(string); !ok || strings.TrimSpace(schema) == "" {
		return fmt.Errorf("slice evidence schema is required")
	}
	if document["correlation_id"] != record.CorrelationID {
		return fmt.Errorf("slice evidence correlation_id mismatch")
	}
	if document["mission_ref"] != record.MissionID {
		return fmt.Errorf("slice evidence mission_ref mismatch")
	}
	if document["slice"] != options.Slice {
		return fmt.Errorf("slice evidence slice mismatch")
	}
	if document["result"] != "pass" {
		return fmt.Errorf("slice evidence result must be pass")
	}
	authority, ok := document["authority"].(map[string]any)
	if !ok {
		return fmt.Errorf("slice evidence authority must be an object")
	}
	for field := range authority {
		if _, known := sliceAuthorityFields[field]; known {
			continue
		}
		for known := range sliceAuthorityFields {
			if strings.EqualFold(field, known) {
				return fmt.Errorf("slice evidence authority field case variant: %s", field)
			}
		}
		return fmt.Errorf("slice evidence authority unknown property: %s", field)
	}
	for field := range sliceAuthorityFields {
		value, present := authority[field]
		if !present {
			return fmt.Errorf("slice evidence authority missing property: %s", field)
		}
		if value != false {
			return fmt.Errorf("slice evidence authority must remain false: %s", field)
		}
	}
	if err := rejectNestedSliceAuthority(document, ""); err != nil {
		return err
	}
	return nil
}

func rejectNestedSliceAuthority(value any, path string) error {
	switch typed := value.(type) {
	case map[string]any:
		for field, child := range typed {
			childPath := path + "/" + strings.ReplaceAll(strings.ReplaceAll(field, "~", "~0"), "/", "~1")
			if _, known := sliceAuthorityFields[field]; known {
				if child != false {
					return fmt.Errorf("slice evidence nested authority must remain false: %s", childPath)
				}
			} else {
				for known := range sliceAuthorityFields {
					if strings.EqualFold(field, known) {
						return fmt.Errorf("slice evidence authority field case variant: %s", childPath)
					}
				}
			}
			if err := rejectNestedSliceAuthority(child, childPath); err != nil {
				return err
			}
		}
	case []any:
		for index, child := range typed {
			if err := rejectNestedSliceAuthority(child, fmt.Sprintf("%s/%d", path, index)); err != nil {
				return err
			}
		}
	}
	return nil
}

func BuildCheckpointBundle(r Record) MissionCheckpointBundle {
	var latest *MissionCheckpoint
	if n := len(r.Checkpoints); n > 0 {
		cp := r.Checkpoints[n-1]
		latest = &cp
	}
	gate := EvaluateReturnGate(r)
	return MissionCheckpointBundle{
		Schema:              CheckpointBundleSchema,
		MissionID:           r.MissionID,
		CorrelationID:       r.CorrelationID,
		Status:              "ready",
		CheckpointCount:     len(r.Checkpoints),
		LatestCheckpoint:    latest,
		ReturnGate:          &gate,
		ResumePrompt:        fmt.Sprintf("ao-mission continue --mission %s --until-done --max-iterations 10", r.MissionID),
		SafeToExecute:       false,
		ExecutesWork:        false,
		ApprovesWork:        false,
		MutatesRepositories: false,
		GeneratedAtUTC:      now(nil),
	}
}
