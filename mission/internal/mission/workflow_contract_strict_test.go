package mission

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestObjectiveWorkflowContractRequiresEveryPublicField(t *testing.T) {
	body := readObjectiveWorkflowFixtureForStrictTest(t)
	required := requiredFieldsForContract(ObjectiveWorkflowSchema)
	for _, field := range required {
		field := field
		t.Run("missing "+field, func(t *testing.T) {
			document := decodeObjectForStrictTest(t, body)
			delete(document, field)
			assertWorkflowDecodeRejectedForStrictTest(t, document, field)
		})
		t.Run("null "+field, func(t *testing.T) {
			document := decodeObjectForStrictTest(t, body)
			document[field] = nil
			assertWorkflowDecodeRejectedForStrictTest(t, document, field)
		})
	}
}

func TestObjectiveWorkflowStageRequiresEveryPublicField(t *testing.T) {
	body := readObjectiveWorkflowFixtureForStrictTest(t)
	for _, field := range []string{"name", "status", "reason"} {
		field := field
		t.Run("missing "+field, func(t *testing.T) {
			document := decodeObjectForStrictTest(t, body)
			stage := document["stages"].([]any)[0].(map[string]any)
			delete(stage, field)
			assertWorkflowDecodeRejectedForStrictTest(t, document, field)
		})
		t.Run("null "+field, func(t *testing.T) {
			document := decodeObjectForStrictTest(t, body)
			stage := document["stages"].([]any)[0].(map[string]any)
			stage[field] = nil
			assertWorkflowDecodeRejectedForStrictTest(t, document, field)
		})
	}
}

func TestMissionRecordLoadAndValidationRejectIncompleteWorkflowContract(t *testing.T) {
	for name, mutate := range map[string]func(map[string]any){
		"missing authority": func(workflow map[string]any) {
			delete(workflow, "approves_work")
		},
		"null authority": func(workflow map[string]any) {
			workflow["approves_work"] = nil
		},
		"missing stage field": func(workflow map[string]any) {
			delete(workflow["stages"].([]any)[0].(map[string]any), "reason")
		},
	} {
		t.Run(name, func(t *testing.T) {
			store := NewStore(t.TempDir())
			contract, err := store.StartObjective(
				"strictly validate persisted workflow contract",
				ObjectiveStartOptions{CorrelationID: "corr-workflow-strict-001"},
			)
			if err != nil {
				t.Fatal(err)
			}
			path := store.path(contract.MissionID)
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			document := decodeObjectForStrictTest(t, body)
			mutate(document["workflow_contract"].(map[string]any))
			writeJSONForTest(t, path, document)

			if _, err := store.Load(contract.MissionID); err == nil {
				t.Fatal("Store.Load accepted incomplete workflow contract")
			}
			if _, err := ValidateContractFile(path); err == nil {
				t.Fatal("generic Mission validation accepted incomplete workflow contract")
			}
		})
	}
}

func TestMissionStoreRejectsInvalidWorkflowBeforeReplacingDurableState(t *testing.T) {
	for _, operation := range []string{"save", "update"} {
		t.Run(operation, func(t *testing.T) {
			store := NewStore(t.TempDir())
			contract, err := store.StartObjective(
				"reject invalid workflow writes",
				ObjectiveStartOptions{CorrelationID: "corr-workflow-write-001"},
			)
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(store.path(contract.MissionID))
			if err != nil {
				t.Fatal(err)
			}

			switch operation {
			case "save":
				var record Record
				record, err = store.Load(contract.MissionID)
				if err != nil {
					t.Fatal(err)
				}
				record.WorkflowContract.ApprovesWork = true
				err = store.Save(record)
			case "update":
				_, err = store.Update(contract.MissionID, func(record *Record) error {
					record.WorkflowContract.ApprovesWork = true
					return nil
				})
			}
			if err == nil {
				t.Fatalf("%s accepted an invalid workflow contract", operation)
			}
			after, readErr := os.ReadFile(store.path(contract.MissionID))
			if readErr != nil {
				t.Fatal(readErr)
			}
			if string(after) != string(before) {
				t.Fatalf("%s replaced durable Mission bytes before validation", operation)
			}
			if _, loadErr := store.Load(contract.MissionID); loadErr != nil {
				t.Fatalf("Mission became unreadable after rejected %s: %v", operation, loadErr)
			}
		})
	}
}

func TestMissionArchiveImportRejectsMissingOrNullWorkflowAuthority(t *testing.T) {
	store := NewStore(t.TempDir())
	contract, err := store.StartObjective(
		"strictly validate archived workflow contract",
		ObjectiveStartOptions{CorrelationID: "corr-workflow-archive-001"},
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
	body, err := json.Marshal(archive)
	if err != nil {
		t.Fatal(err)
	}

	for name, useNull := range map[string]bool{
		"missing": false,
		"null":    true,
	} {
		t.Run(name, func(t *testing.T) {
			document := decodeObjectForStrictTest(t, body)
			workflow := document["record"].(map[string]any)["workflow_contract"].(map[string]any)
			if useNull {
				workflow["safe_to_execute"] = nil
			} else {
				delete(workflow, "safe_to_execute")
			}
			path := filepath.Join(t.TempDir(), "archive.json")
			writeJSONForTest(t, path, document)
			destination := NewStore(t.TempDir())
			if _, err := ImportMissionArchive(destination, path); err == nil {
				t.Fatal("archive import accepted incomplete workflow contract")
			}
			if _, err := destination.Load(record.MissionID); !os.IsNotExist(err) {
				t.Fatalf("rejected archive changed destination: %v", err)
			}
		})
	}
}

func TestObjectiveWorkflowValidationIsIntrinsicOutsideSourceTree(t *testing.T) {
	body := readObjectiveWorkflowFixtureForStrictTest(t)
	for name, mutate := range map[string]func(map[string]any){
		"unknown root": func(document map[string]any) {
			document["unexpected"] = true
		},
		"missing nested stage field": func(document map[string]any) {
			delete(document["stages"].([]any)[0].(map[string]any), "reason")
		},
		"null authority": func(document map[string]any) {
			document["mutates_repositories"] = nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			document := decodeObjectForStrictTest(t, body)
			mutate(document)
			path := filepath.Join(t.TempDir(), "workflow.json")
			writeJSONForTest(t, path, document)

			originalCWD, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chdir(t.TempDir()); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := os.Chdir(originalCWD); err != nil {
					t.Errorf("restore cwd: %v", err)
				}
			}()

			result, err := ValidateContractFile(path)
			if err == nil || result.Status != "blocked" {
				t.Fatalf("intrinsic workflow validation accepted malformed contract: result=%+v err=%v", result, err)
			}
		})
	}
}

func TestKnownContractFailsClosedWhenRuntimeSchemaIsUnavailable(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "examples", "valid", "scheduler-readback.json"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "scheduler-readback.json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	originalCWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(originalCWD); err != nil {
			t.Errorf("restore cwd: %v", err)
		}
	}()

	result, err := ValidateContractFile(path)
	if err == nil || result.Status != "blocked" ||
		!strings.Contains(strings.Join(result.Blockers, " "), "schema definition unavailable") {
		t.Fatalf("known contract passed without its runtime schema: result=%+v err=%v", result, err)
	}
}

func TestEveryPublicFirstPartyContractFailsClosedWithoutItsSchemaDefinition(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "docs", "contracts", "*.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)
	schemas := make([]string, 0, len(paths))
	for _, schemaPath := range paths {
		body, err := os.ReadFile(schemaPath)
		if err != nil {
			t.Fatal(err)
		}
		document := decodeObjectForStrictTest(t, body)
		properties, _ := document["properties"].(map[string]any)
		schemaProperty, _ := properties["schema"].(map[string]any)
		schema, _ := schemaProperty["const"].(string)
		if schema == "" {
			continue
		}
		schemas = append(schemas, schema)
	}
	originalCWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(originalCWD); err != nil {
			t.Errorf("restore cwd: %v", err)
		}
	}()

	for _, schema := range schemas {
		t.Run(schema, func(t *testing.T) {
			if intrinsic, _ := validateIntrinsicCorrelationContract([]byte(`{}`), schema); intrinsic {
				return
			}
			blockers := validateAgainstSchemaFile(
				filepath.Join(t.TempDir(), "contract.json"),
				map[string]any{"schema": schema},
				schema,
			)
			if !strings.Contains(strings.Join(blockers, " "), "schema definition unavailable") {
				t.Fatalf("public first-party contract failed open without schema: %v", blockers)
			}
		})
	}
	if len(schemas) < 35 {
		t.Fatalf("public first-party registry sweep covered only %d schemas", len(schemas))
	}
}

func TestLoadMissionArchiveRejectsWorkflowIdentityAndSemanticDrift(t *testing.T) {
	store := NewStore(t.TempDir())
	contract, err := store.StartObjective(
		"strictly validate direct archive loading",
		ObjectiveStartOptions{CorrelationID: "corr-workflow-load-001"},
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
	body, err := json.Marshal(archive)
	if err != nil {
		t.Fatal(err)
	}

	for name, mutate := range map[string]func(map[string]any){
		"archive record identity": func(document map[string]any) {
			document["mission_id"] = "mission-ffffffffffffffff"
		},
		"workflow mission identity": func(document map[string]any) {
			document["record"].(map[string]any)["workflow_contract"].(map[string]any)["mission_id"] =
				"mission-ffffffffffffffff"
		},
		"workflow correlation identity": func(document map[string]any) {
			document["record"].(map[string]any)["workflow_contract"].(map[string]any)["correlation_id"] =
				"corr-foreign"
		},
		"workflow routing semantics": func(document map[string]any) {
			document["record"].(map[string]any)["workflow_contract"].(map[string]any)["routing_class"] =
				"unsupported"
		},
	} {
		t.Run(name, func(t *testing.T) {
			document := decodeObjectForStrictTest(t, body)
			mutate(document)
			path := filepath.Join(t.TempDir(), "archive.json")
			writeJSONForTest(t, path, document)
			if _, err := LoadMissionArchive(path); err == nil {
				t.Fatal("LoadMissionArchive accepted workflow identity or semantic drift")
			}
		})
	}
}

func readObjectiveWorkflowFixtureForStrictTest(t *testing.T) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", "examples", "valid", "objective-workflow-contract.json"))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func decodeObjectForStrictTest(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatal(err)
	}
	return document
}

func assertWorkflowDecodeRejectedForStrictTest(t *testing.T, document map[string]any, field string) {
	t.Helper()
	body, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	var contract ObjectiveWorkflowContract
	if err := json.Unmarshal(body, &contract); err == nil ||
		!strings.Contains(err.Error(), field) {
		t.Fatalf("workflow decoder accepted malformed %s: %v", field, err)
	}
}
