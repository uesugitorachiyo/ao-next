package mission

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateContractFileRejectsDuplicateKeysAtEveryNestingDepth(t *testing.T) {
	fixture := filepath.Join("..", "..", "examples", "valid", "objective-workflow-contract.json")
	body, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}

	tests := map[string][]byte{
		"root":            duplicateJSONField(t, body, `"safe_to_execute": false`, `"safe_to_execute": true, "safe_to_execute": false`),
		"nested object":   duplicateJSONField(t, body, `"name": "ao-blueprint"`, `"name": "shadow-route", "name": "ao-blueprint"`),
		"object in array": duplicateJSONField(t, body, `"status": "omitted"`, `"status": "required", "status": "omitted"`),
	}
	for name, malformed := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "contract.json")
			if err := os.WriteFile(path, malformed, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := ValidateContractFile(path); err == nil || !strings.Contains(err.Error(), "duplicate JSON key") {
				t.Fatalf("duplicate contract key was not rejected: %v", err)
			}
		})
	}
}

func TestObjectiveWorkflowContractRejectsDuplicateAuthorityAndNestedKeys(t *testing.T) {
	fixture := filepath.Join("..", "..", "examples", "valid", "objective-workflow-contract.json")
	body, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}

	for name, malformed := range map[string][]byte{
		"true then false authority": duplicateJSONField(t, body, `"approves_work": false`, `"approves_work": true, "approves_work": false`),
		"nested stage":              duplicateJSONField(t, body, `"name": "ao-blueprint"`, `"name": "unsafe-route", "name": "ao-blueprint"`),
	} {
		t.Run(name, func(t *testing.T) {
			var contract ObjectiveWorkflowContract
			if err := json.Unmarshal(malformed, &contract); err == nil || !strings.Contains(err.Error(), "duplicate JSON key") {
				t.Fatalf("duplicate workflow key was not rejected: %v", err)
			}
		})
	}
}

func TestMissionArchiveEntryPointsRejectDuplicateAuthorityAtAnyDepth(t *testing.T) {
	store := NewStore(t.TempDir())
	record, err := store.Start("archive duplicate-key regression")
	if err != nil {
		t.Fatal(err)
	}
	archive, err := BuildMissionArchive(record)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(archive)
	if err != nil {
		t.Fatal(err)
	}

	for name, malformed := range map[string][]byte{
		"archive true then false": duplicateLastJSONField(t, body, `"approves_work":false`, `"approves_work":true,"approves_work":false`),
		"nested record status":    duplicateJSONField(t, body, `"status":"active"`, `"status":"closed","status":"active"`),
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "archive.json")
			if err := os.WriteFile(path, malformed, 0o600); err != nil {
				t.Fatal(err)
			}
			checks := map[string]func() error{
				"load": func() error {
					_, err := LoadMissionArchive(path)
					return err
				},
				"validate": func() error {
					_, err := ValidateMissionArchive(path)
					return err
				},
				"import": func() error {
					_, err := ImportMissionArchive(NewStore(t.TempDir()), path)
					return err
				},
			}
			for entryPoint, check := range checks {
				if err := check(); err == nil || !strings.Contains(err.Error(), "duplicate JSON key") {
					t.Errorf("%s accepted duplicate archive key: %v", entryPoint, err)
				}
			}
		})
	}
}

func TestTouchedPublicDecodersRejectDuplicateAuthorityFields(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}

	snapshotPath := write("snapshot.json", `{"safe_to_execute":true,"safe_to_execute":false}`)
	if _, err := LoadGovernanceSnapshot(snapshotPath); err == nil || !strings.Contains(err.Error(), "duplicate JSON key") {
		t.Fatalf("governance snapshot accepted duplicate authority: %v", err)
	}

	cardPath := write("agent-card.json", `{"mutation_authority":true,"mutation_authority":false}`)
	if _, err := BuildA2AStreamingDenialReadback(cardPath); err == nil || !strings.Contains(err.Error(), "duplicate JSON key") {
		t.Fatalf("A2A streaming decoder accepted duplicate authority: %v", err)
	}
	if _, err := BuildA2ACompatibilityReadback(cardPath, "", ""); err == nil || !strings.Contains(err.Error(), "duplicate JSON key") {
		t.Fatalf("A2A compatibility decoder accepted duplicate authority: %v", err)
	}

	readbackPath := write("readback.json", `{"status":"ready","approves_work":true,"approves_work":false}`)
	if _, err := BuildGatewayReadinessRollup(readbackPath); err == nil || !strings.Contains(err.Error(), "duplicate JSON key") {
		t.Fatalf("gateway rollup accepted duplicate authority: %v", err)
	}
}

func duplicateJSONField(t *testing.T, body []byte, old, replacement string) []byte {
	t.Helper()
	if !strings.Contains(string(body), old) {
		t.Fatalf("fixture does not contain %q", old)
	}
	return []byte(strings.Replace(string(body), old, replacement, 1))
}

func duplicateLastJSONField(t *testing.T, body []byte, old, replacement string) []byte {
	t.Helper()
	index := strings.LastIndex(string(body), old)
	if index < 0 {
		t.Fatalf("fixture does not contain %q", old)
	}
	return append(append(append([]byte(nil), body[:index]...), replacement...), body[index+len(old):]...)
}
