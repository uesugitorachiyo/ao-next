package mission

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var pythonRepairGateNow = time.Date(2026, 8, 8, 17, 0, 0, 0, time.UTC)

func TestPythonRepairProductGatePassesTechnicalRepairWithoutQualificationAuthority(t *testing.T) {
	root, manifest := writePythonRepairGateFixture(t, nil)
	result, err := EvaluatePythonRepairProductGate(root, manifest, pythonRepairGateNow)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "passed" || result.TechnicalRepairDecision != "passed" ||
		result.GovernedQualificationDecision != "not_run" ||
		result.ReleaseDecision != "not_qualified" {
		t.Fatalf("unexpected product gate result: %+v", result)
	}
	if result.ExecutesWork || result.ApprovesWork || result.MutatesRepositories ||
		result.ReleaseAttempted || result.DeploymentAttempted || result.PublicationAttempted {
		t.Fatalf("product gate widened authority: %+v", result)
	}
}

func TestPythonRepairProductGateAcceptsBoundQualificationAndLifecycle(t *testing.T) {
	root, manifest := writePythonRepairGateFixture(t, func(evidence map[string]any) {
		bindings := evidence["bindings"].(map[string]any)
		bindings["governed_qualification_sha256"] = "sha256:" + strings.Repeat("a", 64)
		bindings["process_lifecycle_sha256"] = "sha256:" + strings.Repeat("b", 64)
		evidence["qualification"] = map[string]any{
			"required": true, "status": "passed", "record_sha256": bindings["governed_qualification_sha256"],
		}
		evidence["lifecycle"] = map[string]any{
			"required": true, "status": "passed", "orphan_processes": 0,
			"record_sha256": bindings["process_lifecycle_sha256"],
		}
	})
	result, err := EvaluatePythonRepairProductGate(root, manifest, pythonRepairGateNow)
	if err != nil {
		t.Fatal(err)
	}
	if result.GovernedQualificationDecision != "passed" || !result.ProcessLifecyclePassed ||
		result.ReleaseDecision != "eligible_for_separate_authorization" || result.AuthorityAdvanced {
		t.Fatalf("qualification or lifecycle was not represented: %+v", result)
	}
}

func TestPythonRepairProductGateRejectsValidFormatIdentitySubstitution(t *testing.T) {
	cases := map[string]func(map[string]any){
		"gate":        func(e map[string]any) { e["gate_id"] = "livekit-agents-6309-gate" },
		"repository":  func(e map[string]any) { e["repository"] = "livekit/components" },
		"issue":       func(e map[string]any) { e["issue_number"] = 6309 },
		"source":      func(e map[string]any) { e["source_sha"] = strings.Repeat("f", 40) },
		"source tree": func(e map[string]any) { e["source_tree_sha256"] = "sha256:" + strings.Repeat("c", 64) },
		"candidate":   func(e map[string]any) { e["candidate_sha256"] = "sha256:" + strings.Repeat("d", 64) },
		"correlation": func(e map[string]any) { e["correlation_id"] = "corr-livekit-agents-6309" },
		"binding": func(e map[string]any) {
			e["bindings"].(map[string]any)["issue_snapshot_sha256"] = "sha256:" + strings.Repeat("e", 64)
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			root, manifest := writePythonRepairGateFixture(t, nil)
			evidencePath := filepath.Join(root, "evidence.json")
			evidence := readJSONMap(t, evidencePath)
			mutate(evidence)
			writeJSONFile(t, evidencePath, evidence)
			rebindPythonRepairGateEvidenceArtifact(t, manifest, evidencePath)
			if _, err := EvaluatePythonRepairProductGate(root, manifest, pythonRepairGateNow); err == nil {
				t.Fatal("valid-format identity substitution passed")
			}
		})
	}
}

func TestPythonRepairProductGateRejectsNestedDigestSubstitution(t *testing.T) {
	cases := map[string]func(map[string]any){
		"selection": func(e map[string]any) {
			e["selection"].(map[string]any)["record_sha256"] = "sha256:" + strings.Repeat("e", 64)
		},
		"reproduction": func(e map[string]any) {
			e["reproduction"].(map[string]any)["evidence_sha256"] = "sha256:" + strings.Repeat("e", 64)
		},
		"candidate tree": func(e map[string]any) {
			e["candidate"].(map[string]any)["tree_sha256"] = "sha256:" + strings.Repeat("e", 64)
		},
		"candidate seal": func(e map[string]any) {
			e["candidate"].(map[string]any)["seal_sha256"] = "sha256:" + strings.Repeat("e", 64)
		},
		"repair pack": func(e map[string]any) {
			e["repair_pack"].(map[string]any)["validation_sha256"] = "sha256:" + strings.Repeat("e", 64)
		},
		"security": func(e map[string]any) {
			e["security"].(map[string]any)["routing_sha256"] = "sha256:" + strings.Repeat("e", 64)
		},
		"score": func(e map[string]any) {
			e["evaluation"].(map[string]any)["record_sha256"] = "sha256:" + strings.Repeat("e", 64)
		},
		"authority": func(e map[string]any) {
			e["authority"].(map[string]any)["ledger_sha256"] = "sha256:" + strings.Repeat("e", 64)
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			root, manifest := writePythonRepairGateFixture(t, mutate)
			if _, err := EvaluatePythonRepairProductGate(root, manifest, pythonRepairGateNow); err == nil {
				t.Fatal("nested digest substitution passed")
			}
		})
	}
}

func TestPythonRepairProductGateRejectsFailedStaleOrUnsafeEvidence(t *testing.T) {
	cases := map[string]func(map[string]any){
		"wrong source": func(e map[string]any) { e["source_sha"] = "not-a-sha" },
		"stale":        func(e map[string]any) { e["expires_at"] = "2026-08-08T16:59:59Z" },
		"selection": func(e map[string]any) {
			e["selection"].(map[string]any)["deterministic"] = false
		},
		"selection age": func(e map[string]any) {
			e["selection"].(map[string]any)["selected_at"] = "2026-07-01T00:00:00Z"
		},
		"candidate": func(e map[string]any) {
			e["candidate"].(map[string]any)["focused_candidate_exit_code"] = 1
		},
		"deterministic replay": func(e map[string]any) {
			e["candidate"].(map[string]any)["deterministic_replay_passed"] = false
		},
		"repair pack": func(e map[string]any) {
			e["repair_pack"].(map[string]any)["status"] = "failed"
		},
		"lifecycle": func(e map[string]any) {
			e["lifecycle"] = map[string]any{
				"required": true, "status": "not_applicable", "orphan_processes": 0,
				"record_sha256": "",
			}
		},
		"security": func(e map[string]any) {
			e["security"].(map[string]any)["public_manifest_excluded"] = false
		},
		"score": func(e map[string]any) { e["evaluation"].(map[string]any)["score"] = 7 },
		"authority": func(e map[string]any) {
			e["authority"].(map[string]any)["provider_calls"] = 1
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			root, manifest := writePythonRepairGateFixture(t, mutate)
			if _, err := EvaluatePythonRepairProductGate(root, manifest, pythonRepairGateNow); err == nil {
				t.Fatal("unsafe evidence passed")
			}
		})
	}
}

func TestPythonRepairProductGateRejectsDigestDrift(t *testing.T) {
	root, manifest := writePythonRepairGateFixture(t, nil)
	if err := os.WriteFile(filepath.Join(root, "evidence.json"), []byte(`{"changed":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := EvaluatePythonRepairProductGate(root, manifest, pythonRepairGateNow); err == nil ||
		!strings.Contains(err.Error(), "digest") {
		t.Fatalf("digest drift was not rejected: %v", err)
	}
}

func TestPythonRepairProductGateRejectsMalformedDuplicateAndOversizedEvidence(t *testing.T) {
	for name, body := range map[string][]byte{
		"malformed": []byte(`{"schema_version":`),
		"duplicate": []byte(`{"schema_version":"a","schema_version":"b"}`),
		"oversized": bytes.Repeat([]byte("x"), pythonRepairProductGateLimit+1),
	} {
		t.Run(name, func(t *testing.T) {
			root, manifest := writePythonRepairGateRawFixture(t, body)
			if _, err := EvaluatePythonRepairProductGate(root, manifest, pythonRepairGateNow); err == nil {
				t.Fatal("invalid evidence passed")
			}
		})
	}
}

func TestPythonRepairProductGateRejectsUnknownNestedField(t *testing.T) {
	root, manifest := writePythonRepairGateFixture(t, func(evidence map[string]any) {
		evidence["selection"].(map[string]any)["unexpected"] = true
	})
	if _, err := EvaluatePythonRepairProductGate(root, manifest, pythonRepairGateNow); err == nil {
		t.Fatal("unknown nested field passed")
	}
}

func TestPythonRepairProductGateRejectsOptionalBindingMismatch(t *testing.T) {
	root, manifest := writePythonRepairGateFixture(t, func(evidence map[string]any) {
		evidence["bindings"].(map[string]any)["governed_qualification_sha256"] =
			"sha256:" + strings.Repeat("a", 64)
	})
	if _, err := EvaluatePythonRepairProductGate(root, manifest, pythonRepairGateNow); err == nil {
		t.Fatal("unrequired qualification binding passed")
	}
}

func TestPythonRepairProductGateRejectsUnsafePathAndSymlink(t *testing.T) {
	root, manifest := writePythonRepairGateFixture(t, nil)
	var document map[string]any
	body, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatal(err)
	}
	document["evidence"].(map[string]any)["path"] = "../evidence.json"
	writeJSONFile(t, manifest, document)
	if _, err := EvaluatePythonRepairProductGate(root, manifest, pythonRepairGateNow); err == nil {
		t.Fatal("traversal path passed")
	}

	root, manifest = writePythonRepairGateFixture(t, nil)
	target := filepath.Join(root, "evidence.json")
	linked := filepath.Join(root, "linked.json")
	createTestSymlink(t, target, linked)
	document = readJSONMap(t, manifest)
	info, _ := os.Stat(target)
	digest := fileSHA256(t, target)
	document["evidence"] = map[string]any{
		"path": "linked.json", "size_bytes": info.Size(), "sha256": digest,
	}
	writeJSONFile(t, manifest, document)
	if _, err := EvaluatePythonRepairProductGate(root, manifest, pythonRepairGateNow); err == nil {
		t.Fatal("symlink passed")
	}
}

func TestPythonRepairProductGateRejectsHardlink(t *testing.T) {
	root, manifest := writePythonRepairGateFixture(t, nil)
	target := filepath.Join(root, "evidence.json")
	linked := filepath.Join(root, "linked.json")
	if err := os.Link(target, linked); err != nil {
		t.Skipf("hardlinks unavailable: %v", err)
	}
	document := readJSONMap(t, manifest)
	info, err := os.Stat(linked)
	if err != nil {
		t.Fatal(err)
	}
	document["evidence"] = map[string]any{
		"path": "linked.json", "size_bytes": info.Size(), "sha256": fileSHA256(t, linked),
	}
	writeJSONFile(t, manifest, document)
	if _, err := EvaluatePythonRepairProductGate(root, manifest, pythonRepairGateNow); err == nil {
		t.Fatal("hardlink passed")
	}
}

func TestCLIPythonRepairProductGateRoundTrip(t *testing.T) {
	now := time.Now().UTC()
	root, manifest := writePythonRepairGateFixture(t, func(evidence map[string]any) {
		evidence["completed_at"] = now.Add(-time.Minute).Format(time.RFC3339)
		evidence["expires_at"] = now.Add(time.Hour).Format(time.RFC3339)
		evidence["selection"].(map[string]any)["selected_at"] = now.Add(-2 * time.Minute).Format(time.RFC3339)
		selectedAt, err := time.Parse(time.RFC3339, evidence["selection"].(map[string]any)["selected_at"].(string))
		if err != nil {
			t.Fatal(err)
		}
		completedAt, err := time.Parse(time.RFC3339, evidence["completed_at"].(string))
		if err != nil {
			t.Fatal(err)
		}
		expiresAt, err := time.Parse(time.RFC3339, evidence["expires_at"].(string))
		if err != nil {
			t.Fatal(err)
		}
		if selectedAt.After(completedAt) || completedAt.After(expiresAt) || completedAt.Sub(selectedAt) > 7*24*time.Hour {
			t.Fatalf("time-inconsistent repair fixture: selected=%s completed=%s expires=%s", selectedAt, completedAt, expiresAt)
		}
	})
	var stdout, stderr bytes.Buffer
	code := Run([]string{
		"issue-repair", "product-gate", "--root", root, "--manifest", manifest, "--json",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("product gate failed: %s", stderr.String())
	}
	var result PythonRepairProductGateResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.SchemaVersion != PythonRepairProductGateResultSchema || result.Status != "passed" {
		t.Fatalf("unexpected CLI result: %+v", result)
	}
}

func writePythonRepairGateFixture(t *testing.T, mutate func(map[string]any)) (string, string) {
	t.Helper()
	evidence := validPythonRepairGateEvidence()
	if mutate != nil {
		mutate(evidence)
	}
	body, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	return writePythonRepairGateRawFixture(t, body)
}

func writePythonRepairGateRawFixture(t *testing.T, body []byte) (string, string) {
	t.Helper()
	root := t.TempDir()
	evidencePath := filepath.Join(root, "evidence.json")
	if err := os.WriteFile(evidencePath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(root, "manifest.json")
	expected := validPythonRepairGateEvidence()
	if err := json.Unmarshal(body, &expected); err != nil {
		expected = validPythonRepairGateEvidence()
	}
	writeJSONFile(t, manifestPath, map[string]any{
		"schema_version": PythonRepairProductGateManifestSchema,
		"gate_id":        expected["gate_id"], "repository": expected["repository"],
		"issue_number": expected["issue_number"], "source_sha": expected["source_sha"],
		"source_tree_sha256": expected["source_tree_sha256"],
		"candidate_sha256":   expected["candidate_sha256"],
		"correlation_id":     expected["correlation_id"], "bindings": expected["bindings"],
		"evidence": map[string]any{
			"path": "evidence.json", "size_bytes": len(body), "sha256": fileSHA256(t, evidencePath),
		},
	})
	return root, manifestPath
}

func validPythonRepairGateEvidence() map[string]any {
	digest := func(char string) string { return "sha256:" + strings.Repeat(char, 64) }
	return map[string]any{
		"schema_version":     PythonRepairProductGateEvidenceSchema,
		"gate_id":            "livekit-agents-6308-gate",
		"repository":         "livekit/agents",
		"issue_number":       6308,
		"source_sha":         strings.Repeat("1", 40),
		"source_tree_sha256": digest("b"),
		"candidate_sha256":   digest("2"),
		"correlation_id":     "corr-livekit-agents-6308",
		"completed_at":       "2026-08-08T16:59:00Z",
		"expires_at":         "2026-08-08T18:00:00Z",
		"bindings": map[string]any{
			"issue_snapshot_sha256": digest("3"), "selection_sha256": digest("4"),
			"reproduction_sha256": digest("5"), "candidate_seal_sha256": digest("6"),
			"repair_pack_validation_sha256": digest("7"), "governed_qualification_sha256": "",
			"process_lifecycle_sha256": "", "security_routing_sha256": digest("8"),
			"independent_score_sha256": digest("9"), "authority_ledger_sha256": digest("a"),
		},
		"selection": map[string]any{
			"deterministic": true, "exact_source": true, "oracle_access_before_seal": false,
			"selected_at": "2026-08-08T16:58:00Z", "record_sha256": digest("4"),
		},
		"reproduction": map[string]any{
			"result": "reproduced_failure", "baseline_exit_code": 1, "network": "none",
			"git_history_present": false, "credentials_present": false, "external_effects": 0,
			"fixture_sha256": digest("c"), "output_sha256": digest("d"),
			"evidence_sha256": digest("5"),
		},
		"candidate": map[string]any{
			"sealed": true, "focused_candidate_exit_code": 0, "changed_file_precision": true,
			"applicable_suite": "baseline_limitation_matched",
			"tree_sha256":      digest("2"), "patch_sha256": digest("e"), "seal_sha256": digest("6"),
			"baseline_suite_sha256": digest("f"), "applicable_suite_sha256": digest("1"),
			"deterministic_replay_sha256": digest("2"), "deterministic_replay_passed": true,
			"baseline_comparison": "matched_limitation",
		},
		"repair_pack": map[string]any{
			"schema_version": "ao2.github-issue-repair-pack-validation.v3",
			"status":         "passed", "eligibility_status": "reproduced", "failed_rows": 0,
			"validation_sha256": digest("7"),
		},
		"qualification": map[string]any{"required": false, "status": "not_run", "record_sha256": ""},
		"lifecycle": map[string]any{
			"required": false, "status": "not_applicable", "orphan_processes": 0, "record_sha256": "",
		},
		"security": map[string]any{
			"private_routing_passed": true, "public_manifest_excluded": true,
			"credentials_present": false, "routing_sha256": digest("8"),
		},
		"evaluation": map[string]any{
			"score": 9, "threshold": 8, "correct": true, "negative_mutations_passed": true,
			"record_sha256": digest("9"),
		},
		"authority": map[string]any{
			"level": "L1", "provider_calls": 0, "external_effects": 0,
			"third_party_remote_mutations": 0, "release_attempted": false,
			"deployment_attempted": false, "publication_attempted": false, "ledger_sha256": digest("a"),
		},
	}
}

func rebindPythonRepairGateEvidenceArtifact(t *testing.T, manifestPath, evidencePath string) {
	t.Helper()
	manifest := readJSONMap(t, manifestPath)
	info, err := os.Stat(evidencePath)
	if err != nil {
		t.Fatal(err)
	}
	manifest["evidence"] = map[string]any{
		"path": filepath.Base(evidencePath), "size_bytes": info.Size(), "sha256": fileSHA256(t, evidencePath),
	}
	writeJSONFile(t, manifestPath, manifest)
}

func writeJSONFile(t *testing.T, path string, value any) {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readJSONMap(t *testing.T, path string) map[string]any {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func fileSHA256(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}
