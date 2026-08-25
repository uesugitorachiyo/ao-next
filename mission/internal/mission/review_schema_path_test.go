package mission

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestMissionRecordSchemaStrictlyDocumentsWorkflowContract(t *testing.T) {
	schema := loadMissionRecordSchemaForReview(t)
	properties := schema["properties"].(map[string]any)
	workflow := properties["workflow_contract"].(map[string]any)

	if workflow["additionalProperties"] != false {
		t.Fatal("workflow_contract schema must reject unknown fields")
	}
	assertSchemaMatchesJSONFields(t, workflow, reflect.TypeOf(ObjectiveWorkflowContract{}))

	workflowProperties := workflow["properties"].(map[string]any)
	stages := workflowProperties["stages"].(map[string]any)
	stageItems := stages["items"].(map[string]any)
	if stageItems["additionalProperties"] != false {
		t.Fatal("workflow_contract stages must reject unknown fields")
	}
	assertSchemaMatchesJSONFields(t, stageItems, reflect.TypeOf(ObjectiveWorkflowStage{}))
}

func TestMissionRecordSchemaDocumentsLocatorCommitmentAndStateRules(t *testing.T) {
	schema := loadMissionRecordSchemaForReview(t)
	properties := schema["properties"].(map[string]any)

	references := properties["correlation_chain_references"].(map[string]any)
	referenceItems := references["items"].(map[string]any)
	entries := referenceItems["properties"].(map[string]any)["entries"].(map[string]any)
	entryItems := entries["items"].(map[string]any)
	entryProperties := entryItems["properties"].(map[string]any)
	if _, ok := entryProperties["locator_digest"]; !ok {
		t.Fatal("correlation reference entry must document locator_digest")
	}
	if !schemaRequiresField(entryItems, "locator_digest") {
		t.Fatal("correlation reference entry must require locator_digest")
	}

	imports := properties["correlated_imports"].(map[string]any)
	importItems := imports["items"].(map[string]any)
	digest := "sha256:" + strings.Repeat("a", 64)
	base := map[string]any{
		"role":             "artifact",
		"digest":           digest,
		"artifact_path":    "docs/evidence/result.json",
		"locator_state":    "live",
		"locator_digest":   digest,
		"chain_digest":     digest,
		"reference_digest": digest,
	}
	if blockers := validateJSONSchemaNode(base, importItems, "correlated_imports.0."); len(blockers) != 0 {
		t.Fatalf("valid live locator was rejected: %v", blockers)
	}

	liveWithArchiveSource := cloneReviewMap(base)
	liveWithArchiveSource["archive_source_locator_digest"] = digest
	if blockers := validateJSONSchemaNode(liveWithArchiveSource, importItems, "correlated_imports.0."); len(blockers) == 0 {
		t.Fatal("live locator accepted archive_source_locator_digest")
	}

	archive := cloneReviewMap(base)
	archive["artifact_path"] = correlationRedactedPathSentinel
	archive["locator_state"] = correlationLocatorStateArchiveRedacted
	archive["archive_source_locator_digest"] = digest
	if blockers := validateJSONSchemaNode(archive, importItems, "correlated_imports.0."); len(blockers) != 0 {
		t.Fatalf("valid archive locator was rejected: %v", blockers)
	}

	delete(archive, "archive_source_locator_digest")
	if blockers := validateJSONSchemaNode(archive, importItems, "correlated_imports.0."); len(blockers) == 0 {
		t.Fatal("archive locator accepted missing archive_source_locator_digest")
	}

	archive["archive_source_locator_digest"] = digest
	archive["artifact_path"] = "docs/evidence/result.json"
	if blockers := validateJSONSchemaNode(archive, importItems, "correlated_imports.0."); len(blockers) == 0 {
		t.Fatal("archive locator accepted a non-sentinel artifact_path")
	}
}

func TestArchivePathRedactionHandlesArbitraryLocalRootsWithoutScrubbingPublicReferences(t *testing.T) {
	tests := map[string]struct {
		input string
		want  string
	}{
		"workspace root":          {input: "/workspace/ao/evidence.json", want: correlationRedactedPathSentinel},
		"data root":               {input: "/data/ao/evidence.json", want: correlationRedactedPathSentinel},
		"embedded POSIX":          {input: "read /workspace/ao/evidence.json next", want: "read " + correlationRedactedPathSentinel + " next"},
		"punctuated POSIX":        {input: "inspect:/workspace/ao/evidence.json", want: "inspect:" + correlationRedactedPathSentinel},
		"punctuated data path":    {input: "evidence=(/data/ao/result.json)", want: "evidence=(" + correlationRedactedPathSentinel + ")"},
		"Windows drive":           {input: `C:\work\evidence.json`, want: correlationRedactedPathSentinel},
		"backslash UNC":           {input: `\\server\share\evidence.json`, want: correlationRedactedPathSentinel},
		"protocol localhost":      {input: "//localhost/app.js", want: "//localhost/app.js"},
		"protocol localhost port": {input: "//localhost:8080/app.js", want: "//localhost:8080/app.js"},
		"protocol intranet":       {input: "//intranet/app.js", want: "//intranet/app.js"},
		"protocol IPv6":           {input: "//[::1]/app.js", want: "//[::1]/app.js"},
		"protocol relative CDN":   {input: "//cdn.example.com/app.js", want: "//cdn.example.com/app.js"},
		"API local path":          {input: "/api/private/evidence.json", want: correlationRedactedPathSentinel},
		"repos local path":        {input: "/repos/private/evidence.json", want: correlationRedactedPathSentinel},
		"relative artifact":       {input: "docs/evidence/result.json", want: "docs/evidence/result.json"},
		"relative prose":          {input: "inspect docs/evidence/result.json", want: "inspect docs/evidence/result.json"},
	}
	for name, test := range tests {
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

func TestArchivePathRedactionPreservesOnlyValidContractProvenancePointers(t *testing.T) {
	for name, test := range map[string]struct {
		path  string
		input string
		want  string
	}{
		"native pointer": {
			path:  "record.correlation_chain_references[0].entries[0].native_field",
			input: "/provenance/request_id",
			want:  "/provenance/request_id",
		},
		"parent pointer": {
			path:  "record.correlation_chain_references[0].entries[0].parent_digest_field",
			input: "/repair/verification_sha256",
			want:  "/repair/verification_sha256",
		},
		"malformed pointer": {
			path:  "record.correlation_chain_references[0].entries[0].native_field",
			input: "/private/~x/evidence.json",
			want:  correlationRedactedPathSentinel,
		},
		"generic local path": {
			path:  "record.objective",
			input: "/provenance/request_id",
			want:  correlationRedactedPathSentinel,
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, redactions := redactArchiveLocalPathsValue(test.input, test.path, nil)
			if got != test.want {
				t.Fatalf("redaction mismatch: got %q want %q", got, test.want)
			}
			if (test.input == test.want) != (len(redactions) == 0) {
				t.Fatalf("unexpected redaction metadata for %q: %v", test.input, redactions)
			}
		})
	}
}

func loadMissionRecordSchemaForReview(t *testing.T) map[string]any {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", "docs", "contracts", "mission-record-v0.1.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	value, err := decodeExactJSON(body)
	if err != nil {
		t.Fatal(err)
	}
	schema, ok := value.(map[string]any)
	if !ok {
		t.Fatal("Mission record schema must be one exact JSON object")
	}
	return schema
}

func assertSchemaMatchesJSONFields(t *testing.T, schema map[string]any, runtimeType reflect.Type) {
	t.Helper()
	properties := schema["properties"].(map[string]any)
	for i := 0; i < runtimeType.NumField(); i++ {
		name := strings.Split(runtimeType.Field(i).Tag.Get("json"), ",")[0]
		if _, ok := properties[name]; !ok {
			t.Errorf("runtime field %q is absent from schema", name)
		}
		if !schemaRequiresField(schema, name) {
			t.Errorf("runtime field %q is not required by schema", name)
		}
	}
	if len(properties) != runtimeType.NumField() {
		t.Errorf("schema has %d fields; runtime has %d", len(properties), runtimeType.NumField())
	}
}

func schemaRequiresField(schema map[string]any, field string) bool {
	for _, raw := range schema["required"].([]any) {
		if raw == field {
			return true
		}
	}
	return false
}

func cloneReviewMap(source map[string]any) map[string]any {
	clone := make(map[string]any, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}
