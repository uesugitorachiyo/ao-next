package mission

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

func BuildMissionVerificationBundleReadback(s Store, missionID string, opts MissionVerificationBundleOptions) (MissionVerificationBundleReadback, error) {
	record, err := s.Load(missionID)
	if err != nil {
		return MissionVerificationBundleReadback{}, err
	}
	if err := validateRecordWorkflowContract(record); err != nil {
		return MissionVerificationBundleReadback{}, err
	}
	index, err := BuildMissionEventIndex(s)
	if err != nil {
		return MissionVerificationBundleReadback{}, err
	}
	dashboard, err := BuildMissionDashboardReadback(s, record.MissionID, true)
	if err != nil {
		return MissionVerificationBundleReadback{}, err
	}
	manifest := BuildArtifactManifest(record)
	components := []MissionVerificationBundleComponent{}
	if component, err := componentFromValue("event_index", index.Schema, index.Status, index); err != nil {
		return MissionVerificationBundleReadback{}, err
	} else {
		components = append(components, component)
	}
	if component, err := componentFromValue("dashboard", dashboard.Schema, dashboard.Status, dashboard); err != nil {
		return MissionVerificationBundleReadback{}, err
	} else {
		components = append(components, component)
	}
	if component, err := componentFromValue("artifact_manifest", manifest.Schema, "ready", manifest); err != nil {
		return MissionVerificationBundleReadback{}, err
	} else {
		components = append(components, component)
	}
	if record.WorkflowContract != nil {
		if component, err := componentFromValue("workflow_contract", record.WorkflowContract.Schema, record.WorkflowContract.Status, record.WorkflowContract); err != nil {
			return MissionVerificationBundleReadback{}, err
		} else {
			components = append(components, component)
		}
	}
	if strings.TrimSpace(opts.ReadinessBundlePath) != "" {
		component, err := componentFromFile("readiness_bundle", opts.ReadinessBundlePath, "ao.mission.readiness-bundle-readback.v0.1")
		if err != nil {
			return MissionVerificationBundleReadback{}, err
		}
		components = append(components, component)
	}
	if strings.TrimSpace(opts.GatewayReplayBundlePath) != "" {
		component, err := componentFromFile("gateway_replay_bundle", opts.GatewayReplayBundlePath, "ao.mission.gateway-replay-bundle-readback.v0.1")
		if err != nil {
			return MissionVerificationBundleReadback{}, err
		}
		components = append(components, component)
	}
	bundle := MissionVerificationBundleReadback{
		Schema:              "ao.mission.verification-bundle-readback.v0.1",
		Status:              "ready",
		MissionID:           record.MissionID,
		CorrelationID:       record.CorrelationID,
		ComponentCount:      len(components),
		Components:          components,
		SafeToExecute:       false,
		ExecutesWork:        false,
		ApprovesWork:        false,
		MutatesRepositories: false,
		ExactNextAction:     "verification bundle is local readback evidence; remote PR lifecycle remains operator-controlled",
		GeneratedAtUTC:      now(s.Clock),
	}
	digest, err := digestMissionVerificationBundle(bundle)
	if err != nil {
		return MissionVerificationBundleReadback{}, err
	}
	bundle.BundleDigest = digest
	return bundle, nil
}

func componentFromValue(name, schema, status string, value any) (MissionVerificationBundleComponent, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return MissionVerificationBundleComponent{}, err
	}
	return MissionVerificationBundleComponent{
		Name:   name,
		Schema: schema,
		Status: statusOrReady(status),
		SHA256: digestBytes(body),
	}, nil
}

func componentFromFile(name, path, wantSchema string) (MissionVerificationBundleComponent, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return MissionVerificationBundleComponent{}, err
	}
	if err := ValidatePublicSafeText(string(body)); err != nil {
		return MissionVerificationBundleComponent{}, err
	}
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		return MissionVerificationBundleComponent{}, err
	}
	schema, _ := doc["schema"].(string)
	if schema != wantSchema {
		return MissionVerificationBundleComponent{}, fmt.Errorf("%s schema must be %s", name, wantSchema)
	}
	if boolField(doc, "safe_to_execute") || boolField(doc, "executes_work") || boolField(doc, "approves_work") || boolField(doc, "mutates_repositories") {
		return MissionVerificationBundleComponent{}, fmt.Errorf("%s must not claim execution, approval, or repository mutation authority", name)
	}
	status, _ := doc["status"].(string)
	return MissionVerificationBundleComponent{
		Name:   name,
		Schema: schema,
		Path:   path,
		Status: statusOrReady(status),
		SHA256: digestBytes(body),
	}, nil
}

func digestMissionVerificationBundle(bundle MissionVerificationBundleReadback) (string, error) {
	copy := bundle
	copy.BundleDigest = ""
	body, err := json.Marshal(copy)
	if err != nil {
		return "", err
	}
	return digestBytes(body), nil
}

func boolField(doc map[string]any, field string) bool {
	value, _ := doc[field].(bool)
	return value
}

func statusOrReady(status string) string {
	status = strings.TrimSpace(status)
	if status == "" {
		return "ready"
	}
	return status
}
