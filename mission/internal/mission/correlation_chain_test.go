package mission

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCorrelationChainBuildIsDeterministicAndValidatesArtifacts(t *testing.T) {
	dir := t.TempDir()
	record := correlationTestRecord()
	parentPath := filepath.Join(dir, "objective.json")
	writeJSONForTest(t, parentPath, map[string]any{
		"schema":         ObjectiveWorkflowSchema,
		"correlation_id": record.CorrelationID,
	})
	parentBody, err := os.ReadFile(parentPath)
	if err != nil {
		t.Fatal(err)
	}
	childPath := filepath.Join(dir, "authorization.json")
	writeJSONForTest(t, childPath, map[string]any{
		"schema":        "ao.blueprint.build-authorization.v0.1",
		"parent_digest": digestBytes(parentBody),
	})

	specs := []CorrelationArtifactSpec{
		{Role: "blueprint-authorization", Path: childPath},
		{Role: "objective-contract", Path: parentPath},
	}
	first, err := BuildCorrelationChain(record, specs)
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildCorrelationChain(record, []CorrelationArtifactSpec{specs[1], specs[0]})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("build is not deterministic:\nfirst=%+v\nsecond=%+v", first, second)
	}
	if first.Schema != CorrelationChainSchema ||
		first.MissionID != record.MissionID ||
		first.CorrelationID != record.CorrelationID ||
		len(first.Entries) != 2 {
		t.Fatalf("unexpected chain: %+v", first)
	}
	if first.SafeToExecute || first.ExecutesWork || first.ApprovesWork ||
		first.MutatesRepositories || first.WidensPolicy || first.PublishesArtifacts {
		t.Fatalf("chain widened authority: %+v", first)
	}
	if first.Entries[0].Role != "blueprint-authorization" ||
		first.Entries[0].BindingMode != CorrelationBindingDigestLink ||
		first.Entries[0].ParentRole != "objective-contract" ||
		first.Entries[0].ParentDigest != first.Entries[1].Digest {
		t.Fatalf("digest link was not derived: %+v", first.Entries)
	}
	if first.Entries[1].BindingMode != CorrelationBindingNativeField ||
		first.Entries[1].NativeField != "/correlation_id" ||
		first.Entries[1].NativeIdentifier != record.CorrelationID {
		t.Fatalf("native provenance was not derived: %+v", first.Entries[1])
	}

	path := filepath.Join(dir, "chain.json")
	writeJSONForTest(t, path, first)
	validation, err := ValidateCorrelationChainFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if validation.Schema != CorrelationChainValidationSchema ||
		validation.Status != "ready" ||
		validation.MissionID != record.MissionID ||
		validation.CorrelationID != record.CorrelationID ||
		validation.ArtifactCount != 2 ||
		!validSHA256Digest(validation.ChainDigest) {
		t.Fatalf("unexpected validation: %+v", validation)
	}
	if validation.SafeToExecute || validation.ExecutesWork || validation.ApprovesWork ||
		validation.MutatesRepositories || validation.WidensPolicy || validation.PublishesArtifacts {
		t.Fatalf("validation widened authority: %+v", validation)
	}
	if _, err := ValidateContractFile(path); err != nil {
		t.Fatalf("public correlation chain schema rejected builder output: %v", err)
	}

	named := first
	named.Entries = append([]CorrelationChainEntry(nil), first.Entries...)
	for i := range named.Entries {
		named.Entries[i].ArtifactName = filepath.Base(named.Entries[i].ArtifactPath)
		named.Entries[i].ArtifactPath = ""
	}
	namedPath := filepath.Join(dir, "named-chain.json")
	writeJSONForTest(t, namedPath, named)
	if _, err := ValidateCorrelationChainFile(namedPath); err != nil {
		t.Fatalf("unambiguous artifact_name chain was rejected: %v", err)
	}
}

func TestCorrelationChainRejectsMissionAndCorrelationMismatch(t *testing.T) {
	dir := t.TempDir()
	record := correlationTestRecord()
	artifactPath := filepath.Join(dir, "artifact.json")
	writeJSONForTest(t, artifactPath, map[string]any{
		"schema":         "ao.blueprint.authorization.v0.1",
		"correlation_id": record.CorrelationID,
	})
	chain, err := BuildCorrelationChain(record, []CorrelationArtifactSpec{{
		Role: "blueprint-authorization",
		Path: artifactPath,
	}})
	if err != nil {
		t.Fatal(err)
	}

	for name, mutate := range map[string]func(*CorrelationChain){
		"mission": func(chain *CorrelationChain) {
			chain.MissionID = "mission-ffffffffffffffff"
		},
		"correlation": func(chain *CorrelationChain) {
			chain.CorrelationID = "corr-other"
		},
	} {
		t.Run(name, func(t *testing.T) {
			tampered := chain
			mutate(&tampered)
			path := filepath.Join(t.TempDir(), "chain.json")
			writeJSONForTest(t, path, tampered)
			if _, err := ValidateCorrelationChainForRecord(path, record); err == nil {
				t.Fatalf("%s mismatch accepted", name)
			}
		})
	}

	writeJSONForTest(t, artifactPath, map[string]any{
		"schema":         "ao.blueprint.authorization.v0.1",
		"correlation_id": "corr-other",
	})
	if _, err := BuildCorrelationChain(record, []CorrelationArtifactSpec{{
		Role: "blueprint-authorization",
		Path: artifactPath,
	}}); err == nil {
		t.Fatal("builder accepted artifact correlation mismatch")
	}
}

func TestCorrelationChainRejectsNestedIdentityMismatchBeforeWeakerProvenance(t *testing.T) {
	record := correlationTestRecord()
	cases := map[string]any{
		"nested foreign mission": map[string]any{
			"provenance": map[string]any{"mission_id": "mission-foreign"},
		},
		"array foreign correlation": map[string]any{
			"results": []any{
				map[string]any{"correlation_id": "corr-foreign"},
			},
		},
		"nested null mission": map[string]any{
			"provenance": map[string]any{"mission_id": nil},
		},
		"nested numeric correlation": map[string]any{
			"provenance": map[string]any{"correlation_id": 42},
		},
	}
	for name, nested := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			artifactPath := filepath.Join(dir, "artifact.json")
			document := map[string]any{
				"schema": "ao.atlas.workgraph.v0.1",
			}
			for key, value := range nested.(map[string]any) {
				document[key] = value
			}
			writeJSONForTest(t, artifactPath, document)
			if _, err := BuildCorrelationChain(record, []CorrelationArtifactSpec{{
				Role: "atlas-workgraph",
				Path: artifactPath,
			}}); err == nil {
				t.Fatalf("builder accepted %s", name)
			}

			canonicalPath := canonicalPathForTest(t, artifactPath)
			chain := CorrelationChain{
				Schema:        CorrelationChainSchema,
				MissionID:     record.MissionID,
				CorrelationID: record.CorrelationID,
				Entries: []CorrelationChainEntry{{
					Role:             "atlas-workgraph",
					ArtifactPath:     canonicalPath,
					Digest:           digestFileForTest(t, canonicalPath),
					Producer:         "ao-atlas",
					BindingMode:      CorrelationBindingNativeField,
					NativeIdentifier: "ao.atlas.workgraph.v0.1",
					NativeField:      "schema",
				}},
			}
			chainPath := filepath.Join(dir, "chain.json")
			writeJSONForTest(t, chainPath, chain)
			if _, err := ValidateCorrelationChainForRecord(chainPath, record); err == nil {
				t.Fatalf("validator accepted %s by weaker schema provenance", name)
			}
		})
	}

	dir := t.TempDir()
	matchingPath := filepath.Join(dir, "matching.json")
	writeJSONForTest(t, matchingPath, map[string]any{
		"schema": "ao.atlas.workgraph.v0.1",
		"provenance": map[string]any{
			"correlation_id": record.CorrelationID,
		},
	})
	if _, err := BuildCorrelationChain(record, []CorrelationArtifactSpec{{
		Role: "matching-identities",
		Path: matchingPath,
	}}); err != nil {
		t.Fatalf("matching nested identities were rejected: %v", err)
	}
}

func TestCorrelationChainValidationRejectsChangedArtifact(t *testing.T) {
	dir := t.TempDir()
	record := correlationTestRecord()
	artifactPath := filepath.Join(dir, "artifact.json")
	writeJSONForTest(t, artifactPath, map[string]any{
		"schema":         "ao.atlas.workgraph.v0.1",
		"correlation_id": record.CorrelationID,
	})
	chain, err := BuildCorrelationChain(record, []CorrelationArtifactSpec{{
		Role: "atlas-workgraph",
		Path: artifactPath,
	}})
	if err != nil {
		t.Fatal(err)
	}
	chainPath := filepath.Join(dir, "chain.json")
	writeJSONForTest(t, chainPath, chain)
	writeJSONForTest(t, artifactPath, map[string]any{
		"schema":         "ao.atlas.workgraph.v0.1",
		"correlation_id": record.CorrelationID,
		"changed":        true,
	})

	if _, err := ValidateCorrelationChainFile(chainPath); err == nil ||
		!strings.Contains(err.Error(), "digest") {
		t.Fatalf("changed artifact did not fail by digest: %v", err)
	}
}

func TestCorrelationChainStrictContractRejectsMalformedInput(t *testing.T) {
	dir := t.TempDir()
	record := correlationTestRecord()
	artifactPath := filepath.Join(dir, "artifact.json")
	writeJSONForTest(t, artifactPath, map[string]any{
		"schema":         "ao.atlas.workgraph.v0.1",
		"correlation_id": record.CorrelationID,
	})
	chain, err := BuildCorrelationChain(record, []CorrelationArtifactSpec{{
		Role: "atlas-workgraph",
		Path: artifactPath,
	}})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(chain)
	if err != nil {
		t.Fatal(err)
	}
	var base map[string]any
	if err := json.Unmarshal(body, &base); err != nil {
		t.Fatal(err)
	}

	tests := map[string]func(map[string]any){
		"schema": func(doc map[string]any) {
			doc["schema"] = "ao.mission.correlation-chain.v9"
		},
		"unknown chain field": func(doc map[string]any) {
			doc["unknown"] = true
		},
		"malformed digest": func(doc map[string]any) {
			doc["entries"].([]any)[0].(map[string]any)["digest"] = "sha256:ABC"
		},
		"unknown entry field": func(doc map[string]any) {
			doc["entries"].([]any)[0].(map[string]any)["unknown"] = true
		},
		"invalid binding mode": func(doc map[string]any) {
			doc["entries"].([]any)[0].(map[string]any)["binding_mode"] = "implicit"
		},
		"incomplete native provenance": func(doc map[string]any) {
			delete(doc["entries"].([]any)[0].(map[string]any), "native_identifier")
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			var doc map[string]any
			if err := json.Unmarshal(body, &doc); err != nil {
				t.Fatal(err)
			}
			mutate(doc)
			path := filepath.Join(t.TempDir(), "chain.json")
			writeJSONForTest(t, path, doc)
			if _, err := ValidateCorrelationChainFile(path); err == nil {
				t.Fatalf("%s accepted", name)
			}
		})
	}
}

func TestCorrelationChainExactJSONRejectsDuplicateCaseVariantAndNullFields(t *testing.T) {
	dir := t.TempDir()
	record := correlationTestRecord()
	artifactPath := filepath.Join(dir, "artifact.json")
	writeJSONForTest(t, artifactPath, map[string]any{
		"schema":         "ao.atlas.workgraph.v0.1",
		"correlation_id": record.CorrelationID,
	})
	chain, err := BuildCorrelationChain(record, []CorrelationArtifactSpec{{
		Role: "atlas-workgraph",
		Path: artifactPath,
	}})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(chain)
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(body)
	chainCases := map[string]string{
		"duplicate top-level key": strings.Replace(
			encoded,
			`"schema":"`+CorrelationChainSchema+`"`,
			`"schema":"`+CorrelationChainSchema+`","schema":"`+CorrelationChainSchema+`"`,
			1,
		),
		"duplicate nested key": strings.Replace(
			encoded,
			`"role":"atlas-workgraph"`,
			`"role":"atlas-workgraph","role":"atlas-workgraph"`,
			1,
		),
		"case-variant top-level key": strings.Replace(encoded, `"schema":`, `"Schema":`, 1),
		"case-variant nested key":    strings.Replace(encoded, `"role":`, `"Role":`, 1),
		"null required string":       strings.Replace(encoded, `"producer":"ao-atlas"`, `"producer":null`, 1),
		"null required object":       strings.Replace(encoded, `"entries":[{`, `"entries":[null,{`, 1),
	}
	for name, malformed := range chainCases {
		t.Run("chain "+name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "chain.json")
			if err := os.WriteFile(path, []byte(malformed), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := ValidateCorrelationChainFile(path); err == nil {
				t.Fatalf("%s was accepted", name)
			}
		})
	}

	artifactCases := map[string]string{
		"duplicate top-level key": `{
			"schema":"ao.atlas.workgraph.v1",
			"schema":"ao.foundry.result.v1",
			"correlation_id":"` + record.CorrelationID + `"
		}`,
		"duplicate nested key": `{
			"schema":"ao.atlas.workgraph.v1",
			"correlation_id":"` + record.CorrelationID + `",
			"provenance":{"request_id":"first","request_id":"second"}
		}`,
		"case-variant nested contract key": `{
			"schema":"ao.atlas.workgraph.v1",
			"provenance":{"Request_ID":"request-001"}
		}`,
		"null schema string": `{
			"schema":null,
			"correlation_id":"` + record.CorrelationID + `"
		}`,
		"null root object": `null`,
		"trailing JSON": `{
			"schema":"ao.atlas.workgraph.v1",
			"correlation_id":"` + record.CorrelationID + `"
		}{}`,
	}
	for name, malformed := range artifactCases {
		t.Run("artifact "+name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "artifact.json")
			if err := os.WriteFile(path, []byte(malformed), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := BuildCorrelationChain(record, []CorrelationArtifactSpec{{
				Role: "artifact",
				Path: path,
			}}); err == nil {
				t.Fatalf("%s was accepted", name)
			}
		})
	}
}

func TestDecodeExactJSONRejectsRustIncompatibleUnicode(t *testing.T) {
	tests := []struct {
		name string
		body []byte
	}{
		{"invalid UTF-8", []byte("{\"value\":\"\xff\"}")},
		{"lone high surrogate", []byte(`{"value":"\ud800"}`)},
		{"lone low surrogate", []byte(`{"value":"\udc00"}`)},
		{"high surrogate followed by non-low", []byte(`{"value":"\ud800\u0061"}`)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := decodeExactJSON(test.body); err == nil {
				t.Fatalf("malformed Unicode was accepted: %q", test.body)
			}
		})
	}
}

func TestDecodeExactJSONAcceptsRustCompatibleUnicode(t *testing.T) {
	tests := []struct {
		name string
		body []byte
		want string
	}{
		{"valid surrogate pair", []byte(`{"value":"\ud83d\ude00"}`), "😀"},
		{"escaped literal high surrogate", []byte(`{"value":"\\ud800"}`), `\ud800`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value, err := decodeExactJSON(test.body)
			if err != nil {
				t.Fatal(err)
			}
			if got := value.(map[string]any)["value"]; got != test.want {
				t.Fatalf("value=%q want=%q", got, test.want)
			}
		})
	}
}

func TestDecodeExactJSONPreservesUnicodeEscapeSlashParity(t *testing.T) {
	tests := []struct {
		precedingSlashes int
		wantErr          bool
		want             string
	}{
		{precedingSlashes: 0, wantErr: true},
		{precedingSlashes: 1, want: `\ud800`},
		{precedingSlashes: 2, wantErr: true},
		{precedingSlashes: 3, want: `\\ud800`},
	}
	for _, test := range tests {
		t.Run(fmt.Sprintf("preceding-slashes-%d", test.precedingSlashes), func(t *testing.T) {
			body := []byte(`{"value":"` + strings.Repeat(`\`, test.precedingSlashes) + `\ud800"}`)
			value, err := decodeExactJSON(body)
			if test.wantErr {
				if err == nil {
					t.Fatalf("real lone surrogate was accepted: %q", body)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := value.(map[string]any)["value"]; got != test.want {
				t.Fatalf("value=%q want=%q", got, test.want)
			}
		})
	}
}

func TestCorrelationChainRawDecodingRequiresFalseAuthorityFields(t *testing.T) {
	dir := t.TempDir()
	record := correlationTestRecord()
	artifactPath := filepath.Join(dir, "artifact.json")
	writeJSONForTest(t, artifactPath, map[string]any{
		"schema":         "ao.atlas.workgraph.v0.1",
		"correlation_id": record.CorrelationID,
	})
	chain, err := BuildCorrelationChain(record, []CorrelationArtifactSpec{{
		Role: "atlas-workgraph",
		Path: artifactPath,
	}})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(chain)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}

	cases := map[string]func(map[string]any){
		"missing top-level false": func(doc map[string]any) {
			delete(doc, "safe_to_execute")
		},
		"null top-level false": func(doc map[string]any) {
			doc["executes_work"] = nil
		},
		"missing entry false": func(doc map[string]any) {
			delete(doc["entries"].([]any)[0].(map[string]any), "approves_work")
		},
		"null entry false": func(doc map[string]any) {
			doc["entries"].([]any)[0].(map[string]any)["mutates_repositories"] = nil
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			var malformed map[string]any
			if err := json.Unmarshal(body, &malformed); err != nil {
				t.Fatal(err)
			}
			mutate(malformed)
			path := filepath.Join(t.TempDir(), "chain.json")
			writeJSONForTest(t, path, malformed)
			if _, err := ValidateCorrelationChainFile(path); err == nil {
				t.Fatalf("%s was accepted", name)
			}
		})
	}
}

func TestStoreLoadStrictlyDecodesMissionCorrelationState(t *testing.T) {
	store, record, _, _ := importCorrelationTestWorkflow(t, true)
	recordPath := store.path(record.MissionID)
	valid, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	var validDoc map[string]any
	if err := json.Unmarshal(valid, &validDoc); err != nil {
		t.Fatal(err)
	}

	mapCases := map[string]func(map[string]any){
		"missing required Mission field": func(doc map[string]any) {
			delete(doc, "current_route")
		},
		"unknown Mission field": func(doc map[string]any) {
			doc["unexpected"] = true
		},
		"missing reference false": func(doc map[string]any) {
			ref := doc["correlation_chain_references"].([]any)[0].(map[string]any)
			delete(ref, "safe_to_execute")
		},
		"null reference false": func(doc map[string]any) {
			ref := doc["correlation_chain_references"].([]any)[0].(map[string]any)
			ref["executes_work"] = nil
		},
		"unknown reference field": func(doc map[string]any) {
			ref := doc["correlation_chain_references"].([]any)[0].(map[string]any)
			ref["unexpected"] = true
		},
		"case-variant reference field": func(doc map[string]any) {
			ref := doc["correlation_chain_references"].([]any)[0].(map[string]any)
			ref["Safe_To_Execute"] = ref["safe_to_execute"]
			delete(ref, "safe_to_execute")
		},
		"unknown reference entry field": func(doc map[string]any) {
			entry := doc["correlation_chain_references"].([]any)[0].(map[string]any)["entries"].([]any)[0].(map[string]any)
			entry["unexpected"] = true
		},
		"unknown correlated import field": func(doc map[string]any) {
			binding := doc["correlated_imports"].([]any)[0].(map[string]any)
			binding["unexpected"] = true
		},
	}
	for name, mutate := range mapCases {
		t.Run(name, func(t *testing.T) {
			body, err := json.Marshal(validDoc)
			if err != nil {
				t.Fatal(err)
			}
			var malformed map[string]any
			if err := json.Unmarshal(body, &malformed); err != nil {
				t.Fatal(err)
			}
			mutate(malformed)
			writeJSONForTest(t, recordPath, malformed)
			if _, err := store.Load(record.MissionID); err == nil {
				t.Fatalf("%s was accepted", name)
			}
			if err := os.WriteFile(recordPath, valid, 0o644); err != nil {
				t.Fatal(err)
			}
		})
	}

	rawCases := map[string]string{
		"duplicate Mission field": strings.Replace(
			string(valid),
			`"schema": "`+RecordSchema+`"`,
			`"schema": "`+RecordSchema+`", "schema": "`+RecordSchema+`"`,
			1,
		),
		"case-variant Mission field": strings.Replace(
			string(valid),
			`"schema": "`+RecordSchema+`"`,
			`"Schema": "`+RecordSchema+`"`,
			1,
		),
		"duplicate reference field": strings.Replace(
			string(valid),
			`"schema": "`+CorrelationChainReferenceSchema+`"`,
			`"schema": "`+CorrelationChainReferenceSchema+`", "schema": "`+CorrelationChainReferenceSchema+`"`,
			1,
		),
	}
	for name, malformed := range rawCases {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(recordPath, []byte(malformed), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Load(record.MissionID); err == nil {
				t.Fatalf("%s was accepted", name)
			}
			if err := os.WriteFile(recordPath, valid, 0o644); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMissionArchiveImportStrictlyDecodesRedactedCorrelationReference(t *testing.T) {
	_, record, _, _ := importCorrelationTestWorkflow(t, true)
	archive, err := BuildMissionArchive(record)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(archive)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	ref := raw["record"].(map[string]any)["correlation_chain_references"].([]any)[0].(map[string]any)
	delete(ref, "safe_to_execute")
	path := filepath.Join(t.TempDir(), "archive.json")
	writeJSONForTest(t, path, raw)

	if _, err := ImportMissionArchive(NewStore(t.TempDir()), path); err == nil {
		t.Fatal("archive import accepted a correlation reference missing required raw authority")
	}
}

func TestCorrelationChainRejectsDuplicateRolesAndLocators(t *testing.T) {
	dir := t.TempDir()
	record := correlationTestRecord()
	artifactPath := filepath.Join(dir, "artifact.json")
	writeJSONForTest(t, artifactPath, map[string]any{
		"schema":         "ao.atlas.workgraph.v0.1",
		"correlation_id": record.CorrelationID,
	})

	if _, err := BuildCorrelationChain(record, []CorrelationArtifactSpec{
		{Role: "atlas-workgraph", Path: artifactPath},
		{Role: "atlas-workgraph", Path: artifactPath},
	}); err == nil {
		t.Fatal("builder accepted duplicate roles")
	}
	if _, err := BuildCorrelationChain(record, []CorrelationArtifactSpec{
		{Role: "atlas-workgraph", Path: artifactPath},
		{Role: "workgraph-copy", Path: artifactPath},
	}); err == nil {
		t.Fatal("builder accepted duplicate artifact locators")
	}
}

func TestCorrelationChainRejectsMissingOrMismatchedParent(t *testing.T) {
	dir := t.TempDir()
	artifactPath := filepath.Join(dir, "artifact.json")
	writeJSONForTest(t, artifactPath, map[string]any{
		"schema":        "ao.foundry.run-link.v0.1",
		"parent_digest": "sha256:" + strings.Repeat("a", 64),
	})
	artifactPath = canonicalPathForTest(t, artifactPath)
	entry := CorrelationChainEntry{
		Role:          "foundry-run-link",
		ArtifactPath:  artifactPath,
		Digest:        digestFileForTest(t, artifactPath),
		Producer:      "ao-foundry",
		BindingMode:   CorrelationBindingDigestLink,
		ParentRole:    "atlas-workgraph",
		ParentDigest:  "sha256:" + strings.Repeat("a", 64),
		SafeToExecute: false,
	}
	base := CorrelationChain{
		Schema:        CorrelationChainSchema,
		MissionID:     correlationTestRecord().MissionID,
		CorrelationID: correlationTestRecord().CorrelationID,
		Entries:       []CorrelationChainEntry{entry},
	}

	path := filepath.Join(dir, "missing-parent.json")
	writeJSONForTest(t, path, base)
	if _, err := ValidateCorrelationChainFile(path); err == nil ||
		!strings.Contains(err.Error(), "parent") {
		t.Fatalf("missing parent accepted: %v", err)
	}

	parentPath := filepath.Join(dir, "parent.json")
	writeJSONForTest(t, parentPath, map[string]any{
		"schema":         "ao.atlas.workgraph.v0.1",
		"correlation_id": base.CorrelationID,
	})
	parentPath = canonicalPathForTest(t, parentPath)
	parent := CorrelationChainEntry{
		Role:             "atlas-workgraph",
		ArtifactPath:     parentPath,
		Digest:           digestFileForTest(t, parentPath),
		Producer:         "ao-atlas",
		BindingMode:      CorrelationBindingNativeField,
		NativeIdentifier: base.CorrelationID,
		NativeField:      "/correlation_id",
	}
	base.Entries = append(base.Entries, parent)
	path = filepath.Join(dir, "mismatched-parent.json")
	writeJSONForTest(t, path, base)
	if _, err := ValidateCorrelationChainFile(path); err == nil ||
		!strings.Contains(err.Error(), "parent_digest") {
		t.Fatalf("mismatched parent digest accepted: %v", err)
	}
}

func TestCorrelationChainValidationProvesArtifactProducerAndDigestLink(t *testing.T) {
	dir := t.TempDir()
	record := correlationTestRecord()
	parentPath := filepath.Join(dir, "parent.json")
	writeJSONForTest(t, parentPath, map[string]any{
		"schema":         "ao.atlas.workgraph.v0.1",
		"correlation_id": record.CorrelationID,
	})
	parentPath = canonicalPathForTest(t, parentPath)
	parentDigest := digestFileForTest(t, parentPath)
	childPath := filepath.Join(dir, "child.json")
	writeJSONForTest(t, childPath, map[string]any{
		"schema": "ao.foundry.result.v0.1",
		"status": "ready",
	})
	childPath = canonicalPathForTest(t, childPath)

	chain := CorrelationChain{
		Schema:        CorrelationChainSchema,
		MissionID:     record.MissionID,
		CorrelationID: record.CorrelationID,
		Entries: []CorrelationChainEntry{
			{
				Role:              "foundry-result",
				ArtifactPath:      childPath,
				Digest:            digestFileForTest(t, childPath),
				Producer:          "ao-foundry",
				BindingMode:       CorrelationBindingDigestLink,
				ParentRole:        "atlas-workgraph",
				ParentDigest:      parentDigest,
				ParentDigestField: "/parent_digest",
				SafeToExecute:     false,
			},
			{
				Role:             "atlas-workgraph",
				ArtifactPath:     parentPath,
				Digest:           parentDigest,
				Producer:         "ao-atlas",
				BindingMode:      CorrelationBindingNativeField,
				NativeIdentifier: record.CorrelationID,
				NativeField:      "/correlation_id",
				SafeToExecute:    false,
			},
		},
	}

	t.Run("fabricated digest link", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "chain.json")
		writeJSONForTest(t, path, chain)
		if _, err := ValidateCorrelationChainFile(path); err == nil ||
			!strings.Contains(err.Error(), "instance provenance") {
			t.Fatalf("digest link absent from child artifact was accepted: %v", err)
		}
	})

	t.Run("wrong producer", func(t *testing.T) {
		writeJSONForTest(t, childPath, map[string]any{
			"schema":        "ao.foundry.result.v0.1",
			"parent_digest": parentDigest,
			"status":        "ready",
		})
		tampered := chain
		tampered.Entries = append([]CorrelationChainEntry(nil), chain.Entries...)
		tampered.Entries[0].Digest = digestFileForTest(t, childPath)
		tampered.Entries[1].Producer = "ao-foundry"
		path := filepath.Join(t.TempDir(), "chain.json")
		writeJSONForTest(t, path, tampered)
		if _, err := ValidateCorrelationChainFile(path); err == nil ||
			!strings.Contains(err.Error(), "producer") {
			t.Fatalf("producer not derived from artifact schema was accepted: %v", err)
		}
	})
}

func TestCorrelationChainSupportsStackEvidenceConventions(t *testing.T) {
	dir := t.TempDir()
	record := correlationTestRecord()
	verificationPath := filepath.Join(dir, "verification.json")
	writeJSONForTest(t, verificationPath, map[string]any{
		"schema": "ao.stack.month3.issue-route-verification-bundle.v1",
		"status": "passed",
	})
	if _, err := BuildCorrelationChain(record, []CorrelationArtifactSpec{{
		Role: "verification",
		Path: verificationPath,
	}}); err == nil || !strings.Contains(err.Error(), "instance provenance") {
		t.Fatalf("schema-only artifact was accepted as instance provenance: %v", err)
	}
	writeJSONForTest(t, verificationPath, map[string]any{
		"schema":          "ao.stack.month3.issue-route-verification-bundle.v1",
		"status":          "passed",
		"verification_id": "issue-route-verification-001",
	})
	verificationDigest := digestFileForTest(t, verificationPath)
	draftPath := filepath.Join(dir, "draft.json")
	writeJSONForTest(t, draftPath, map[string]any{
		"schema_version": "ao2.github-draft-pr-evidence.v1",
		"repair": map[string]any{
			"verification_sha256": strings.TrimPrefix(verificationDigest, "sha256:"),
			"provenance": map[string]any{
				"request_id": "request-001",
				"result_id":  "result-001",
			},
		},
	})
	requestPath := filepath.Join(dir, "request.json")
	writeJSONForTest(t, requestPath, map[string]any{
		"contract_version": "ao.foundry.request-readback.v1",
		"provenance": map[string]any{
			"request_id": "request-002",
		},
	})
	actionPath := filepath.Join(dir, "action.json")
	rawActionDigest := strings.Repeat("a", 64)
	writeJSONForTest(t, actionPath, map[string]any{
		"schema_version": "ao2.draft-action-readback.v1",
		"approval": map[string]any{
			"action_digest": rawActionDigest,
		},
	})

	chain, err := BuildCorrelationChain(record, []CorrelationArtifactSpec{
		{Role: "verification", Path: verificationPath},
		{Role: "draft", Path: draftPath},
		{Role: "request", Path: requestPath},
		{Role: "action", Path: actionPath},
	})
	if err != nil {
		t.Fatal(err)
	}
	entries := make(map[string]CorrelationChainEntry, len(chain.Entries))
	for _, entry := range chain.Entries {
		entries[entry.Role] = entry
	}
	if entries["verification"].NativeField != "/verification_id" ||
		entries["verification"].NativeIdentifier != "issue-route-verification-001" {
		t.Fatalf("native verification identifier was not preserved: %+v", entries["verification"])
	}
	if entries["draft"].Producer != "ao2" ||
		entries["draft"].BindingMode != CorrelationBindingDigestLink ||
		entries["draft"].ParentRole != "verification" ||
		entries["draft"].ParentDigest != verificationDigest ||
		entries["draft"].ParentDigestField != "/repair/verification_sha256" {
		t.Fatalf("raw nested parent digest was not resolved exactly: %+v", entries["draft"])
	}
	if entries["request"].Producer != "ao-foundry" ||
		entries["request"].NativeField != "/provenance/request_id" ||
		entries["request"].NativeIdentifier != "request-002" {
		t.Fatalf("nested request identifier was not resolved: %+v", entries["request"])
	}
	if entries["action"].Producer != "ao2" ||
		entries["action"].NativeField != "/approval/action_digest" ||
		entries["action"].NativeIdentifier != "sha256:"+rawActionDigest {
		t.Fatalf("nested action digest was not normalized: %+v", entries["action"])
	}

	chainPath := filepath.Join(dir, "chain.json")
	writeJSONForTest(t, chainPath, chain)
	if _, err := ValidateCorrelationChainFile(chainPath); err != nil {
		t.Fatalf("stack evidence conventions failed validation: %v", err)
	}

	tampered := chain
	tampered.Entries = append([]CorrelationChainEntry(nil), chain.Entries...)
	for i := range tampered.Entries {
		if tampered.Entries[i].Role == "request" {
			tampered.Entries[i].NativeField = "/provenance/result_id"
		}
	}
	writeJSONForTest(t, chainPath, tampered)
	if _, err := ValidateCorrelationChainFile(chainPath); err == nil ||
		!strings.Contains(err.Error(), "native") {
		t.Fatalf("validator did not resolve the exact nested native path: %v", err)
	}
}

func TestCorrelationChainBindsAO2EvidencePackToCanonicalRunID(t *testing.T) {
	record := correlationTestRecord()
	path := filepath.Join(t.TempDir(), "ao2-evidence-pack.json")
	writeJSONForTest(t, path, map[string]any{
		"schema_version": "ao2.evidence-pack.v1",
		"run_id":         "windows-qualification-demo",
		"workflow_id":    "risky-pr-run@0.1.0",
		"verdict":        "accepted",
		"workflow_tasks": []any{
			map[string]any{"task_id": "implementer"},
		},
	})

	chain, err := BuildCorrelationChain(record, []CorrelationArtifactSpec{{
		Role: "ao2-evidence",
		Path: path,
	}})
	if err != nil {
		t.Fatalf("AO2 evidence pack was rejected: %v", err)
	}
	if len(chain.Entries) != 1 {
		t.Fatalf("entry count = %d, want 1", len(chain.Entries))
	}
	entry := chain.Entries[0]
	if entry.Producer != "ao2" ||
		entry.BindingMode != CorrelationBindingNativeField ||
		entry.NativeField != "/run_id" ||
		entry.NativeIdentifier != "windows-qualification-demo" {
		t.Fatalf("AO2 evidence identity was not bound to /run_id: %+v", entry)
	}
}

func TestCorrelationChainRejectsInvalidAO2EvidencePackRunIdentity(t *testing.T) {
	record := correlationTestRecord()

	for name, document := range map[string]map[string]any{
		"missing top-level run_id": {
			"schema_version": "ao2.evidence-pack.v1",
			"workflow_id":    "risky-pr-run@0.1.0",
		},
		"conflicting nested run_id": {
			"schema_version": "ao2.evidence-pack.v1",
			"run_id":         "windows-qualification-demo",
			"workflow_id":    "risky-pr-run@0.1.0",
			"runtime_contract": map[string]any{
				"run_id": "different-run",
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "ao2-evidence-pack.json")
			writeJSONForTest(t, path, document)
			_, err := BuildCorrelationChain(record, []CorrelationArtifactSpec{{
				Role: "ao2-evidence",
				Path: path,
			}})
			if err == nil || !strings.Contains(err.Error(), "AO2 evidence pack") {
				t.Fatalf("invalid AO2 run identity was accepted: %v", err)
			}
		})
	}
}

func TestCorrelationChainRequiresAO2RunIdentityBeforeDigestLinkBinding(t *testing.T) {
	record := correlationTestRecord()
	dir := t.TempDir()
	parentPath := filepath.Join(dir, "parent.json")
	writeJSONForTest(t, parentPath, map[string]any{
		"schema":         "ao.atlas.parent.v1",
		"correlation_id": record.CorrelationID,
	})
	parentDigest := digestFileForTest(t, parentPath)

	for name, document := range map[string]map[string]any{
		"missing top-level run_id": {
			"schema_version": "ao2.evidence-pack.v1",
			"parent_sha256":  parentDigest,
		},
		"conflicting nested run_id": {
			"schema_version": "ao2.evidence-pack.v1",
			"run_id":         "windows-qualification-demo",
			"parent_sha256":  parentDigest,
			"runtime_contract": map[string]any{
				"run_id": "different-run",
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(dir, strings.ReplaceAll(name, " ", "-")+".json")
			writeJSONForTest(t, path, document)
			_, err := BuildCorrelationChain(record, []CorrelationArtifactSpec{
				{Role: "parent", Path: parentPath},
				{Role: "ao2-evidence", Path: path},
			})
			if err == nil || !strings.Contains(err.Error(), "AO2 evidence pack") {
				t.Fatalf("digest-linked AO2 evidence bypassed run identity validation: %v", err)
			}
		})
	}

	validPath := filepath.Join(dir, "valid.json")
	writeJSONForTest(t, validPath, map[string]any{
		"schema_version": "ao2.evidence-pack.v1",
		"run_id":         "windows-qualification-demo",
		"parent_sha256":  parentDigest,
	})
	chain, err := BuildCorrelationChain(record, []CorrelationArtifactSpec{
		{Role: "parent", Path: parentPath},
		{Role: "ao2-evidence", Path: validPath},
	})
	if err != nil {
		t.Fatal(err)
	}
	entries := make(map[string]CorrelationChainEntry, len(chain.Entries))
	for _, entry := range chain.Entries {
		entries[entry.Role] = entry
	}
	if entries["ao2-evidence"].BindingMode != CorrelationBindingDigestLink ||
		entries["ao2-evidence"].ParentRole != "parent" {
		t.Fatalf("validated AO2 digest link did not retain parent precedence: %+v", entries["ao2-evidence"])
	}
}

func TestCorrelationChainValidatesEveryAO2RunIDValue(t *testing.T) {
	record := correlationTestRecord()

	validPath := filepath.Join(t.TempDir(), "same-run-ids.json")
	writeJSONForTest(t, validPath, map[string]any{
		"schema_version": "ao2.evidence-pack.v1",
		"run_id":         "windows-qualification-demo",
		"runtime_contract": map[string]any{
			"run_id": "windows-qualification-demo",
			"events": []any{
				map[string]any{"run_id": "windows-qualification-demo"},
			},
		},
	})
	if _, err := BuildCorrelationChain(record, []CorrelationArtifactSpec{{Role: "ao2-evidence", Path: validPath}}); err != nil {
		t.Fatalf("matching nested AO2 run_id strings were rejected: %v", err)
	}

	for name, value := range map[string]any{
		"empty string": "",
		"number":       1,
		"boolean":      true,
		"null":         nil,
		"object":       map[string]any{"value": "windows-qualification-demo"},
		"array":        []any{"windows-qualification-demo"},
	} {
		t.Run("top-level "+name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "invalid-top-level-run-id.json")
			writeJSONForTest(t, path, map[string]any{
				"schema_version": "ao2.evidence-pack.v1",
				"run_id":         value,
			})
			_, err := BuildCorrelationChain(record, []CorrelationArtifactSpec{{Role: "ao2-evidence", Path: path}})
			if err == nil || !strings.Contains(err.Error(), "AO2 evidence pack run_id") {
				t.Fatalf("AO2 evidence accepted top-level %s run_id: %v", name, err)
			}
		})
	}

	for name, value := range map[string]any{
		"number":  1,
		"boolean": true,
		"null":    nil,
		"object":  map[string]any{"value": "windows-qualification-demo"},
		"array":   []any{"windows-qualification-demo"},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "invalid-run-id.json")
			writeJSONForTest(t, path, map[string]any{
				"schema_version": "ao2.evidence-pack.v1",
				"run_id":         "windows-qualification-demo",
				"runtime_contract": map[string]any{
					"events": []any{map[string]any{"run_id": value}},
				},
			})
			_, err := BuildCorrelationChain(record, []CorrelationArtifactSpec{{Role: "ao2-evidence", Path: path}})
			if err == nil || !strings.Contains(err.Error(), "AO2 evidence pack run_id") {
				t.Fatalf("AO2 evidence accepted nested %s run_id: %v", name, err)
			}
		})
	}
}

func TestCorrelationChainRejectsSchemaOnlyAO2EvidencePack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schema-only.json")
	writeJSONForTest(t, path, map[string]any{"schema_version": "ao2.evidence-pack.v1"})
	_, err := BuildCorrelationChain(correlationTestRecord(), []CorrelationArtifactSpec{{
		Role: "ao2-evidence",
		Path: path,
	}})
	if err == nil || !strings.Contains(err.Error(), "AO2 evidence pack requires a top-level run_id") {
		t.Fatalf("schema-only AO2 evidence did not return the required run_id error: %v", err)
	}
}

func TestCorrelationChainRejectsAmbiguousBindingCandidates(t *testing.T) {
	record := correlationTestRecord()

	t.Run("parent links", func(t *testing.T) {
		dir := t.TempDir()
		firstPath := filepath.Join(dir, "first.json")
		secondPath := filepath.Join(dir, "second.json")
		writeJSONForTest(t, firstPath, map[string]any{
			"schema":         "ao.atlas.first.v1",
			"correlation_id": record.CorrelationID,
		})
		writeJSONForTest(t, secondPath, map[string]any{
			"schema":         "ao.atlas.second.v1",
			"correlation_id": record.CorrelationID,
		})
		childPath := filepath.Join(dir, "child.json")
		writeJSONForTest(t, childPath, map[string]any{
			"schema_version": "ao2.child.v1",
			"first_sha256":   strings.TrimPrefix(digestFileForTest(t, firstPath), "sha256:"),
			"second_digest":  digestFileForTest(t, secondPath),
		})

		_, err := BuildCorrelationChain(record, []CorrelationArtifactSpec{
			{Role: "first", Path: firstPath},
			{Role: "second", Path: secondPath},
			{Role: "child", Path: childPath},
		})
		if err == nil || !strings.Contains(err.Error(), "ambiguous") {
			t.Fatalf("ambiguous parent links were accepted: %v", err)
		}
	})

	t.Run("native identifiers", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "artifact.json")
		writeJSONForTest(t, path, map[string]any{
			"schema": "ao.foundry.result.v1",
			"provenance": map[string]any{
				"request_id": "request-001",
				"result_id":  "result-001",
			},
		})
		_, err := BuildCorrelationChain(record, []CorrelationArtifactSpec{{
			Role: "result",
			Path: path,
		}})
		if err == nil || !strings.Contains(err.Error(), "ambiguous") {
			t.Fatalf("ambiguous native identifiers were accepted: %v", err)
		}
	})

	t.Run("mixed top-level and nested native identifiers", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "artifact.json")
		writeJSONForTest(t, path, map[string]any{
			"schema":    "ao.foundry.result.v1",
			"result_id": "result-001",
			"provenance": map[string]any{
				"request_id": "request-001",
			},
		})
		_, err := BuildCorrelationChain(record, []CorrelationArtifactSpec{{
			Role: "result",
			Path: path,
		}})
		if err == nil || !strings.Contains(err.Error(), "ambiguous") {
			t.Fatalf("mixed top-level and nested identifiers were accepted: %v", err)
		}
	})

	for name, artifact := range map[string]map[string]any{
		"correlation and component identity": {
			"schema":         "ao.foundry.result.v1",
			"correlation_id": record.CorrelationID,
			"provenance": map[string]any{
				"request_id": "request-001",
			},
		},
		"Mission and component identity": {
			"schema":     "ao.foundry.result.v1",
			"mission_id": record.MissionID,
			"result_id":  "result-001",
		},
		"Mission and correlation identity": {
			"schema":         "ao.foundry.result.v1",
			"mission_id":     record.MissionID,
			"correlation_id": record.CorrelationID,
		},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "artifact.json")
			writeJSONForTest(t, path, artifact)
			_, err := BuildCorrelationChain(record, []CorrelationArtifactSpec{{
				Role: "result",
				Path: path,
			}})
			if err == nil || !strings.Contains(err.Error(), "ambiguous") {
				t.Fatalf("Mission identity preference accepted ambiguous provenance: %v", err)
			}
		})
	}

	for name, artifact := range map[string]map[string]any{
		"dotted key collision": {
			"schema":                "ao.foundry.result.v1",
			"provenance.request_id": "literal-001",
			"provenance": map[string]any{
				"request_id": "nested-001",
			},
		},
		"bracketed key collision": {
			"schema":              "ao.foundry.result.v1",
			"items[0].request_id": "literal-001",
			"items": []any{
				map[string]any{"request_id": "nested-001"},
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "artifact.json")
			writeJSONForTest(t, path, artifact)
			_, err := BuildCorrelationChain(record, []CorrelationArtifactSpec{{
				Role: "result",
				Path: path,
			}})
			if err == nil || !strings.Contains(err.Error(), "ambiguous") {
				t.Fatalf("colliding native paths were accepted: %v", err)
			}
		})
	}
}

func TestCorrelationChainUsesUnambiguousJSONPointerProvenancePaths(t *testing.T) {
	record := correlationTestRecord()
	for name, test := range map[string]struct {
		artifact map[string]any
		wantPath string
	}{
		"nested object": {
			artifact: map[string]any{
				"schema": "ao.foundry.result.v1",
				"provenance": map[string]any{
					"request_id": "request-001",
				},
			},
			wantPath: "/provenance/request_id",
		},
		"literal dotted key": {
			artifact: map[string]any{
				"schema":                "ao.foundry.result.v1",
				"provenance.request_id": "request-001",
			},
			wantPath: "/provenance.request_id",
		},
		"nested array": {
			artifact: map[string]any{
				"schema": "ao.foundry.result.v1",
				"items": []any{
					map[string]any{"request_id": "request-001"},
				},
			},
			wantPath: "/items/0/request_id",
		},
		"literal bracketed key": {
			artifact: map[string]any{
				"schema":              "ao.foundry.result.v1",
				"items[0].request_id": "request-001",
			},
			wantPath: "/items[0].request_id",
		},
		"escaped slash and tilde": {
			artifact: map[string]any{
				"schema":                 "ao.foundry.result.v1",
				"provenance/request~_id": "request-001",
			},
			wantPath: "/provenance~1request~0_id",
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			artifactPath := filepath.Join(dir, "artifact.json")
			writeJSONForTest(t, artifactPath, test.artifact)
			chain, err := BuildCorrelationChain(record, []CorrelationArtifactSpec{{
				Role: "result",
				Path: artifactPath,
			}})
			if err != nil {
				t.Fatal(err)
			}
			if got := chain.Entries[0].NativeField; got != test.wantPath {
				t.Fatalf("native field path=%q want %q", got, test.wantPath)
			}
			chainPath := filepath.Join(dir, "chain.json")
			writeJSONForTest(t, chainPath, chain)
			if _, err := ValidateCorrelationChainFile(chainPath); err != nil {
				t.Fatalf("JSON Pointer chain did not validate: %v", err)
			}
		})
	}
}

func TestGenericSchemaValidationEnforcesCorrelationAlternatives(t *testing.T) {
	dir := t.TempDir()
	record := correlationTestRecord()
	artifactPath := filepath.Join(dir, "artifact.json")
	writeJSONForTest(t, artifactPath, map[string]any{
		"schema":         "ao.atlas.workgraph.v1",
		"correlation_id": record.CorrelationID,
	})
	chain, err := BuildCorrelationChain(record, []CorrelationArtifactSpec{{
		Role: "artifact",
		Path: artifactPath,
	}})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(chain)
	if err != nil {
		t.Fatal(err)
	}

	mutations := map[string]func(map[string]any){
		"both locators": func(document map[string]any) {
			document["entries"].([]any)[0].(map[string]any)["artifact_name"] = "artifact.json"
		},
		"native with parent provenance": func(document map[string]any) {
			entry := document["entries"].([]any)[0].(map[string]any)
			entry["parent_role"] = "parent"
			entry["parent_digest"] = "sha256:" + strings.Repeat("a", 64)
			entry["parent_digest_field"] = "/parent_digest"
		},
		"digest link with native provenance": func(document map[string]any) {
			entry := document["entries"].([]any)[0].(map[string]any)
			entry["binding_mode"] = CorrelationBindingDigestLink
			entry["parent_role"] = "parent"
			entry["parent_digest"] = "sha256:" + strings.Repeat("a", 64)
			entry["parent_digest_field"] = "/parent_digest"
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			var document map[string]any
			if err := json.Unmarshal(body, &document); err != nil {
				t.Fatal(err)
			}
			mutate(document)
			path := filepath.Join(t.TempDir(), "chain.json")
			writeJSONForTest(t, path, document)
			if _, err := ValidateContractFile(path); err == nil {
				t.Fatalf("generic schema validator accepted %s", name)
			}
		})
	}
}

func TestCorrelationReferenceAndMissionRecordSchemasAreStrict(t *testing.T) {
	dir := t.TempDir()
	record := correlationTestRecord()
	artifactPath := filepath.Join(dir, "artifact.json")
	writeJSONForTest(t, artifactPath, map[string]any{
		"schema":         "ao.atlas.workgraph.v1",
		"correlation_id": record.CorrelationID,
	})
	chain, err := BuildCorrelationChain(record, []CorrelationArtifactSpec{{
		Role: "artifact",
		Path: artifactPath,
	}})
	if err != nil {
		t.Fatal(err)
	}
	chainDigest, err := correlationChainDigest(chain)
	if err != nil {
		t.Fatal(err)
	}
	reference := correlationChainReference(chain, chainDigest)
	referencePath := filepath.Join(dir, "reference.json")
	writeJSONForTest(t, referencePath, reference)
	if _, err := ValidateContractFile(referencePath); err != nil {
		t.Fatalf("valid persisted reference schema rejected: %v", err)
	}
	if _, err := ValidateContractFile(filepath.Join(
		"..", "..", "examples", "valid", "correlation-chain-reference.json",
	)); err != nil {
		t.Fatalf("persisted reference example rejected: %v", err)
	}

	referenceBody, err := json.Marshal(reference)
	if err != nil {
		t.Fatal(err)
	}
	var malformedReference map[string]any
	if err := json.Unmarshal(referenceBody, &malformedReference); err != nil {
		t.Fatal(err)
	}
	malformedReference["entries"].([]any)[0].(map[string]any)["unknown"] = true
	writeJSONForTest(t, referencePath, malformedReference)
	if _, err := ValidateContractFile(referencePath); err == nil {
		t.Fatal("persisted reference schema accepted an unknown entry field")
	}

	missionPath := filepath.Join(dir, "mission.json")
	writeJSONForTest(t, missionPath, map[string]any{
		"schema":           RecordSchema,
		"mission_id":       record.MissionID,
		"objective_digest": "sha256:" + strings.Repeat("a", 64),
		"status":           "active",
		"created_at_utc":   "2026-07-20T00:00:00Z",
		"current_route":    "ao-atlas",
		"correlation_chain_references": []any{
			map[string]any{"schema": CorrelationChainReferenceSchema},
		},
		"correlated_imports": []any{
			map[string]any{"role": "artifact"},
		},
	})
	if _, err := ValidateContractFile(missionPath); err == nil {
		t.Fatal("Mission record schema accepted untyped correlation state items")
	}
}

func TestMissionRecordPublicSchemaMatchesStrictRuntimeFields(t *testing.T) {
	schemaPath := filepath.Join("..", "..", "docs", "contracts", "mission-record-v0.1.schema.json")
	body, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(body, &schema); err != nil {
		t.Fatal(err)
	}
	if schema["additionalProperties"] != false {
		t.Fatal("Mission public schema does not reject unknown top-level fields")
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatal("Mission public schema properties are missing")
	}
	runtimeFields := map[string]struct{}{}
	recordType := reflect.TypeOf(Record{})
	for i := 0; i < recordType.NumField(); i++ {
		name := strings.Split(recordType.Field(i).Tag.Get("json"), ",")[0]
		if name != "" && name != "-" {
			runtimeFields[name] = struct{}{}
		}
	}
	for name := range runtimeFields {
		if _, documented := properties[name]; !documented {
			t.Errorf("runtime Mission field %q is absent from public schema", name)
		}
	}
	for name := range properties {
		if _, supported := runtimeFields[name]; !supported {
			t.Errorf("public Mission field %q is not supported by strict runtime decoding", name)
		}
	}
	routeHistory, ok := properties["route_history"].(map[string]any)
	if !ok || routeHistory["type"] != "array" {
		t.Fatal("route_history is not documented as an array")
	}
	imports := properties["correlated_imports"].(map[string]any)
	items := imports["items"].(map[string]any)
	importProperties := items["properties"].(map[string]any)
	required := map[string]struct{}{}
	for _, value := range items["required"].([]any) {
		required[value.(string)] = struct{}{}
	}
	for _, field := range []string{"locator_state", "locator_digest"} {
		_, documented := importProperties[field]
		_, requiredField := required[field]
		if !documented || !requiredField {
			t.Errorf("correlated import field %q is not required and documented", field)
		}
	}
	if _, present := importProperties["archive_source_locator_digest"]; !present {
		t.Error("archive_source_locator_digest is not documented")
	}
}

func TestCorrelationContractsValidateWithoutSourceTreeSchemaLookup(t *testing.T) {
	dir := t.TempDir()
	record := correlationTestRecord()
	artifactPath := filepath.Join(dir, "artifact.json")
	writeJSONForTest(t, artifactPath, map[string]any{
		"schema":         "ao.atlas.workgraph.v1",
		"correlation_id": record.CorrelationID,
	})
	chain, err := BuildCorrelationChain(record, []CorrelationArtifactSpec{{
		Role: "artifact",
		Path: artifactPath,
	}})
	if err != nil {
		t.Fatal(err)
	}
	chainDigest, err := correlationChainDigest(chain)
	if err != nil {
		t.Fatal(err)
	}
	reference := correlationChainReference(chain, chainDigest)
	chainPath := filepath.Join(dir, "chain.json")
	writeJSONForTest(t, chainPath, chain)
	validPath := filepath.Join(dir, "reference.json")
	writeJSONForTest(t, validPath, reference)
	record.ObjectiveDigest = "sha256:" + strings.Repeat("a", 64)
	record.Objective = "cwd-independent intrinsic Mission validation"
	record.Status = "active"
	record.CreatedAtUTC = "2026-07-20T00:00:00Z"
	record.UpdatedAtUTC = record.CreatedAtUTC
	record.CurrentRoute = "ao-atlas"
	record.CurrentPhase = "handoff_required"
	record.Blockers = []string{}
	record.ArtifactRefs = []ArtifactRef{}
	record.Steps = []ContinuationStep{}
	record.CorrelationChainReferences = []CorrelationChainReference{reference}
	binding := CorrelatedImportBinding{
		Role:            chain.Entries[0].Role,
		Digest:          chain.Entries[0].Digest,
		ArtifactPath:    chain.Entries[0].ArtifactPath,
		LocatorState:    correlationLocatorStateLive,
		ChainDigest:     reference.ChainDigest,
		ReferenceDigest: reference.ReferenceDigest,
	}
	binding.LocatorDigest = correlatedImportLocatorDigest(binding)
	record.CorrelatedImports = []CorrelatedImportBinding{binding}
	missionPath := filepath.Join(dir, "mission.json")
	writeJSONForTest(t, missionPath, record)

	body, err := json.Marshal(reference)
	if err != nil {
		t.Fatal(err)
	}
	var malformed map[string]any
	if err := json.Unmarshal(body, &malformed); err != nil {
		t.Fatal(err)
	}
	entry := malformed["entries"].([]any)[0].(map[string]any)
	entry["parent_role"] = "foreign-parent"
	invalidPath := filepath.Join(dir, "invalid-reference.json")
	writeJSONForTest(t, invalidPath, malformed)

	originalCWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	unrelatedCWD := t.TempDir()
	if err := os.Chdir(unrelatedCWD); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(originalCWD); err != nil {
			t.Errorf("restore cwd: %v", err)
		}
	})

	for _, path := range []string{chainPath, validPath, missionPath} {
		if result, err := ValidateContractFile(path); err != nil || len(result.Blockers) != 0 {
			t.Fatalf("valid intrinsic contract %q rejected away from source tree: result=%+v err=%v", path, result, err)
		}
	}
	result, err := ValidateContractFile(invalidPath)
	if err == nil || result.Status != "blocked" || len(result.Blockers) == 0 {
		t.Fatalf("malformed reference passed without source schema: result=%+v err=%v", result, err)
	}
}

func TestCorrelationChainRejectsDigestLinkCycle(t *testing.T) {
	dir := t.TempDir()
	firstPath := filepath.Join(dir, "first.json")
	secondPath := filepath.Join(dir, "second.json")
	writeJSONForTest(t, firstPath, map[string]any{"schema": "ao.atlas.first.v0.1"})
	writeJSONForTest(t, secondPath, map[string]any{"schema": "ao.foundry.second.v0.1"})
	firstPath = canonicalPathForTest(t, firstPath)
	secondPath = canonicalPathForTest(t, secondPath)
	firstDigest := digestFileForTest(t, firstPath)
	secondDigest := digestFileForTest(t, secondPath)
	chain := CorrelationChain{
		Schema:        CorrelationChainSchema,
		MissionID:     correlationTestRecord().MissionID,
		CorrelationID: correlationTestRecord().CorrelationID,
		Entries: []CorrelationChainEntry{
			{
				Role:          "first",
				ArtifactPath:  firstPath,
				Digest:        firstDigest,
				Producer:      "ao-atlas",
				BindingMode:   CorrelationBindingDigestLink,
				ParentRole:    "second",
				ParentDigest:  secondDigest,
				SafeToExecute: false,
			},
			{
				Role:          "second",
				ArtifactPath:  secondPath,
				Digest:        secondDigest,
				Producer:      "ao-foundry",
				BindingMode:   CorrelationBindingDigestLink,
				ParentRole:    "first",
				ParentDigest:  firstDigest,
				SafeToExecute: false,
			},
		},
	}
	path := filepath.Join(dir, "cycle.json")
	writeJSONForTest(t, path, chain)

	if _, err := ValidateCorrelationChainFile(path); err == nil {
		t.Fatalf("digest-link cycle accepted: %v", err)
	}
}

func TestCorrelationChainRejectsAuthorityWidening(t *testing.T) {
	dir := t.TempDir()
	record := correlationTestRecord()
	artifactPath := filepath.Join(dir, "artifact.json")
	writeJSONForTest(t, artifactPath, map[string]any{
		"schema":         "ao.atlas.workgraph.v0.1",
		"correlation_id": record.CorrelationID,
	})
	chain, err := BuildCorrelationChain(record, []CorrelationArtifactSpec{{
		Role: "atlas-workgraph",
		Path: artifactPath,
	}})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(chain)
	if err != nil {
		t.Fatal(err)
	}

	for _, field := range []string{
		"safe_to_execute",
		"executes_work",
		"approves_work",
		"mutates_repositories",
		"widens_policy",
		"publishes_artifacts",
	} {
		t.Run(field, func(t *testing.T) {
			var doc map[string]any
			if err := json.Unmarshal(body, &doc); err != nil {
				t.Fatal(err)
			}
			doc[field] = true
			path := filepath.Join(t.TempDir(), "chain.json")
			writeJSONForTest(t, path, doc)
			if _, err := ValidateCorrelationChainFile(path); err == nil {
				t.Fatalf("chain accepted %s=true", field)
			}

			if err := json.Unmarshal(body, &doc); err != nil {
				t.Fatal(err)
			}
			doc["entries"].([]any)[0].(map[string]any)[field] = true
			writeJSONForTest(t, path, doc)
			if _, err := ValidateCorrelationChainFile(path); err == nil {
				t.Fatalf("entry accepted %s=true", field)
			}
		})
	}
}

func TestCorrelationChainRejectsUnsafeArtifactFiles(t *testing.T) {
	dir := t.TempDir()
	record := correlationTestRecord()
	regularPath := filepath.Join(dir, "regular.json")
	writeJSONForTest(t, regularPath, map[string]any{
		"schema":         "ao.atlas.workgraph.v0.1",
		"correlation_id": record.CorrelationID,
	})
	symlinkPath := filepath.Join(dir, "symlink.json")
	createTestSymlink(t, regularPath, symlinkPath)
	oversizedPath := filepath.Join(dir, "oversized.json")
	oversized, err := os.Create(oversizedPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := oversized.Truncate(maxCorrelationArtifactBytes + 1); err != nil {
		oversized.Close()
		t.Fatal(err)
	}
	if err := oversized.Close(); err != nil {
		t.Fatal(err)
	}

	for name, path := range map[string]string{
		"missing":   filepath.Join(dir, "missing.json"),
		"directory": dir,
		"symlink":   symlinkPath,
		"oversized": oversizedPath,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := BuildCorrelationChain(record, []CorrelationArtifactSpec{{
				Role: "atlas-workgraph",
				Path: path,
			}}); err == nil {
				t.Fatalf("%s artifact accepted", name)
			}
		})
	}
}

func TestCorrelatedImportRequiresArtifactRoleAndDigestWithoutMutation(t *testing.T) {
	dir := t.TempDir()
	store, record := startCorrelationTestMission(t, filepath.Join(dir, "home"))
	artifactPath := filepath.Join(dir, "authorization.json")
	writeJSONForTest(t, artifactPath, map[string]any{
		"schema":           "ao.blueprint.build-authorization.v0.1",
		"status":           "ready",
		"authorization_id": "authorization-001",
	})
	chain, err := BuildCorrelationChain(record, []CorrelationArtifactSpec{{
		Role: "different-role",
		Path: artifactPath,
	}})
	if err != nil {
		t.Fatal(err)
	}
	chainPath := filepath.Join(dir, "chain.json")
	writeJSONForTest(t, chainPath, chain)

	if _, err := ImportArtifactWithCorrelationChain(
		store,
		record.MissionID,
		"blueprint-authorization",
		artifactPath,
		chainPath,
	); err == nil {
		t.Fatal("import accepted an artifact role absent from the chain")
	}
	after, err := store.Load(record.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.ArtifactRefs) != 0 ||
		len(after.CorrelatedImports) != 0 ||
		len(after.CorrelationChainReferences) != 0 {
		t.Fatalf("rejected correlated import mutated Mission state: %+v", after)
	}
}

func TestCorrelatedImportPersistsDigestBoundChainReference(t *testing.T) {
	dir := t.TempDir()
	store, record := startCorrelationTestMission(t, filepath.Join(dir, "home"))
	artifactPath := filepath.Join(dir, "authorization.json")
	writeJSONForTest(t, artifactPath, map[string]any{
		"schema":           "ao.blueprint.build-authorization.v0.1",
		"status":           "ready",
		"authorization_id": "authorization-002",
	})
	chain, err := BuildCorrelationChain(record, []CorrelationArtifactSpec{{
		Role: "blueprint-authorization",
		Path: artifactPath,
	}})
	if err != nil {
		t.Fatal(err)
	}
	chainPath := filepath.Join(dir, "chain.json")
	writeJSONForTest(t, chainPath, chain)

	if _, err := ImportArtifact(store, record.MissionID, "blueprint-authorization", artifactPath); err == nil {
		t.Fatal("legacy correlated import accepted missing artifact correlation_id")
	}
	readback, err := ImportArtifactWithCorrelationChain(
		store,
		record.MissionID,
		"blueprint-authorization",
		artifactPath,
		chainPath,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !validSHA256Digest(readback.CorrelationChainDigest) {
		t.Fatalf("readback lacks correlation chain digest: %+v", readback)
	}
	after, err := store.Load(record.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.CorrelatedImports) != 1 ||
		len(after.CorrelationChainReferences) != 1 ||
		after.CorrelatedImports[0].Role != "blueprint-authorization" ||
		after.CorrelatedImports[0].Digest != readback.Artifact.Digest ||
		after.CorrelatedImports[0].ChainDigest != readback.CorrelationChainDigest ||
		after.CorrelationChainReferences[0].ChainDigest != readback.CorrelationChainDigest ||
		!validSHA256Digest(after.CorrelationChainReferences[0].ReferenceDigest) ||
		after.CorrelatedImports[0].ReferenceDigest != after.CorrelationChainReferences[0].ReferenceDigest {
		t.Fatalf("Mission did not persist digest-bound correlation state: %+v", after)
	}
}

func TestCorrelationEvidenceImportIsNeutralAndRoleBound(t *testing.T) {
	dir := t.TempDir()
	store, record := startCorrelationTestMission(t, filepath.Join(dir, "home"))
	paths := map[string]string{
		"authority-and-approval": filepath.Join(dir, "authority.json"),
		"draft-pr-evidence":      filepath.Join(dir, "draft.json"),
	}
	writeJSONForTest(t, paths["authority-and-approval"], map[string]any{
		"schema":         "ao.stack.authority-and-approval.v1",
		"correlation_id": record.CorrelationID,
	})
	writeJSONForTest(t, paths["draft-pr-evidence"], map[string]any{
		"schema_version": "ao2.github-draft-pr-evidence.v1",
		"evidence_id":    "draft-pr-evidence-001",
	})
	chain, err := BuildCorrelationChain(record, []CorrelationArtifactSpec{
		{Role: "authority-and-approval", Path: paths["authority-and-approval"]},
		{Role: "draft-pr-evidence", Path: paths["draft-pr-evidence"]},
	})
	if err != nil {
		t.Fatal(err)
	}
	chainPath := filepath.Join(dir, "chain.json")
	writeJSONForTest(t, chainPath, chain)

	for name, args := range map[string][3]string{
		"missing chain": {"", "authority-and-approval", paths["authority-and-approval"]},
		"missing role":  {chainPath, "", paths["authority-and-approval"]},
		"wrong role":    {chainPath, "draft-pr-evidence", paths["authority-and-approval"]},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ImportCorrelationEvidence(
				store,
				record.MissionID,
				args[2],
				args[0],
				args[1],
			); err == nil {
				t.Fatalf("%s was accepted", name)
			}
		})
	}
	if _, err := ImportArtifactWithCorrelationChain(
		store,
		record.MissionID,
		"blueprint-authorization",
		paths["authority-and-approval"],
		chainPath,
	); err == nil {
		t.Fatal("legacy semantic import accepted a differently named chain role")
	}

	first, err := ImportCorrelationEvidence(
		store,
		record.MissionID,
		paths["authority-and-approval"],
		chainPath,
		"authority-and-approval",
	)
	if err != nil {
		t.Fatal(err)
	}
	if first.Kind != "correlation-evidence" ||
		first.Artifact.Kind != "correlation-evidence" ||
		!validSHA256Digest(first.CorrelationChainDigest) {
		t.Fatalf("neutral import readback is not chain-bound: %+v", first)
	}
	afterFirst, err := store.Load(record.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	assertOnlyCorrelationEvidenceChanged(t, record, afterFirst)

	if _, err := ImportCorrelationEvidence(
		store,
		record.MissionID,
		paths["draft-pr-evidence"],
		chainPath,
		"draft-pr-evidence",
	); err != nil {
		t.Fatal(err)
	}
	afterSecond, err := store.Load(record.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	assertOnlyCorrelationEvidenceChanged(t, record, afterSecond)
	if len(afterSecond.ArtifactRefs) != 2 ||
		len(afterSecond.CorrelatedImports) != 2 ||
		len(afterSecond.CorrelationChainReferences) != 1 {
		t.Fatalf("unique neutral roles were not retained: %+v", afterSecond)
	}

	beforeDuplicate, err := os.ReadFile(store.path(record.MissionID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ImportCorrelationEvidence(
		store,
		record.MissionID,
		paths["authority-and-approval"],
		chainPath,
		"authority-and-approval",
	); err == nil {
		t.Fatal("duplicate neutral role was accepted")
	}
	afterDuplicate, err := os.ReadFile(store.path(record.MissionID))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(afterDuplicate, beforeDuplicate) {
		t.Fatal("duplicate neutral role changed persisted Mission bytes")
	}
}

func TestCorrelationEvidenceCLIRequiresExplicitChainAndRole(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	_, record := startCorrelationTestMission(t, home)
	artifactPath := filepath.Join(dir, "evidence.json")
	writeJSONForTest(t, artifactPath, map[string]any{
		"schema":      "ao.stack.issue-route-verification.v1",
		"evidence_id": "issue-route-verification-001",
	})
	chain, err := BuildCorrelationChain(record, []CorrelationArtifactSpec{{
		Role: "issue-route-verification",
		Path: artifactPath,
	}})
	if err != nil {
		t.Fatal(err)
	}
	chainPath := filepath.Join(dir, "chain.json")
	writeJSONForTest(t, chainPath, chain)

	for name, args := range map[string][]string{
		"missing chain": {
			"--home", home, "import", "correlation-evidence",
			"--mission", record.MissionID, "--path", artifactPath,
			"--correlation-role", "issue-route-verification",
		},
		"missing role": {
			"--home", home, "import", "correlation-evidence",
			"--mission", record.MissionID, "--path", artifactPath,
			"--correlation-chain", chainPath,
		},
	} {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := Run(args, &stdout, &stderr); code == 0 {
				t.Fatalf("%s was accepted: %s", name, stdout.String())
			}
		})
	}

	var stdout, stderr bytes.Buffer
	if code := Run([]string{
		"--home", home,
		"import", "correlation-evidence",
		"--mission", record.MissionID,
		"--path", artifactPath,
		"--correlation-chain", chainPath,
		"--correlation-role", "issue-route-verification",
	}, &stdout, &stderr); code != 0 {
		t.Fatalf("neutral CLI import failed: %s", stderr.String())
	}
	var readback ImportReadback
	if err := json.Unmarshal(stdout.Bytes(), &readback); err != nil {
		t.Fatal(err)
	}
	if readback.Kind != "correlation-evidence" {
		t.Fatalf("unexpected neutral CLI readback: %+v", readback)
	}
}

func TestCorrelationReferenceDigestRejectsPersistedStateMutation(t *testing.T) {
	store, record, _, _ := importCorrelationTestWorkflow(t, true)
	if len(record.CorrelationChainReferences) != 1 {
		t.Fatalf("expected one complete reference, got %+v", record.CorrelationChainReferences)
	}

	mutations := map[string]func(*Record){
		"entry": func(record *Record) {
			record.CorrelationChainReferences[0].Entries[0].Producer = "ao-forge"
		},
		"provenance": func(record *Record) {
			record.CorrelationChainReferences[0].Entries[0].NativeIdentifier = "authorization-changed"
		},
		"chain digest": func(record *Record) {
			record.CorrelationChainReferences[0].ChainDigest = "sha256:" + strings.Repeat("a", 64)
		},
		"reference digest": func(record *Record) {
			record.CorrelationChainReferences[0].ReferenceDigest = "sha256:" + strings.Repeat("b", 64)
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			body, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			var tampered Record
			if err := json.Unmarshal(body, &tampered); err != nil {
				t.Fatal(err)
			}
			mutate(&tampered)
			if err := validateRecordCorrelationState(tampered); err == nil ||
				!strings.Contains(err.Error(), "reference_digest") {
				t.Fatalf("%s mutation was accepted: %v", name, err)
			}
			if _, err := BuildMissionArchive(tampered); err == nil {
				t.Fatalf("archive accepted %s mutation", name)
			}

			tamperedStore := NewStore(t.TempDir())
			if err := tamperedStore.Save(tampered); err == nil ||
				!strings.Contains(err.Error(), "reference_digest") {
				t.Fatalf("record save accepted %s mutation: %v", name, err)
			}
			if _, err := os.Stat(tamperedStore.path(tampered.MissionID)); !os.IsNotExist(err) {
				t.Fatalf("rejected %s mutation created a Mission record: %v", name, err)
			}
		})
	}

	loaded, err := store.Load(record.MissionID)
	if err != nil {
		t.Fatalf("valid persisted reference was rejected on load: %v", err)
	}
	if loaded.CorrelationChainReferences[0].ReferenceDigest !=
		loaded.CorrelatedImports[0].ReferenceDigest {
		t.Fatalf("loaded import is not bound to reference digest: %+v", loaded)
	}
}

func TestPersistedCorrelationLocatorIntegrityRejectsLiveRetargeting(t *testing.T) {
	store, record, _, artifactPaths := importCorrelationTestWorkflow(t, true)
	if len(record.CorrelatedImports) == 0 {
		t.Fatal("expected correlation-bound imports")
	}
	for _, binding := range record.CorrelatedImports {
		if binding.LocatorState != correlationLocatorStateLive ||
			!validSHA256Digest(binding.LocatorDigest) ||
			binding.ArchiveSourceLocatorDigest != "" {
			t.Fatalf("import lacks live locator commitment: %+v", binding)
		}
	}

	source := artifactPaths[record.CorrelatedImports[0].Role]
	body, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	copyPath := filepath.Join(t.TempDir(), filepath.Base(source))
	if err := os.WriteFile(copyPath, body, 0o644); err != nil {
		t.Fatal(err)
	}

	for name, mutate := range map[string]func(*CorrelatedImportBinding){
		"same digest copy": func(binding *CorrelatedImportBinding) {
			binding.ArtifactPath = copyPath
			binding.LocatorDigest = correlatedImportLocatorDigest(*binding)
		},
		"redaction sentinel": func(binding *CorrelatedImportBinding) {
			binding.ArtifactPath = correlationRedactedPathSentinel
		},
		"forged archive state": func(binding *CorrelatedImportBinding) {
			binding.ArtifactPath = correlationRedactedPathSentinel
			binding.LocatorState = correlationLocatorStateArchiveRedacted
			binding.ArchiveSourceLocatorDigest = record.CorrelatedImports[0].LocatorDigest
			binding.LocatorDigest = correlatedImportLocatorDigest(*binding)
		},
	} {
		t.Run(name, func(t *testing.T) {
			encoded, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			var tampered Record
			if err := json.Unmarshal(encoded, &tampered); err != nil {
				t.Fatal(err)
			}
			mutate(&tampered.CorrelatedImports[0])
			writeJSONForTest(t, store.path(record.MissionID), tampered)
			if _, err := store.Load(record.MissionID); err == nil ||
				!strings.Contains(err.Error(), "locator") {
				t.Fatalf("%s was accepted: %v", name, err)
			}
			writeJSONForTest(t, store.path(record.MissionID), record)
		})
	}
}

func TestMissionArchiveCarriesExplicitCorrelationLocatorRestorationState(t *testing.T) {
	_, record, completeChainPath, _ := importCorrelationTestWorkflow(t, true)
	archive, err := BuildMissionArchive(record)
	if err != nil {
		t.Fatal(err)
	}
	for i, archived := range archive.Record.CorrelatedImports {
		live := record.CorrelatedImports[i]
		if archived.ArtifactPath != correlationRedactedPathSentinel ||
			archived.LocatorState != correlationLocatorStateArchiveRedacted ||
			archived.ArchiveSourceLocatorDigest != live.LocatorDigest ||
			archived.LocatorDigest != correlatedImportLocatorDigest(archived) {
			t.Fatalf("archive locator restoration state is not integrity-bound:\nlive=%+v\narchive=%+v", live, archived)
		}
	}

	archivePath := filepath.Join(t.TempDir(), "archive.json")
	writeJSONForTest(t, archivePath, archive)
	importStore := NewStore(t.TempDir())
	if _, err := ImportMissionArchive(importStore, archivePath); err != nil {
		t.Fatal(err)
	}
	imported, err := importStore.Load(record.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	packet, err := BuildFinalReconciliationPacketWithCorrelationChain(imported, completeChainPath)
	if err != nil {
		t.Fatal(err)
	}
	if packet.CorrelationChainStatus != "ready" {
		t.Fatalf("exact chain did not rehydrate explicit archive state: %+v", packet)
	}
}

func TestMissionArchiveRedactsPlatformIndependentAbsolutePaths(t *testing.T) {
	for name, test := range map[string]struct {
		input string
		want  string
	}{
		"macOS":           {input: "/Users/example/evidence.json", want: correlationRedactedPathSentinel},
		"Linux home":      {input: "/home/example/evidence.json", want: correlationRedactedPathSentinel},
		"Linux var":       {input: "/var/lib/ao/evidence.json", want: correlationRedactedPathSentinel},
		"macOS private":   {input: "/private/var/tmp/evidence.json", want: correlationRedactedPathSentinel},
		"Windows drive":   {input: `C:\Users\example\evidence.json`, want: correlationRedactedPathSentinel},
		"Windows slash":   {input: `D:/work/evidence.json`, want: correlationRedactedPathSentinel},
		"Windows UNC":     {input: `\\server\share\evidence.json`, want: correlationRedactedPathSentinel},
		"protocol server": {input: `//server/share/evidence.json`, want: `//server/share/evidence.json`},
		"relative":        {input: "docs/evidence/result.json", want: "docs/evidence/result.json"},
		"drive relative":  {input: `C:evidence.json`, want: `C:evidence.json`},
		"repos path":      {input: "/repos/example/project/pulls", want: correlationRedactedPathSentinel},
		"public URL":      {input: "https://example.test/evidence.json", want: "https://example.test/evidence.json"},
	} {
		t.Run(name, func(t *testing.T) {
			got, redactions := redactArchiveLocalPathsValue(test.input, "record.path", nil)
			if got != test.want {
				t.Fatalf("redaction mismatch: got %q want %q", got, test.want)
			}
			if (test.input == test.want) != (len(redactions) == 0) {
				t.Fatalf("unexpected redaction metadata for %q: %v", test.input, redactions)
			}
		})
	}
}

func TestIncompleteCorrelationChainIsAllowedForImportButDeniedAtFinal(t *testing.T) {
	store, record, completeChainPath, _ := importCorrelationTestWorkflow(t, false)
	record = markCorrelationTestMissionReady(t, store, record.MissionID)

	packet := BuildFinalReconciliationPacket(record)
	if packet.Status != "blocked" || packet.ArtifactsAgree ||
		packet.CorrelationChainStatus != "blocked" ||
		!strings.Contains(packet.Blocker, "complete correlation chain") {
		t.Fatalf("incomplete persisted chain did not deny final reconciliation: %+v", packet)
	}

	packet, err := BuildFinalReconciliationPacketWithCorrelationChain(record, completeChainPath)
	if err != nil {
		t.Fatal(err)
	}
	if packet.Status != "ready" || !packet.ArtifactsAgree ||
		packet.CorrelationChainStatus != "ready" ||
		!validSHA256Digest(packet.CorrelationChainDigest) {
		t.Fatalf("caller-supplied complete chain was not accepted: %+v", packet)
	}
}

func TestCompletePersistedCorrelationChainIsAcceptedAtFinal(t *testing.T) {
	store, record, _, _ := importCorrelationTestWorkflow(t, true)
	record = markCorrelationTestMissionReady(t, store, record.MissionID)

	packet := BuildFinalReconciliationPacket(record)
	if packet.Status != "ready" || !packet.ArtifactsAgree ||
		packet.CorrelationChainStatus != "ready" ||
		!validSHA256Digest(packet.CorrelationChainDigest) {
		t.Fatalf("complete persisted chain was not accepted: %+v", packet)
	}
}

func TestCallerSuppliedFinalReconciliationRejectsReorderedPersistedChain(t *testing.T) {
	for name, completeSecondChain := range map[string]bool{
		"exact persisted chain": true,
		"partial consolidation": false,
	} {
		t.Run(name, func(t *testing.T) {
			store, record, completeChainPath, _ := importCorrelationTestWorkflow(t, completeSecondChain)
			record = markCorrelationTestMissionReady(t, store, record.MissionID)

			var chain CorrelationChain
			body, err := os.ReadFile(completeChainPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(body, &chain); err != nil {
				t.Fatal(err)
			}
			for left, right := 0, len(chain.Entries)-1; left < right; left, right = left+1, right-1 {
				chain.Entries[left], chain.Entries[right] = chain.Entries[right], chain.Entries[left]
			}
			reorderedPath := filepath.Join(t.TempDir(), "reordered-chain.json")
			writeJSONForTest(t, reorderedPath, chain)
			validation, err := ValidateCorrelationChainFile(reorderedPath)
			if err != nil {
				t.Fatalf("reordered control chain is structurally invalid: %v", err)
			}
			if completeSecondChain &&
				validation.ChainDigest == record.CorrelatedImports[0].ChainDigest {
				t.Fatal("reordered control chain did not change its digest")
			}

			packet, err := BuildFinalReconciliationPacketWithCorrelationChain(record, reorderedPath)
			if err != nil {
				t.Fatal(err)
			}
			if packet.Status != "blocked" || packet.ArtifactsAgree ||
				packet.CorrelationChainStatus != "blocked" ||
				!strings.Contains(packet.Blocker, "chain identity") {
				t.Fatalf("reordered chain replaced persisted identity: %+v", packet)
			}
		})
	}
}

func TestFinalCorrelationReconciliationRechecksCurrentArtifactDigests(t *testing.T) {
	store, record, _, artifactPaths := importCorrelationTestWorkflow(t, true)
	record = markCorrelationTestMissionReady(t, store, record.MissionID)
	writeJSONForTest(t, artifactPaths["atlas-workgraph"], map[string]any{
		"schema":       "ao.atlas.workgraph.v0.1",
		"workgraph_id": "workgraph-001",
		"changed":      true,
	})

	packet := BuildFinalReconciliationPacket(record)
	if packet.Status != "blocked" || packet.ArtifactsAgree ||
		packet.CorrelationChainStatus != "blocked" ||
		!strings.Contains(packet.Blocker, "digest") {
		t.Fatalf("changed imported artifact did not deny final reconciliation: %+v", packet)
	}
}

func TestCallerSuppliedFinalReconciliationRechecksPersistedImportPaths(t *testing.T) {
	store, record, _, artifactPaths := importCorrelationTestWorkflow(t, true)
	record = markCorrelationTestMissionReady(t, store, record.MissionID)

	copyDir := t.TempDir()
	copyPaths := make(map[string]string, len(artifactPaths))
	specs := make([]CorrelationArtifactSpec, 0, len(artifactPaths))
	for role, sourcePath := range artifactPaths {
		body, err := os.ReadFile(sourcePath)
		if err != nil {
			t.Fatal(err)
		}
		copyPath := filepath.Join(copyDir, filepath.Base(sourcePath))
		if err := os.WriteFile(copyPath, body, 0o644); err != nil {
			t.Fatal(err)
		}
		copyPaths[role] = copyPath
		specs = append(specs, CorrelationArtifactSpec{Role: role, Path: copyPath})
	}
	validCopyChain, err := BuildCorrelationChain(record, specs)
	if err != nil {
		t.Fatal(err)
	}
	validCopyChainPath := filepath.Join(copyDir, "chain.json")
	writeJSONForTest(t, validCopyChainPath, validCopyChain)

	writeJSONForTest(t, artifactPaths["atlas-workgraph"], map[string]any{
		"schema":       "ao.atlas.workgraph.v0.1",
		"workgraph_id": "workgraph-001",
		"changed":      true,
	})
	if _, err := ValidateCorrelationChainFile(validCopyChainPath); err != nil {
		t.Fatalf("control chain over intact copies is invalid: %v", err)
	}

	packet, err := BuildFinalReconciliationPacketWithCorrelationChain(record, validCopyChainPath)
	if err != nil {
		t.Fatal(err)
	}
	if packet.Status != "blocked" || packet.ArtifactsAgree ||
		packet.CorrelationChainStatus != "blocked" ||
		!strings.Contains(packet.Blocker, "live locator") {
		t.Fatalf("valid copies hid changed persisted import evidence: %+v", packet)
	}
}

func TestCorrelationStateSurvivesArchiveExportAndImport(t *testing.T) {
	store, record, completeChainPath, _ := importCorrelationTestWorkflow(t, true)
	record = markCorrelationTestMissionReady(t, store, record.MissionID)
	archive, err := BuildMissionArchive(record)
	if err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(t.TempDir(), "archive.json")
	writeJSONForTest(t, archivePath, archive)
	importStore := NewStore(t.TempDir())
	if _, err := ImportMissionArchive(importStore, archivePath); err != nil {
		t.Fatal(err)
	}
	imported, err := importStore.Load(record.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(imported.CorrelatedImports) != len(record.CorrelatedImports) ||
		len(imported.CorrelationChainReferences) != len(record.CorrelationChainReferences) ||
		imported.CorrelatedImports[0].Digest != record.CorrelatedImports[0].Digest ||
		imported.CorrelationChainReferences[0].ChainDigest != record.CorrelationChainReferences[0].ChainDigest {
		t.Fatalf("archive round trip lost correlation state: %+v", imported)
	}
	packet, err := BuildFinalReconciliationPacketWithCorrelationChain(imported, completeChainPath)
	if err != nil {
		t.Fatal(err)
	}
	if packet.Status != "ready" || packet.CorrelationChainStatus != "ready" {
		t.Fatalf("archived Mission rejected caller-supplied complete chain: %+v", packet)
	}
}

func TestCallerSuppliedChainRehydratesOnlyExactRedactedArchiveImports(t *testing.T) {
	_, record, completeChainPath, artifactPaths := importCorrelationTestWorkflow(t, true)
	archive, err := BuildMissionArchive(record)
	if err != nil {
		t.Fatal(err)
	}
	for i := range archive.Record.CorrelatedImports {
		archive.Record.CorrelatedImports[i].ArtifactPath = "<local-path-redacted>"
	}
	archive.ArchiveDigest = ""
	body, err := json.Marshal(archive)
	if err != nil {
		t.Fatal(err)
	}
	archive.ArchiveDigest = digestBytes(body)
	archivePath := filepath.Join(t.TempDir(), "archive.json")
	writeJSONForTest(t, archivePath, archive)

	importStore := NewStore(t.TempDir())
	if _, err := ImportMissionArchive(importStore, archivePath); err != nil {
		t.Fatal(err)
	}
	imported, err := importStore.Load(record.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	for _, binding := range imported.CorrelatedImports {
		if binding.ArtifactPath != "<local-path-redacted>" {
			t.Fatalf("archive import unexpectedly restored locator: %+v", binding)
		}
	}
	packet, err := BuildFinalReconciliationPacketWithCorrelationChain(imported, completeChainPath)
	if err != nil {
		t.Fatal(err)
	}
	if packet.CorrelationChainStatus != "ready" {
		t.Fatalf("exact chain did not rehydrate redacted locators: %+v", packet)
	}

	copyDir := t.TempDir()
	specs := make([]CorrelationArtifactSpec, 0, len(artifactPaths))
	for role, sourcePath := range artifactPaths {
		copyPath := filepath.Join(copyDir, filepath.Base(sourcePath))
		body, err := os.ReadFile(sourcePath)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(copyPath, body, 0o644); err != nil {
			t.Fatal(err)
		}
		specs = append(specs, CorrelationArtifactSpec{Role: role, Path: copyPath})
	}
	copyChain, err := BuildCorrelationChain(record, specs)
	if err != nil {
		t.Fatal(err)
	}
	copyChainPath := filepath.Join(copyDir, "chain.json")
	writeJSONForTest(t, copyChainPath, copyChain)
	packet, err = BuildFinalReconciliationPacketWithCorrelationChain(imported, copyChainPath)
	if err != nil {
		t.Fatal(err)
	}
	if packet.CorrelationChainStatus != "blocked" {
		t.Fatalf("different chain/reference digests rehydrated redacted locators: %+v", packet)
	}
}

func TestMissionArchiveValidationRejectsRehashedCorrelationReferenceMutation(t *testing.T) {
	_, record, _, _ := importCorrelationTestWorkflow(t, true)
	archive, err := BuildMissionArchive(record)
	if err != nil {
		t.Fatal(err)
	}
	archive.Record.CorrelationChainReferences[0].Entries[0].Producer = "ao-forge"
	archive.ArchiveDigest = ""
	body, err := json.Marshal(archive)
	if err != nil {
		t.Fatal(err)
	}
	archive.ArchiveDigest = digestBytes(body)
	path := filepath.Join(t.TempDir(), "archive.json")
	writeJSONForTest(t, path, archive)

	if _, err := ValidateMissionArchive(path); err == nil ||
		!strings.Contains(err.Error(), "reference_digest") {
		t.Fatalf("rehashed archive hid correlation reference mutation: %v", err)
	}
}

func TestCorrelationChainCLIWorkflow(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	store, record := startCorrelationTestMission(t, home)
	artifactPath := filepath.Join(dir, "authorization.json")
	writeJSONForTest(t, artifactPath, map[string]any{
		"schema":           "ao.blueprint.build-authorization.v0.1",
		"status":           "ready",
		"authorization_id": "authorization-cli-001",
	})
	chainPath := filepath.Join(dir, "chain.json")
	var stdout, stderr bytes.Buffer
	if code := Run([]string{
		"--home", home,
		"correlation", "build",
		"--mission", record.MissionID,
		"--artifact", "blueprint-authorization=" + artifactPath,
		"--out", chainPath,
	}, &stdout, &stderr); code != 0 {
		t.Fatalf("correlation build failed: %s", stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{
		"correlation", "validate", "--path", chainPath,
	}, &stdout, &stderr); code != 0 {
		t.Fatalf("correlation validate failed: %s", stderr.String())
	}
	var validation CorrelationChainValidation
	if err := json.Unmarshal(stdout.Bytes(), &validation); err != nil {
		t.Fatal(err)
	}
	if validation.Status != "ready" {
		t.Fatalf("unexpected CLI validation: %+v", validation)
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{
		"--home", home,
		"import", "blueprint-authorization",
		"--mission", record.MissionID,
		"--path", artifactPath,
		"--correlation-chain", chainPath,
	}, &stdout, &stderr); code != 0 {
		t.Fatalf("correlated import failed: %s", stderr.String())
	}
	var readback ImportReadback
	if err := json.Unmarshal(stdout.Bytes(), &readback); err != nil {
		t.Fatal(err)
	}
	if !validSHA256Digest(readback.CorrelationChainDigest) {
		t.Fatalf("CLI import did not bind chain: %+v", readback)
	}
	markCorrelationTestMissionReady(t, store, record.MissionID)

	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{
		"--home", home,
		"final", "reconcile",
		"--mission", record.MissionID,
		"--correlation-chain", chainPath,
	}, &stdout, &stderr); code != 0 {
		t.Fatalf("correlated final reconcile failed: %s", stderr.String())
	}
	var packet MissionFinalReconciliationPacket
	if err := json.Unmarshal(stdout.Bytes(), &packet); err != nil {
		t.Fatal(err)
	}
	if packet.Status != "ready" || packet.CorrelationChainStatus != "ready" {
		t.Fatalf("CLI final reconciliation rejected valid chain: %+v", packet)
	}
}

func TestCorrelationChainCLIRejectsArtifactAsOutput(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	_, record := startCorrelationTestMission(t, home)
	artifactPath := filepath.Join(dir, "authorization.json")
	writeJSONForTest(t, artifactPath, map[string]any{
		"schema":           "ao.blueprint.build-authorization.v0.1",
		"authorization_id": "authorization-output-001",
	})
	before := digestFileForTest(t, artifactPath)
	var stdout, stderr bytes.Buffer
	if code := Run([]string{
		"--home", home,
		"correlation", "build",
		"--mission", record.MissionID,
		"--artifact", "blueprint-authorization=" + artifactPath,
		"--out", artifactPath,
	}, &stdout, &stderr); code == 0 {
		t.Fatal("correlation build overwrote an input artifact")
	}
	if after := digestFileForTest(t, artifactPath); after != before {
		t.Fatalf("rejected output changed input artifact: got %s want %s", after, before)
	}
}

func TestWriteCorrelationChainRejectsExistingDestinationWithoutChangingIt(t *testing.T) {
	dir := t.TempDir()
	record := correlationTestRecord()
	artifactPath := filepath.Join(dir, "artifact.json")
	writeJSONForTest(t, artifactPath, map[string]any{
		"schema":         "ao.atlas.workgraph.v1",
		"correlation_id": record.CorrelationID,
	})
	chain, err := BuildCorrelationChain(record, []CorrelationArtifactSpec{{
		Role: "artifact",
		Path: artifactPath,
	}})
	if err != nil {
		t.Fatal(err)
	}

	for name, prepare := range map[string]func(*testing.T, string) []byte{
		"regular file": func(t *testing.T, path string) []byte {
			body := []byte("do not replace\n")
			if err := os.WriteFile(path, body, 0o644); err != nil {
				t.Fatal(err)
			}
			return body
		},
		"hard link": func(t *testing.T, path string) []byte {
			source := filepath.Join(filepath.Dir(path), "hard-link-source")
			body := []byte("hard-linked evidence\n")
			if err := os.WriteFile(source, body, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Link(source, path); err != nil {
				t.Fatal(err)
			}
			return body
		},
		"symlink": func(t *testing.T, path string) []byte {
			source := filepath.Join(filepath.Dir(path), "symlink-source")
			body := []byte("symlink target\n")
			if err := os.WriteFile(source, body, 0o644); err != nil {
				t.Fatal(err)
			}
			createTestSymlink(t, source, path)
			return body
		},
	} {
		t.Run(name, func(t *testing.T) {
			outputPath := filepath.Join(t.TempDir(), "chain.json")
			before := prepare(t, outputPath)
			if err := WriteCorrelationChainFile(outputPath, chain); err == nil {
				t.Fatalf("existing %s destination was replaced", name)
			}
			after, err := os.ReadFile(outputPath)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(after, before) {
				t.Fatalf("rejected %s destination changed: got %q want %q", name, after, before)
			}
		})
	}
}

func TestWriteCorrelationChainRejectsOutputSymlinkRace(t *testing.T) {
	dir := t.TempDir()
	record := correlationTestRecord()
	artifactPath := filepath.Join(dir, "artifact.json")
	writeJSONForTest(t, artifactPath, map[string]any{
		"schema":         "ao.atlas.workgraph.v1",
		"correlation_id": record.CorrelationID,
	})
	chain, err := BuildCorrelationChain(record, []CorrelationArtifactSpec{{
		Role: "artifact",
		Path: artifactPath,
	}})
	if err != nil {
		t.Fatal(err)
	}
	targetPath := filepath.Join(dir, "target.json")
	targetBody := []byte("preserve target\n")
	if err := os.WriteFile(targetPath, targetBody, 0o644); err != nil {
		t.Fatal(err)
	}
	outputPath := filepath.Join(dir, "chain.json")

	err = writeCorrelationChainFileWithCreate(outputPath, chain, func(path string) (*os.File, error) {
		createTestSymlink(t, targetPath, path)
		return openExclusiveCorrelationOutput(path)
	})
	if err == nil {
		t.Fatal("output symlink installed during creation race was followed")
	}
	after, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, targetBody) {
		t.Fatalf("output symlink race changed target: got %q want %q", after, targetBody)
	}
}

func TestLegacyUncorrelatedImportAndFinalReconciliationRemainCompatible(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "home"))
	record, err := store.Start("legacy correlation compatibility mission")
	if err != nil {
		t.Fatal(err)
	}
	artifactPath := filepath.Join(dir, "authorization.json")
	writeJSONForTest(t, artifactPath, map[string]any{
		"schema": "ao.blueprint.build-authorization.v0.1",
		"status": "ready",
	})
	if _, err := ImportArtifact(store, record.MissionID, "blueprint-authorization", artifactPath); err != nil {
		t.Fatalf("legacy import failed: %v", err)
	}
	record = markCorrelationTestMissionReady(t, store, record.MissionID)
	packet := BuildFinalReconciliationPacket(record)
	if packet.Status != "ready" || !packet.ArtifactsAgree ||
		packet.CorrelationChainStatus != "" || packet.CorrelationChainDigest != "" {
		t.Fatalf("legacy final reconciliation changed: %+v", packet)
	}
}

func correlationTestRecord() Record {
	return Record{
		Schema:        RecordSchema,
		MissionID:     "mission-0123456789abcdef",
		CorrelationID: "corr-month3-001",
	}
}

func assertOnlyCorrelationEvidenceChanged(t *testing.T, before, after Record) {
	t.Helper()
	before.UpdatedAtUTC = ""
	after.UpdatedAtUTC = ""
	before.ArtifactRefs = nil
	after.ArtifactRefs = nil
	before.CorrelationChainReferences = nil
	after.CorrelationChainReferences = nil
	before.CorrelatedImports = nil
	after.CorrelatedImports = nil
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("neutral evidence import changed Mission workflow state:\nbefore=%+v\nafter=%+v", before, after)
	}
}

func digestFileForTest(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return digestBytes(body)
}

func canonicalPathForTest(t *testing.T, path string) string {
	t.Helper()
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err = filepath.Abs(canonical)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(canonical)
}

func startCorrelationTestMission(t *testing.T, home string) (Store, Record) {
	t.Helper()
	store := NewStore(home)
	contract, err := store.StartObjective(
		"Implement one bounded multi-file correlation workflow",
		ObjectiveStartOptions{CorrelationID: "corr-month3-integration"},
	)
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Load(contract.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	return store, record
}

func importCorrelationTestWorkflow(t *testing.T, completeSecondChain bool) (Store, Record, string, map[string]string) {
	t.Helper()
	dir := t.TempDir()
	store, record := startCorrelationTestMission(t, filepath.Join(dir, "home"))
	artifactPaths := map[string]string{
		"blueprint-authorization": filepath.Join(dir, "authorization.json"),
		"atlas-workgraph":         filepath.Join(dir, "workgraph.json"),
	}
	writeJSONForTest(t, artifactPaths["blueprint-authorization"], map[string]any{
		"schema":           "ao.blueprint.build-authorization.v0.1",
		"status":           "ready",
		"authorization_id": "authorization-003",
	})
	writeJSONForTest(t, artifactPaths["atlas-workgraph"], map[string]any{
		"schema":          "ao.atlas.workgraph.v0.1",
		"mission_id":      record.MissionID,
		"target_instance": record.MissionID,
		"nodes":           []any{},
	})
	allSpecs := []CorrelationArtifactSpec{
		{Role: "blueprint-authorization", Path: artifactPaths["blueprint-authorization"]},
		{Role: "atlas-workgraph", Path: artifactPaths["atlas-workgraph"]},
	}
	completeChain, err := BuildCorrelationChain(record, allSpecs)
	if err != nil {
		t.Fatal(err)
	}
	completeChainPath := filepath.Join(dir, "complete-chain.json")
	writeJSONForTest(t, completeChainPath, completeChain)
	firstChain, err := BuildCorrelationChain(record, allSpecs[:1])
	if err != nil {
		t.Fatal(err)
	}
	firstChainPath := filepath.Join(dir, "first-chain.json")
	writeJSONForTest(t, firstChainPath, firstChain)
	if _, err := ImportArtifactWithCorrelationChain(
		store,
		record.MissionID,
		"blueprint-authorization",
		artifactPaths["blueprint-authorization"],
		firstChainPath,
	); err != nil {
		t.Fatal(err)
	}

	secondChainPath := completeChainPath
	if !completeSecondChain {
		secondChain, err := BuildCorrelationChain(record, allSpecs[1:])
		if err != nil {
			t.Fatal(err)
		}
		secondChainPath = filepath.Join(dir, "second-chain.json")
		writeJSONForTest(t, secondChainPath, secondChain)
	}
	if _, err := ImportArtifactWithCorrelationChain(
		store,
		record.MissionID,
		"atlas-workgraph",
		artifactPaths["atlas-workgraph"],
		secondChainPath,
	); err != nil {
		t.Fatal(err)
	}
	record, err = store.Load(record.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	return store, record, completeChainPath, artifactPaths
}

func markCorrelationTestMissionReady(t *testing.T, store Store, missionID string) Record {
	t.Helper()
	record, err := store.Update(missionID, func(record *Record) error {
		record.Status = "done"
		record.CurrentRoute = "complete"
		record.CurrentPhase = "complete"
		record.ExactNextAction = "mission complete; read final rollup and recommended next tasks"
		record.Evidence.AtlasRecommendation = &AtlasRecommendationReadbackCounts{
			Status:               "completed",
			TotalNodes:           2,
			CompletedNodes:       2,
			ReadyNodes:           0,
			CheckpointCount:      2,
			MinMinutesMet:        true,
			LeaseTimeStatus:      "minimum_minutes_met",
			ReturnGateStatus:     "final_response_allowed",
			FinalResponseAllowed: true,
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func TestStrictJSONTypeMatchesNullableUnion(t *testing.T) {
	for _, test := range []struct {
		name  string
		value any
		want  string
	}{
		{"null", nil, "null"},
		{"integer-null", nil, "integer|null"},
		{"string-null", nil, "string|null"},
		{"object-null", nil, "object|null"},
		{"integer", json.Number("7"), "integer|null"},
		{"string", "value", "string|null"},
		{"object", map[string]any{}, "object|null"},
	} {
		if !strictJSONTypeMatches(test.value, test.want) {
			t.Fatalf("%s did not match %q", test.name, test.want)
		}
	}
	for _, test := range []struct {
		value any
		want  string
	}{
		{[]any{}, "integer|null"},
		{true, "integer|null"},
		{json.Number("7.5"), "integer|null"},
		{"value", "integer|null"},
		{nil, ""},
		{"value", "string|unsupported"},
	} {
		if strictJSONTypeMatches(test.value, test.want) {
			t.Fatalf("value %#v unexpectedly matched %q", test.value, test.want)
		}
	}
}
