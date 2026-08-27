package mission

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

type workflowShape struct {
	events         map[string]bool
	inputs         map[string]map[string]string
	topPermissions map[string]string
	jobs           map[string]*workflowJob
}

type workflowJob struct {
	condition   string
	environment string
	permissions map[string]string
	uses        []string
}

type releaseVerifierFixture struct {
	candidatesPath string
	environment    map[string]string
	manifestPath   string
	planChecksum   string
	planPath       string
}

type releaseTargetFixture struct {
	archive      string
	architecture string
	binaryFormat string
	entryPoint   string
	goarch       string
	goos         string
	machine      string
	os           string
	runnerArch   string
	runnerLabel  string
	runnerOS     string
	targetLabel  string
}

func TestReleaseRehearsalWorkflowStructure(t *testing.T) {
	workflow := readReleaseWorkflow(t)
	shape := parseWorkflowShape(t, workflow)

	if len(shape.events) != 1 || !shape.events["workflow_dispatch"] {
		t.Fatalf("release workflow events=%v, want workflow_dispatch only", shape.events)
	}
	for _, input := range []string{
		"version",
		"tag",
		"source_sha",
		"approved_manifest_digest",
		"approved_manifest_base64",
		"dry_run",
		"live_confirmation",
	} {
		if _, ok := shape.inputs[input]; !ok {
			t.Fatalf("release workflow missing input %q", input)
		}
	}
	if shape.inputs["dry_run"]["default"] != "true" || shape.inputs["dry_run"]["type"] != "boolean" {
		t.Fatalf("dry_run input=%v, want boolean default true", shape.inputs["dry_run"])
	}
	if _, ok := shape.inputs["release_notes"]; ok {
		t.Fatal("release notes must come from exact committed bytes, not dispatch input")
	}
	if shape.topPermissions["contents"] != "read" {
		t.Fatalf("top-level permissions=%v, want contents read", shape.topPermissions)
	}

	wantJobs := []string{
		"bind-release-inputs",
		"native-candidates",
		"assemble-promotion-plan",
		"publish-release",
		"verify-published-release",
	}
	for _, name := range wantJobs {
		if shape.jobs[name] == nil {
			t.Fatalf("release workflow missing job %q", name)
		}
	}
	for name, job := range shape.jobs {
		want := "read"
		if name == "publish-release" {
			want = "write"
		}
		if job.permissions["contents"] != want {
			t.Fatalf("job %s contents permission=%q, want %q", name, job.permissions["contents"], want)
		}
	}

	publisher := shape.jobs["publish-release"]
	wantCondition := "${{ inputs.dry_run == false && inputs.live_confirmation == format('publish-ao-mission-{0}-{1}-{2}-{3}', inputs.version, inputs.tag, inputs.source_sha, inputs.approved_manifest_digest) }}"
	if publisher.condition != wantCondition {
		t.Fatalf("publisher condition=%q, want %q", publisher.condition, wantCondition)
	}
	if publisher.environment != "ao-mission-release" {
		t.Fatalf("publisher environment=%q, want ao-mission-release", publisher.environment)
	}
	if publisher.permissions["actions"] != "read" {
		t.Fatalf("publisher actions permission=%q, want read for environment API readback", publisher.permissions["actions"])
	}
	if _, ok := publisher.permissions["deployments"]; ok {
		t.Fatalf("publisher has unnecessary deployments permission: %v", publisher.permissions)
	}

	actionPin := regexp.MustCompile(`^actions/[a-z0-9-]+@[0-9a-f]{40}$`)
	actionCount := 0
	for jobName, job := range shape.jobs {
		for _, use := range job.uses {
			actionCount++
			if !actionPin.MatchString(use) {
				t.Fatalf("job %s action is not pinned to a full commit SHA: %q", jobName, use)
			}
		}
	}
	if actionCount < 10 {
		t.Fatalf("parsed only %d action uses, want all workflow actions", actionCount)
	}
}

func TestReleaseFinalizationImportsExactRehearsalArtifacts(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "release-finalize.yml"))
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(data)
	for _, want := range []string{
		"producer_run_id:",
		"expected_source_sha:",
		"expected_version:",
		"expected_tag:",
		"expected_manifest_digest:",
		"dry_run:",
		"repair_empty_release_notes:",
		"live_confirmation:",
		"actions: read",
		"contents: read",
		"run-id: ${{ inputs.producer_run_id }}",
		"ao-mission-release-rehearsal-plan-",
		"ao-mission-release-candidate-*",
		"ao-mission-approved-release-manifest-",
		"# imported-release-validator-begin",
		"environment: ao-mission-release",
		`notes_path = one("release-notes.md")`,
		`if digest(notes_path) != plan.get("release_notes_sha256")`,
		`shutil.copy2(notes_path, out / notes_path.name)`,
		`repair-empty-ao-mission-release-notes-`,
		`authorized_release_id=369467111`,
		`authorized_source_sha=cee287597024b5a1e990c6e272518236bc9e32fa`,
		`[ "$PRODUCER_RUN_ID" = 31630121755 ]`,
		`(.body == null or .body == "")`,
		`releases/assets/${asset_id}`,
		`candidate archive digest mismatch`,
		`gh api --method PATCH "repos/${GITHUB_REPOSITORY}/releases/${authorized_release_id}"`,
		`repair-readbacks/post-release.json`,
		`ao-mission-release-finalize-${{ inputs.expected_tag }}`,
	} {
		if !strings.Contains(workflow, want) {
			t.Fatalf("release finalization workflow missing %q", want)
		}
	}
	for _, forbidden := range []string{"go build", "native-candidates:", "assemble-promotion-plan:"} {
		if strings.Contains(workflow, forbidden) {
			t.Fatalf("release finalization workflow rebuilds or reassembles sealed inputs via %q", forbidden)
		}
	}
	if !strings.Contains(workflow, "find validated -type f") {
		t.Fatal("release finalization workflow does not recursively discover validated candidate archives")
	}
	if strings.Contains(workflow, "find validated -maxdepth 1 -type f") {
		t.Fatal("release finalization workflow searches only the validated top level")
	}
	wantPublisher := `gh release create "$TAG" --repo "$GITHUB_REPOSITORY" --target "$SOURCE_SHA" --title "AO Mission $VERSION" --notes-file "$notes" "${archives[@]}"`
	if !strings.Contains(workflow, wantPublisher) {
		t.Fatalf("release finalization publisher is not bound to the explicit repository: want %q", wantPublisher)
	}
	for _, forbidden := range []string{"gh release delete", "gh release edit", "git tag -f", "git push --force", "gh release upload", "gh release delete-asset"} {
		if strings.Contains(workflow, forbidden) {
			t.Fatalf("release-notes repair contains forbidden release mutation %q", forbidden)
		}
	}
}

func TestReleasePublisherRunScriptHasValidBashSyntax(t *testing.T) {
	workflow := strings.ReplaceAll(string(mustReadFile(t, filepath.Join("..", "..", ".github", "workflows", "release-finalize.yml"))), "\r\n", "\n")
	start := strings.Index(workflow, "      - name: Publish only exact imported archives\n")
	if start < 0 {
		t.Fatal("release publisher step not found")
	}
	end := strings.Index(workflow[start:], "      - name: Upload release-notes repair readbacks\n")
	if end < 0 {
		t.Fatal("release publisher step not found")
	}
	step := workflow[start : start+end]
	run := strings.Index(step, "        run: |\n")
	if run < 0 {
		t.Fatal("release publisher run script not found")
	}
	lines := strings.Split(step[run+len("        run: |\n"):], "\n")
	for i, line := range lines {
		if line != "" {
			lines[i] = strings.TrimPrefix(line, "          ")
		}
	}
	bash, err := bashForWorkflowSyntaxTest()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(bash, "-n")
	command.Stdin = strings.NewReader(strings.Join(lines, "\n"))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("release publisher has invalid Bash syntax: %v\n%s", err, output)
	}
}

func bashForWorkflowSyntaxTest() (string, error) {
	if bash, err := exec.LookPath("bash"); err == nil {
		return bash, nil
	}
	git, err := exec.LookPath("git")
	if err != nil {
		return "", errors.New("bash is unavailable and Git could not be located")
	}
	gitRoot := filepath.Dir(filepath.Dir(git))
	for _, candidate := range []string{
		filepath.Join(gitRoot, "bin", "bash.exe"),
		filepath.Join(gitRoot, "usr", "bin", "bash.exe"),
	} {
		if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", errors.New("bash is unavailable, including the Git for Windows installation")
}

func TestImportedReleaseValidatorRejectsDriftAndUnsafeEvidence(t *testing.T) {
	workflow, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "release-finalize.yml"))
	if err != nil {
		t.Fatal(err)
	}
	validator := extractPythonBlock(t, string(workflow), "imported-release-validator")
	for _, tc := range []struct {
		name   string
		mutate func(t *testing.T, fixture *importedReleaseFixture)
		valid  bool
	}{
		{name: "valid", valid: true},
		{name: "altered manifest digest", mutate: func(t *testing.T, fixture *importedReleaseFixture) {
			fixture.environment["EXPECTED_MANIFEST_DIGEST"] = strings.Repeat("0", 64)
		}},
		{name: "wrong source", mutate: func(t *testing.T, fixture *importedReleaseFixture) {
			fixture.environment["EXPECTED_SOURCE_SHA"] = strings.Repeat("f", 40)
		}},
		{name: "wrong version", mutate: func(t *testing.T, fixture *importedReleaseFixture) {
			fixture.environment["EXPECTED_VERSION"] = "0.1.1"
			fixture.environment["EXPECTED_TAG"] = "v0.1.1"
		}},
		{name: "stale producer", mutate: func(t *testing.T, fixture *importedReleaseFixture) {
			fixture.writeRun(t, time.Now().Add(-15*24*time.Hour))
		}},
		{name: "altered archive", mutate: func(t *testing.T, fixture *importedReleaseFixture) {
			archives, _ := filepath.Glob(filepath.Join(fixture.root, "candidates", "*", "*.gz"))
			if len(archives) == 0 {
				t.Fatal("fixture archive missing")
			}
			if err := os.WriteFile(archives[0], []byte("altered"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "altered release notes", mutate: func(t *testing.T, fixture *importedReleaseFixture) {
			if err := os.WriteFile(filepath.Join(fixture.root, "release-notes.md"), []byte("altered notes"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "missing verifier", mutate: func(t *testing.T, fixture *importedReleaseFixture) {
			if err := os.Remove(filepath.Join(fixture.root, "strict-release-verifier.py")); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "symlink", mutate: func(t *testing.T, fixture *importedReleaseFixture) {
			createTestSymlink(t, fixture.manifestPath, filepath.Join(fixture.root, "unsafe-link"))
		}},
		{name: "oversized", mutate: func(t *testing.T, fixture *importedReleaseFixture) {
			path := filepath.Join(fixture.root, "oversized")
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Truncate(path, 129<<20); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newImportedReleaseFixture(t)
			if tc.mutate != nil {
				tc.mutate(t, &fixture)
			}
			_, err := runPythonBlock(t, validator, []string{fixture.root, fixture.output, fixture.runPath}, fixture.environment)
			if (err == nil) != tc.valid {
				t.Fatalf("validator error=%v, valid=%t", err, tc.valid)
			}
		})
	}
}

func TestReleaseNotesRepairFailsClosedAndPatchesOnlyBody(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("publisher runs on ubuntu-latest; POSIX fake-command harness is not portable to Windows")
	}
	repair := extractPythonBlock(t, string(mustReadFile(t, filepath.Join("..", "..", ".github", "workflows", "release-finalize.yml"))), "release-notes-repair")
	notes := mustReadFile(t, filepath.Join("..", "..", "docs", "release", "V0.1.4-RELEASE-NOTES.md"))
	assets := []map[string]any{
		{"id": 511958192, "name": "ao-mission-0.1.4-linux-x86_64.tar.gz", "size": 6638215, "digest": "sha256:041d4b4ab076601bf6fe15335cb70a5d9f87301beb239e8e106b3ee4fd12f800"},
		{"id": 511958193, "name": "ao-mission-0.1.4-macos-aarch64.tar.gz", "size": 6329247, "digest": "sha256:d8b418e42b57306862c75fc10e5c347109c13c144a18e240d2a2edba29c1a34e"},
		{"id": 511958191, "name": "ao-mission-0.1.4-windows-x86_64.zip", "size": 6543779, "digest": "sha256:027ceba61e7b1d3655cce63a1ce4269824d7a5e3acf65fef5fabb0b539c53221"},
	}
	plan := map[string]any{"candidates": []any{
		map[string]any{"archive": assets[0]["name"], "archive_sha256": strings.TrimPrefix(assets[0]["digest"].(string), "sha256:")},
		map[string]any{"archive": assets[1]["name"], "archive_sha256": strings.TrimPrefix(assets[1]["digest"].(string), "sha256:")},
		map[string]any{"archive": assets[2]["name"], "archive_sha256": strings.TrimPrefix(assets[2]["digest"].(string), "sha256:")},
	}}
	fakeGH := `#!/usr/bin/env python3
import json, os, pathlib, sys
args = sys.argv[1:]
mode = os.environ.get("FAKE_MODE", "valid")
state = pathlib.Path(os.environ["FAKE_STATE"])
capture = pathlib.Path(os.environ["FAKE_CAPTURE"])
assets = json.loads(os.environ["FAKE_ASSETS"])
body = state.read_text() if state.exists() else None
release = {"id":369467111,"tag_name":"v0.1.4","target_commitish":"cee287597024b5a1e990c6e272518236bc9e32fa","name":"AO Mission 0.1.4","draft":False,"prerelease":False,"body":body,"assets":assets}
if mode == "wrong-id": release["id"] = 1
elif mode == "wrong-tag": release["tag_name"] = "v0.1.3"
elif mode == "wrong-source": release["target_commitish"] = "f" * 40
elif mode == "wrong-title": release["name"] = "wrong"
elif mode == "draft": release["draft"] = True
elif mode == "prerelease": release["prerelease"] = True
elif mode in ("nonempty", "repeat"): release["body"] = "already populated"
elif mode == "extra-asset": release["assets"] = assets + [{"id":1,"name":"extra","size":1,"digest":"sha256:00"}]
elif mode == "wrong-asset-id": release["assets"][0]["id"] = 1
if args[0] != "api": raise SystemExit(2)
method = "PATCH" if "--method" in args and args[args.index("--method") + 1] == "PATCH" else "GET"
endpoint = next((a for a in args if a.startswith("repos/")), "")
if "/git/ref/tags/" in endpoint:
    sha = "f" * 40 if mode == "wrong-tag-source" else "cee287597024b5a1e990c6e272518236bc9e32fa"
    print(json.dumps({"object":{"type":"commit","sha":sha}})); raise SystemExit
if "/releases/assets/" in endpoint:
    sys.stdout.buffer.write(b"sealed asset"); raise SystemExit
if endpoint.endswith("/releases/369467111") and method == "PATCH":
    request = json.load(open(args[args.index("--input") + 1], encoding="utf-8"))
    if list(request) != ["body"]: raise SystemExit("PATCH contains fields other than body")
    capture.write_text(json.dumps(request), encoding="utf-8")
    state.write_text(request["body"], encoding="utf-8")
    release["body"] = request["body"]
    print(json.dumps(release)); raise SystemExit
if endpoint.endswith("/releases/369467111"):
    print(json.dumps(release)); raise SystemExit
raise SystemExit(2)
`
	fakeSHA := `#!/usr/bin/env python3
import os, pathlib, sys
sys.stdin.buffer.read()
counter = pathlib.Path(os.environ["FAKE_SHA_COUNT"])
n = int(counter.read_text()) if counter.exists() else 0
counter.write_text(str(n + 1))
values = [
"9a84817e6d75b197c72a3219f7f851cb31935da679688bb14e8560eea0bf1022",
"041d4b4ab076601bf6fe15335cb70a5d9f87301beb239e8e106b3ee4fd12f800",
"d8b418e42b57306862c75fc10e5c347109c13c144a18e240d2a2edba29c1a34e",
"027ceba61e7b1d3655cce63a1ce4269824d7a5e3acf65fef5fabb0b539c53221"]
if os.environ.get("FAKE_MODE") == "digest-drift" and n == 1: print("0" * 64)
else: print(values[min(n, len(values) - 1)])
`
	run := func(t *testing.T, mode string) ([]byte, bool, error) {
		t.Helper()
		dir := t.TempDir()
		bin := filepath.Join(dir, "bin")
		if err := os.Mkdir(bin, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, body := range map[string]string{"gh": fakeGH, "sha256sum": fakeSHA} {
			if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		validated := filepath.Join(dir, "validated")
		if err := os.Mkdir(validated, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(validated, "release-notes.md"), notes, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(validated, "immutable-promotion-plan.json"), marshalJSON(t, plan), 0o600); err != nil {
			t.Fatal(err)
		}
		capture := filepath.Join(dir, "capture.json")
		command := exec.Command("bash", "-euo", "pipefail", "-c", "notes=validated/release-notes.md; plan=validated/immutable-promotion-plan.json\n"+repair)
		command.Dir = dir
		assetJSON, _ := json.Marshal(assets)
		command.Env = append(os.Environ(),
			"PATH="+bin+":"+os.Getenv("PATH"), "FAKE_MODE="+mode, "FAKE_STATE="+filepath.Join(dir, "state"),
			"FAKE_CAPTURE="+capture, "FAKE_ASSETS="+string(assetJSON), "FAKE_SHA_COUNT="+filepath.Join(dir, "sha-count"),
			"GITHUB_REPOSITORY=uesugitorachiyo/ao-mission", "PRODUCER_RUN_ID=31630121755", "VERSION=0.1.4", "TAG=v0.1.4",
			"SOURCE_SHA=cee287597024b5a1e990c6e272518236bc9e32fa", "EXPECTED_MANIFEST_DIGEST=ec21a5639a582d3f8c520053bc5b72974a1b333e26b8f09696fe6cb695873d22",
		)
		output, err := command.CombinedOutput()
		_, captureErr := os.Stat(capture)
		patched := captureErr == nil
		if err != nil {
			return output, patched, err
		}
		return mustReadFile(t, capture), patched, nil
	}
	request, patched, err := run(t, "valid")
	if err != nil {
		t.Fatalf("valid repair failed: %v", err)
	}
	if !patched {
		t.Fatal("valid repair did not PATCH the release body")
	}
	var payload map[string]any
	if err := json.Unmarshal(request, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload) != 1 || payload["body"] != string(notes) {
		t.Fatalf("PATCH payload=%v, want exact body only", payload)
	}
	for _, mode := range []string{"wrong-id", "wrong-tag", "wrong-source", "wrong-title", "draft", "prerelease", "nonempty", "repeat", "extra-asset", "wrong-asset-id", "wrong-tag-source", "digest-drift"} {
		t.Run(mode, func(t *testing.T) {
			if _, patched, err := run(t, mode); err == nil {
				t.Fatalf("unsafe repair mode %q succeeded", mode)
			} else if patched {
				t.Fatalf("unsafe repair mode %q mutated the release before failing", mode)
			}
		})
	}
}

type importedReleaseFixture struct {
	root         string
	output       string
	runPath      string
	manifestPath string
	environment  map[string]string
}

func newImportedReleaseFixture(t *testing.T) importedReleaseFixture {
	t.Helper()
	verifier := extractPythonBlock(t, readReleaseWorkflow(t), "strict-release-verifier")
	base := writeReleaseVerifierFixture(t, verifier, nil)
	root := filepath.Dir(base.manifestPath)
	if err := os.WriteFile(filepath.Join(root, "strict-release-verifier.py"), []byte(verifier), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "release-notes.md"), []byte("approved notes"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture := importedReleaseFixture{
		root: root, output: filepath.Join(t.TempDir(), "validated"),
		runPath: filepath.Join(root, "producer-run.json"), manifestPath: base.manifestPath,
		environment: map[string]string{
			"EXPECTED_MANIFEST_DIGEST": base.environment["APPROVED_MANIFEST_DIGEST"],
			"EXPECTED_SOURCE_SHA":      base.environment["SOURCE_SHA"],
			"EXPECTED_TAG":             base.environment["RELEASE_TAG"],
			"EXPECTED_VERSION":         base.environment["RELEASE_VERSION"],
			"PRODUCER_RUN_ID":          "1234", "DRY_RUN": "true",
		},
	}
	if err := os.MkdirAll(fixture.output, 0o755); err != nil {
		t.Fatal(err)
	}
	fixture.writeRun(t, time.Now())
	return fixture
}

func (fixture importedReleaseFixture) writeRun(t *testing.T, created time.Time) {
	t.Helper()
	body := marshalJSON(t, map[string]any{"created_at": created.UTC().Format(time.RFC3339)})
	if err := os.WriteFile(fixture.runPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestReleaseNotesAreCommittedAndBoundToExactHead(t *testing.T) {
	notesPath := filepath.Join("..", "..", "docs", "release", "V0.1.4-RELEASE-NOTES.md")
	notes, err := os.ReadFile(notesPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"# AO Mission v0.1.4 Release Notes",
		"immutable release finalization",
		"correlation-bound compaction readbacks",
		"Linux x86_64",
		"macOS aarch64",
		"Windows x86_64",
		"execute downstream repository mutations",
		"RSI remains denied",
	} {
		if !bytes.Contains(notes, []byte(want)) {
			t.Fatalf("release notes missing %q", want)
		}
	}

	workflow := readReleaseWorkflow(t)
	for _, want := range []string{
		"release_notes_path: ${{ steps.release-notes.outputs.release_notes_path }}",
		`release_notes_path="docs/release/V${RELEASE_VERSION}-RELEASE-NOTES.md"`,
		`expected_heading="# AO Mission v${RELEASE_VERSION} Release Notes"`,
		`git cat-file blob "${SOURCE_SHA}:${release_notes_path}" > "$release_notes_blob"`,
		`release_notes_sha256=$(sha256sum < "$release_notes_blob" | awk '{print $1}')`,
		`git cat-file blob "${SOURCE_SHA}:${RELEASE_NOTES_PATH}" > "$approved_release_notes"`,
		`[ "$actual_release_notes_sha256" = "$RELEASE_NOTES_SHA256" ]`,
		`--notes-file "$approved_release_notes"`,
	} {
		if !strings.Contains(workflow, want) {
			t.Fatalf("release workflow missing committed-note binding %q", want)
		}
	}
	if strings.Contains(workflow, "${{ inputs.release_notes }}") {
		t.Fatal("release workflow still accepts uncommitted release-note text")
	}
}

func TestV016ReleaseNotesBoundCandidateAndCheckpointAuthority(t *testing.T) {
	notes := mustReadFile(t, filepath.Join("..", "..", "docs", "release", "V0.1.6-RELEASE-NOTES.md"))
	for _, want := range []string{
		"# AO Mission v0.1.6 Release Notes",
		"ao.next.live-run-record.v1",
		"S01, S02, S03, S04, S05, S06, S07",
		"idempotent",
		"execution: false",
		"approval: false",
		"repository_mutation: false",
		"provider_calls: false",
		"publication: false",
		"promotion: false",
		"compatibility activation",
		"beta",
		"RSI",
		"AO Office Pool",
	} {
		if !bytes.Contains(notes, []byte(want)) {
			t.Fatalf("v0.1.6 release notes missing %q", want)
		}
	}
}

func TestPublishedReleaseVerifierBindsRepositoryWithoutCheckout(t *testing.T) {
	workflow := readReleaseWorkflow(t)
	verifier := strings.Split(workflow, "  verify-published-release:")[1]
	want := `gh release download "$RELEASE_TAG" --repo "$GITHUB_REPOSITORY" --dir target/release-rehearsal/public-assets`
	if !strings.Contains(verifier, want) {
		t.Fatalf("published release verifier missing explicit repository binding %q", want)
	}
}

func TestApprovedManifestDecoderRejectsMalformedAndOversizedInput(t *testing.T) {
	decoder := extractPythonBlock(t, readReleaseWorkflow(t), "approved-manifest-decoder")
	run := func(t *testing.T, encoded string) ([]byte, error) {
		t.Helper()
		path := filepath.Join(t.TempDir(), "manifest.json")
		_, err := runPythonBlock(t, decoder, []string{path}, map[string]string{
			"APPROVED_MANIFEST_BASE64": encoded,
		})
		if err != nil {
			return nil, err
		}
		raw, readErr := os.ReadFile(path)
		return raw, readErr
	}

	valid := []byte(`{"schema_version":"test"}`)
	raw, err := run(t, base64.StdEncoding.EncodeToString(valid))
	if err != nil {
		t.Fatalf("valid bounded manifest rejected: %v", err)
	}
	if !bytes.Equal(raw, valid) {
		t.Fatalf("decoded manifest=%q, want exact bytes %q", raw, valid)
	}
	if _, err := run(t, "%%%"); err == nil {
		t.Fatal("malformed base64 manifest accepted")
	}
	oversized := bytes.Repeat([]byte("a"), 32769)
	if _, err := run(t, base64.StdEncoding.EncodeToString(oversized)); err == nil {
		t.Fatal("oversized decoded manifest accepted")
	}
}

func TestReleaseManifestValidatorRejectsMissingMalformedAndSubstitutedInputs(t *testing.T) {
	workflow := readReleaseWorkflow(t)
	validator := extractPythonBlock(t, workflow, "release-manifest-validator")
	dir := t.TempDir()
	versionSource := filepath.Join(dir, "AO-MISSION-V0.1.md")
	versionSourceBytes := []byte("# AO Mission v0.1 SDD\n")
	if err := os.WriteFile(versionSource, versionSourceBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	sourceSHA := strings.Repeat("a", 40)
	versionSourceDigest := sha256Hex(versionSourceBytes)
	valid := releaseManifestFixture(sourceSHA, versionSourceDigest)

	run := func(t *testing.T, raw []byte, digest, path string) error {
		t.Helper()
		outputs := filepath.Join(t.TempDir(), "outputs")
		_, err := runPythonBlock(t, validator, []string{path, versionSource, outputs}, map[string]string{
			"APPROVED_MANIFEST_DIGEST": digest,
			"RELEASE_TAG":              "v0.1.0",
			"RELEASE_VERSION":          "0.1.0",
			"SOURCE_SHA":               sourceSHA,
		})
		return err
	}
	writeManifest := func(t *testing.T, raw []byte) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "manifest.json")
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}

	validBytes := marshalJSON(t, valid)
	if err := run(t, validBytes, sha256Hex(validBytes), writeManifest(t, validBytes)); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}

	t.Run("missing", func(t *testing.T) {
		if err := run(t, nil, strings.Repeat("0", 64), filepath.Join(t.TempDir(), "missing.json")); err == nil {
			t.Fatal("missing manifest accepted")
		}
	})
	t.Run("malformed", func(t *testing.T) {
		raw := []byte("{")
		if err := run(t, raw, sha256Hex(raw), writeManifest(t, raw)); err == nil {
			t.Fatal("malformed manifest accepted")
		}
	})
	t.Run("arbitrary-digest", func(t *testing.T) {
		if err := run(t, validBytes, strings.Repeat("0", 64), writeManifest(t, validBytes)); err == nil {
			t.Fatal("manifest with non-matching approved digest accepted")
		}
	})
	t.Run("source-drift", func(t *testing.T) {
		manifest := cloneJSONMap(t, valid)
		manifest["source_sha"] = strings.Repeat("b", 40)
		raw := marshalJSON(t, manifest)
		if err := run(t, raw, sha256Hex(raw), writeManifest(t, raw)); err == nil {
			t.Fatal("manifest source drift accepted")
		}
	})
	t.Run("tag-drift", func(t *testing.T) {
		manifest := cloneJSONMap(t, valid)
		manifest["tag"] = "v0.1.1"
		raw := marshalJSON(t, manifest)
		if err := run(t, raw, sha256Hex(raw), writeManifest(t, raw)); err == nil {
			t.Fatal("manifest tag drift accepted")
		}
	})
	t.Run("candidate-inventory-substitution", func(t *testing.T) {
		manifest := cloneJSONMap(t, valid)
		artifacts := manifest["artifacts"].([]any)
		artifacts[0].(map[string]any)["archive"] = "substituted.tar.gz"
		raw := marshalJSON(t, manifest)
		if err := run(t, raw, sha256Hex(raw), writeManifest(t, raw)); err == nil {
			t.Fatal("substituted candidate inventory accepted")
		}
	})
}

func TestRemoteReleaseStateValidatorFailsClosed(t *testing.T) {
	validator := extractPythonBlock(t, readReleaseWorkflow(t), "release-state-validator")
	sourceSHA := strings.Repeat("a", 40)
	run := func(t *testing.T, state map[string]any) error {
		t.Helper()
		dir := t.TempDir()
		statePath := filepath.Join(dir, "remote-state.json")
		if err := os.WriteFile(statePath, marshalJSON(t, state), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := runPythonBlock(t, validator, []string{statePath, filepath.Join(dir, "readback.json")}, map[string]string{
			"RELEASE_TAG": "v0.1.0",
			"SOURCE_SHA":  sourceSHA,
		})
		return err
	}

	for name, state := range map[string]map[string]any{
		"no-existing-tag-or-release": {
			"release_http_status": 404,
			"tag_exists":          false,
			"tag_source_sha":      nil,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := run(t, state); err != nil {
				t.Fatalf("safe remote state rejected: %v", err)
			}
		})
	}
	for name, state := range map[string]map[string]any{
		"tag-source-drift": {
			"release_http_status": 404,
			"tag_exists":          true,
			"tag_source_sha":      strings.Repeat("b", 40),
		},
		"exact-existing-tag-without-release": {
			"release_http_status": 404,
			"tag_exists":          true,
			"tag_source_sha":      sourceSHA,
		},
		"existing-release": {
			"release_http_status": 200,
			"tag_exists":          true,
			"tag_source_sha":      sourceSHA,
		},
		"unknown-release-state": {
			"release_http_status": 500,
			"tag_exists":          false,
			"tag_source_sha":      nil,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := run(t, state); err == nil {
				t.Fatalf("unsafe remote state %v accepted", state)
			}
		})
	}
}

func TestEnvironmentGateValidatorRequiresProtectedEnvironment(t *testing.T) {
	validator := extractPythonBlock(t, readReleaseWorkflow(t), "environment-gate-validator")
	run := func(t *testing.T, state map[string]any) (map[string]any, error) {
		t.Helper()
		dir := t.TempDir()
		statePath := filepath.Join(dir, "environment.json")
		readbackPath := filepath.Join(dir, "readback.json")
		if err := os.WriteFile(statePath, marshalJSON(t, state), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := runPythonBlock(t, validator, []string{statePath, readbackPath}, map[string]string{
			"RELEASE_ENVIRONMENT": "ao-mission-release",
		})
		var readback map[string]any
		if raw, readErr := os.ReadFile(readbackPath); readErr == nil {
			if jsonErr := json.Unmarshal(raw, &readback); jsonErr != nil {
				t.Fatal(jsonErr)
			}
		}
		return readback, err
	}

	protected := map[string]any{
		"name": "ao-mission-release",
		"protection_rules": []any{
			map[string]any{
				"type": "required_reviewers",
				"reviewers": []any{
					map[string]any{"type": "User"},
				},
			},
		},
	}
	readback, err := run(t, protected)
	if err != nil {
		t.Fatalf("protected environment rejected: %v", err)
	}
	if readback["status"] != "ready" || readback["protected"] != true {
		t.Fatalf("environment readback=%v, want ready and protected", readback)
	}

	unprotected := map[string]any{"name": "ao-mission-release", "protection_rules": []any{}}
	readback, err = run(t, unprotected)
	if err == nil {
		t.Fatal("unprotected environment accepted")
	}
	if readback["status"] != "blocked" || readback["protected"] != false {
		t.Fatalf("blocked environment readback=%v", readback)
	}
}

func TestDryRunBoundaryAllowsPrivateArtifactsButNoPublicMutation(t *testing.T) {
	writer := extractPythonBlock(t, readReleaseWorkflow(t), "dry-run-boundary-writer")
	path := filepath.Join(t.TempDir(), "dry-run-boundary.json")
	if _, err := runPythonBlock(t, writer, []string{path}, map[string]string{
		"DRY_RUN":     "true",
		"RELEASE_TAG": "v0.1.0",
		"SOURCE_SHA":  strings.Repeat("a", 40),
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var boundary map[string]any
	if err := json.Unmarshal(raw, &boundary); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{
		"private_candidate_artifact_uploads_performed",
		"private_plan_artifact_upload_authorized",
	} {
		if boundary[field] != true {
			t.Fatalf("%s=%v, want true", field, boundary[field])
		}
	}
	for _, field := range []string{
		"deployment_attempted",
		"publication_performed",
		"public_release_asset_upload_attempted",
		"release_creation_attempted",
		"tag_creation_attempted",
	} {
		if boundary[field] != false {
			t.Fatalf("%s=%v, want false", field, boundary[field])
		}
	}
}

func TestNativeCandidatesEmitSeparateHelpVersionAndFunctionalSmokeEvidence(t *testing.T) {
	workflow := readReleaseWorkflow(t)
	for _, want := range []string{
		"help-evidence.json",
		"version-evidence.json",
		"version-output.txt",
		"functional-smoke-evidence.json",
		"validate contract --path examples/valid/mission-record.json",
		`if json.load(open(sys.argv[1], encoding="utf-8")).get("status") != "ready":`,
		"provider_calls",
		"-X github.com/uesugitorachiyo/ao-mission/internal/mission.BuildVersion=${RELEASE_VERSION}",
		"-X github.com/uesugitorachiyo/ao-mission/internal/mission.BuildSourceSHA=${SOURCE_SHA}",
		`version_output=$("$package_dir/$binary" --version)`,
		`version_output=${version_output%$'\r'}`,
		`[ "$version_output" = "$expected_version_output" ]`,
		`git cat-file blob "${SOURCE_SHA}:${VERSION_SOURCE_PATH}" > "$version_source_blob"`,
		`actual_version_source_sha256=$(sha256sum < "$version_source_blob" | awk '{print $1}')`,
		`actual_version_source_sha256=$(shasum -a 256 < "$version_source_blob" | awk '{print $1}')`,
		`binary_sha256=$(sha256sum < "$package_dir/$binary" | awk '{print $1}')`,
		`binary_sha256=$(shasum -a 256 < "$package_dir/$binary" | awk '{print $1}')`,
		`archive_sha256=$(sha256sum < "$artifact_dir/$archive" | awk '{print $1}')`,
		`archive_sha256=$(shasum -a 256 < "$artifact_dir/$archive" | awk '{print $1}')`,
		`printf '%s  %s\n' "$archive_sha256" "$archive" > "$artifact_dir/SHA256SUMS"`,
		`provenance_sha256=$(sha256sum < "$artifact_dir/provenance.json" | awk '{print $1}')`,
		`provenance_sha256=$(shasum -a 256 < "$artifact_dir/provenance.json" | awk '{print $1}')`,
		"docs/sdd/AO-MISSION-V0.1.md",
		"DISPATCH_SHA: ${{ github.sha }}",
		`[ "$SOURCE_SHA" = "$DISPATCH_SHA" ]`,
		"environment-gate-readback.json",
		"release-preflight-readback.json",
		"base64.b64decode(encoded, validate=True)",
		"if len(encoded) > 49152:",
		"if not manifest_bytes or len(manifest_bytes) > 32768:",
	} {
		if !strings.Contains(workflow, want) {
			t.Fatalf("release workflow missing %q", want)
		}
	}
	if strings.Contains(workflow, `"smoke":{"command":"no-args-usage"`) {
		t.Fatal("workflow still classifies no-argument failure as functional smoke")
	}
	if strings.Contains(workflow, `grep -F '"status": "ready"'`) {
		t.Fatal("workflow must parse functional smoke JSON instead of matching its formatting")
	}
	if strings.Contains(workflow, `sha256sum "$VERSION_SOURCE_PATH"`) ||
		strings.Contains(workflow, `shasum -a 256 "$VERSION_SOURCE_PATH"`) {
		t.Fatal("workflow must hash exact committed version-source bytes, not a checkout-normalized working-tree file")
	}
	for _, unsafe := range []string{
		`sha256sum "$version_source_blob"`,
		`shasum -a 256 "$version_source_blob"`,
		`sha256sum "$package_dir/$binary"`,
		`shasum -a 256 "$package_dir/$binary"`,
		`sha256sum "$archive" > SHA256SUMS`,
		`shasum -a 256 "$archive" > SHA256SUMS`,
		`sha256sum "$artifact_dir/provenance.json"`,
		`shasum -a 256 "$artifact_dir/provenance.json"`,
	} {
		if strings.Contains(workflow, unsafe) {
			t.Fatalf("workflow hashes a Windows path as a sha256sum filename, allowing an escaped digest marker: %s", unsafe)
		}
	}
}

func TestCandidateVersionCommandReportsLinkerBoundVersionAndSource(t *testing.T) {
	sourceSHA := strings.Repeat("a", 40)
	binaryName := "ao-mission"
	if runtime.GOOS == "windows" {
		binaryName += ".exe"
	}
	binaryPath := filepath.Join(t.TempDir(), binaryName)
	ldflags := strings.Join([]string{
		"-X github.com/uesugitorachiyo/ao-mission/internal/mission.BuildVersion=0.1.0",
		"-X github.com/uesugitorachiyo/ao-mission/internal/mission.BuildSourceSHA=" + sourceSHA,
	}, " ")
	build := exec.Command("go", "build", "-trimpath", "-ldflags", ldflags, "-o", binaryPath, "../../cmd/ao-mission")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build candidate: %v\n%s", err, output)
	}
	command := exec.Command(binaryPath, "--version")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("candidate --version failed: %v\n%s", err, output)
	}
	want := "ao-mission version=0.1.0 source_sha=" + sourceSHA + "\n"
	if string(output) != want {
		t.Fatalf("candidate --version output = %q, want %q", output, want)
	}
}

func TestVersionCommandDefaultsToDevelopmentIdentity(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"--version"}, &stdout, &stderr); code != 0 {
		t.Fatalf("development --version failed: code=%d stderr=%q", code, stderr.String())
	}
	if got, want := stdout.String(), "ao-mission version=dev source_sha=unknown\n"; got != want {
		t.Fatalf("development --version output = %q, want %q", got, want)
	}
}

func TestNativeMatrixBindsExactRunnerAndGoIdentity(t *testing.T) {
	workflow := readReleaseWorkflow(t)
	for _, want := range []string{
		"os: ubuntu-24.04",
		"os: macos-15",
		"os: windows-2025",
		"expected_runner_os: Linux",
		"expected_runner_os: macOS",
		"expected_runner_os: Windows",
		"expected_runner_arch: X64",
		"expected_runner_arch: ARM64",
		"expected_goos: linux",
		"expected_goos: darwin",
		"expected_goos: windows",
		"expected_goarch: amd64",
		"expected_goarch: arm64",
		`[ "$RUNNER_OS" = "$EXPECTED_RUNNER_OS" ]`,
		`[ "$RUNNER_ARCH" = "$EXPECTED_RUNNER_ARCH" ]`,
		`[ "$(go env GOOS)" = "$EXPECTED_GOOS" ]`,
		`[ "$(go env GOARCH)" = "$EXPECTED_GOARCH" ]`,
	} {
		if !strings.Contains(workflow, want) {
			t.Fatalf("release workflow missing exact native identity binding %q", want)
		}
	}
	for _, forbidden := range []string{
		"os: macos-latest",
		"os: ubuntu-latest",
		"os: windows-latest",
	} {
		if strings.Contains(workflow, forbidden) {
			t.Fatalf("release workflow uses mutable runner architecture label %q", forbidden)
		}
	}
}

func TestStrictReleaseVerifierRejectsWrongVersionAndTargetSubstitution(t *testing.T) {
	verifier := extractPythonBlock(t, readReleaseWorkflow(t), "strict-release-verifier")

	t.Run("valid-candidates", func(t *testing.T) {
		fixture := writeReleaseVerifierFixture(t, verifier, nil)
		if output, err := runStrictVerifier(t, verifier, fixture, "candidates"); err != nil {
			t.Fatalf("valid candidates rejected: %v\n%s", err, output)
		}
	})
	t.Run("wrong-version", func(t *testing.T) {
		fixture := writeReleaseVerifierFixture(t, verifier, func(target releaseTargetFixture, files map[string][]byte, summary, provenance map[string]any) {
			if target.targetLabel == "linux-x86_64" {
				summary["version"] = "0.1.1"
				provenance["version"] = "0.1.1"
				files["provenance.json"] = marshalJSON(t, provenance)
			}
		})
		if _, err := runStrictVerifier(t, verifier, fixture, "candidates"); err == nil {
			t.Fatal("coherent wrong-version candidate accepted")
		}
	})
	t.Run("self-asserted-version-output", func(t *testing.T) {
		fixture := writeReleaseVerifierFixture(t, verifier, func(target releaseTargetFixture, files map[string][]byte, summary, _ map[string]any) {
			if target.targetLabel == "linux-x86_64" {
				output := "ao-mission version=0.1.1 source_sha=" + strings.Repeat("b", 40)
				evidence := summary["version_evidence"].(map[string]any)
				evidence["release_version"] = "0.1.1"
				evidence["source_sha"] = strings.Repeat("b", 40)
				evidence["output"] = output
				files["version-output.txt"] = []byte(output + "\n")
				files["version-evidence.json"] = marshalJSON(t, evidence)
			}
		})
		if _, err := runStrictVerifier(t, verifier, fixture, "candidates"); err == nil {
			t.Fatal("self-asserted substituted candidate --version output accepted")
		}
	})
	t.Run("binary-format-substitution", func(t *testing.T) {
		fixture := writeReleaseVerifierFixture(t, verifier, func(target releaseTargetFixture, files map[string][]byte, summary, provenance map[string]any) {
			if target.targetLabel == "linux-x86_64" {
				files[target.entryPoint] = fixtureBinary("pe-x86_64")
				summary["binary_sha256"] = sha256Hex(files[target.entryPoint])
				provenance["binary_sha256"] = summary["binary_sha256"]
				files["provenance.json"] = marshalJSON(t, provenance)
			}
		})
		if _, err := runStrictVerifier(t, verifier, fixture, "candidates"); err == nil {
			t.Fatal("PE binary substituted into Linux candidate accepted")
		}
	})
	t.Run("archive-traversal", func(t *testing.T) {
		fixture := writeReleaseVerifierFixture(t, verifier, func(target releaseTargetFixture, files map[string][]byte, _, _ map[string]any) {
			if target.targetLabel == "linux-x86_64" {
				files["../escape"] = []byte("escape")
			}
		})
		if _, err := runStrictVerifier(t, verifier, fixture, "candidates"); err == nil {
			t.Fatal("archive traversal member accepted")
		}
	})
}

func TestStrictReleaseVerifierRejectsMalformedAndSemanticPlanMutation(t *testing.T) {
	verifier := extractPythonBlock(t, readReleaseWorkflow(t), "strict-release-verifier")

	t.Run("valid-plan", func(t *testing.T) {
		fixture := writeReleaseVerifierFixture(t, verifier, nil)
		if output, err := runStrictVerifier(t, verifier, fixture, "plan"); err != nil {
			t.Fatalf("valid plan rejected: %v\n%s", err, output)
		}
	})
	t.Run("malformed-plan-with-recomputed-sidecar", func(t *testing.T) {
		fixture := writeReleaseVerifierFixture(t, verifier, nil)
		raw := []byte("{")
		if err := os.WriteFile(fixture.planPath, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		writePlanChecksum(t, fixture.planChecksum, raw)
		if _, err := runStrictVerifier(t, verifier, fixture, "plan"); err == nil {
			t.Fatal("malformed plan accepted after recomputing sidecar")
		}
	})
	t.Run("semantic-plan-mutation-with-recomputed-sidecar", func(t *testing.T) {
		fixture := writeReleaseVerifierFixture(t, verifier, nil)
		var plan map[string]any
		raw, err := os.ReadFile(fixture.planPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &plan); err != nil {
			t.Fatal(err)
		}
		plan["repository"] = "substituted-repository"
		raw = marshalJSON(t, plan)
		if err := os.WriteFile(fixture.planPath, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		writePlanChecksum(t, fixture.planChecksum, raw)
		if _, err := runStrictVerifier(t, verifier, fixture, "plan"); err == nil {
			t.Fatal("semantically mutated plan accepted after recomputing sidecar")
		}
	})
}

func readReleaseWorkflow(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "release-rehearsal.yml"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func parseWorkflowShape(t *testing.T, workflow string) workflowShape {
	t.Helper()
	shape := workflowShape{
		events:         map[string]bool{},
		inputs:         map[string]map[string]string{},
		topPermissions: map[string]string{},
		jobs:           map[string]*workflowJob{},
	}
	section := ""
	event := ""
	input := ""
	jobName := ""
	jobSection := ""
	for lineNumber, line := range strings.Split(workflow, "\n") {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		trimmed := strings.TrimSpace(line)
		if indent == 0 {
			if strings.HasSuffix(trimmed, ":") {
				section = strings.TrimSuffix(trimmed, ":")
				event, input, jobName, jobSection = "", "", "", ""
			} else {
				section = ""
			}
			continue
		}
		switch section {
		case "on":
			if indent == 2 && strings.HasSuffix(trimmed, ":") {
				event = strings.TrimSuffix(trimmed, ":")
				shape.events[event] = true
				continue
			}
			if event == "workflow_dispatch" && indent == 6 && strings.HasSuffix(trimmed, ":") {
				input = strings.TrimSuffix(trimmed, ":")
				shape.inputs[input] = map[string]string{}
				continue
			}
			if input != "" && indent == 8 {
				key, value, ok := yamlField(trimmed)
				if ok {
					shape.inputs[input][key] = value
				}
			}
		case "permissions":
			if indent == 2 {
				key, value, ok := yamlField(trimmed)
				if ok {
					shape.topPermissions[key] = value
				}
			}
		case "jobs":
			if indent == 2 && strings.HasSuffix(trimmed, ":") {
				jobName = strings.TrimSuffix(trimmed, ":")
				shape.jobs[jobName] = &workflowJob{permissions: map[string]string{}}
				jobSection = ""
				continue
			}
			if jobName == "" {
				continue
			}
			job := shape.jobs[jobName]
			if indent == 4 && strings.HasSuffix(trimmed, ":") {
				jobSection = strings.TrimSuffix(trimmed, ":")
				continue
			}
			if indent == 4 {
				key, value, ok := yamlField(trimmed)
				if !ok {
					continue
				}
				switch key {
				case "if":
					job.condition = value
				case "environment":
					job.environment = value
				}
				jobSection = ""
				continue
			}
			if jobSection == "permissions" && indent == 6 {
				key, value, ok := yamlField(trimmed)
				if ok {
					job.permissions[key] = value
				}
			}
			if jobSection == "steps" && indent == 8 {
				key, value, ok := yamlField(trimmed)
				if ok && key == "uses" {
					job.uses = append(job.uses, value)
				}
			}
		default:
			t.Fatalf("line %d is under unknown top-level section %q", lineNumber+1, section)
		}
	}
	return shape
}

func yamlField(line string) (string, string, bool) {
	key, value, ok := strings.Cut(line, ":")
	if !ok || strings.TrimSpace(value) == "" {
		return "", "", false
	}
	value = strings.TrimSpace(value)
	if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
		value = value[1 : len(value)-1]
	}
	return strings.TrimSpace(key), value, true
}

func extractPythonBlock(t *testing.T, workflow, name string) string {
	t.Helper()
	begin := "# " + name + "-begin"
	end := "# " + name + "-end"
	lines := strings.Split(workflow, "\n")
	start, finish := -1, -1
	for i, line := range lines {
		switch strings.TrimSpace(line) {
		case begin:
			start = i + 1
		case end:
			finish = i
		}
	}
	if start < 0 || finish < start {
		t.Fatalf("workflow missing embedded Python block %q", name)
	}
	block := append([]string(nil), lines[start:finish]...)
	minIndent := -1
	for _, line := range block {
		if strings.TrimSpace(line) == "" {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		if minIndent < 0 || indent < minIndent {
			minIndent = indent
		}
	}
	for i := range block {
		if len(block[i]) >= minIndent {
			block[i] = block[i][minIndent:]
		}
	}
	return strings.Join(block, "\n") + "\n"
}

func runPythonBlock(t *testing.T, script string, args []string, environment map[string]string) (string, error) {
	t.Helper()
	scriptPath := filepath.Join(t.TempDir(), "validator.py")
	if err := os.WriteFile(scriptPath, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("python3", append([]string{scriptPath}, args...)...)
	command.Env = os.Environ()
	keys := make([]string, 0, len(environment))
	for key := range environment {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		command.Env = append(command.Env, key+"="+environment[key])
	}
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	err := command.Run()
	return output.String(), err
}

func runStrictVerifier(t *testing.T, verifier string, fixture releaseVerifierFixture, mode string) (string, error) {
	t.Helper()
	args := []string{
		mode,
		"--manifest", fixture.manifestPath,
		"--candidates", fixture.candidatesPath,
	}
	if mode == "candidates" {
		args = append(args, "--output", filepath.Join(t.TempDir(), "verified-candidates.json"))
	} else {
		args = append(args,
			"--plan", fixture.planPath,
			"--plan-checksum", fixture.planChecksum,
		)
	}
	return runPythonBlock(t, verifier, args, fixture.environment)
}

func writeReleaseVerifierFixture(
	t *testing.T,
	verifier string,
	mutate func(releaseTargetFixture, map[string][]byte, map[string]any, map[string]any),
) releaseVerifierFixture {
	t.Helper()
	root := t.TempDir()
	candidatesPath := filepath.Join(root, "candidates")
	if err := os.MkdirAll(candidatesPath, 0o755); err != nil {
		t.Fatal(err)
	}
	sourceSHA := strings.Repeat("a", 40)
	versionSourceDigest := strings.Repeat("b", 64)
	manifest := releaseManifestFixture(sourceSHA, versionSourceDigest)
	manifestBytes := marshalJSON(t, manifest)
	manifestPath := filepath.Join(root, "approved-release-manifest.json")
	if err := os.WriteFile(manifestPath, manifestBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	environment := map[string]string{
		"APPROVED_MANIFEST_DIGEST": sha256Hex(manifestBytes),
		"RELEASE_NOTES_SHA256":     sha256Hex([]byte("approved notes")),
		"RELEASE_TAG":              "v0.1.0",
		"RELEASE_VERSION":          "0.1.0",
		"SOURCE_SHA":               sourceSHA,
		"VERIFIER_SHA256":          sha256Hex([]byte(verifier)),
		"VERSION_SOURCE_PATH":      "docs/sdd/AO-MISSION-V0.1.md",
		"VERSION_SOURCE_SHA256":    versionSourceDigest,
		"WORKFLOW_REF":             "uesugitorachiyo/ao-mission/.github/workflows/release-rehearsal.yml@refs/heads/main",
	}
	for _, target := range releaseTargetFixtures("0.1.0") {
		writeCandidateFixture(t, candidatesPath, target, manifestBytes, environment, mutate)
	}

	plan := map[string]any{
		"approved_manifest_digest":    environment["APPROVED_MANIFEST_DIGEST"],
		"approved_manifest_inventory": manifest["artifacts"],
		"candidates":                  readCandidateSummaries(t, candidatesPath),
		"immutable":                   true,
		"release_notes_sha256":        environment["RELEASE_NOTES_SHA256"],
		"repository":                  "ao-mission",
		"schema_version":              "ao.release-rehearsal-promotion-plan.v0.3",
		"source_sha":                  sourceSHA,
		"tag":                         "v0.1.0",
		"verifier_sha256":             environment["VERIFIER_SHA256"],
		"version":                     "0.1.0",
	}
	planBytes := marshalJSON(t, plan)
	planPath := filepath.Join(root, "immutable-promotion-plan.json")
	if err := os.WriteFile(planPath, planBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	planChecksum := filepath.Join(root, "immutable-promotion-plan.sha256")
	writePlanChecksum(t, planChecksum, planBytes)
	return releaseVerifierFixture{
		candidatesPath: candidatesPath,
		environment:    environment,
		manifestPath:   manifestPath,
		planChecksum:   planChecksum,
		planPath:       planPath,
	}
}

func writeCandidateFixture(
	t *testing.T,
	root string,
	target releaseTargetFixture,
	manifestBytes []byte,
	environment map[string]string,
	mutate func(releaseTargetFixture, map[string][]byte, map[string]any, map[string]any),
) {
	t.Helper()
	binaryBytes := fixtureBinary(target.binaryFormat + "-" + target.machine)
	versionOutput := "ao-mission version=" + environment["RELEASE_VERSION"] + " source_sha=" + environment["SOURCE_SHA"]
	versionEvidence := map[string]any{
		"command":               "--version",
		"output":                versionOutput,
		"release_version":       environment["RELEASE_VERSION"],
		"source_sha":            environment["SOURCE_SHA"],
		"status":                "passed",
		"version_source":        environment["VERSION_SOURCE_PATH"],
		"version_source_sha256": environment["VERSION_SOURCE_SHA256"],
	}
	files := map[string][]byte{
		target.entryPoint:                binaryBytes,
		"LICENSE":                        []byte("license\n"),
		"NOTICE":                         []byte("notice\n"),
		"approved-release-manifest.json": manifestBytes,
		"help.txt":                       []byte("usage: ao-mission <command>\n"),
		"help-evidence.json":             marshalJSON(t, map[string]any{"command": "no-args-usage", "expected_exit": "nonzero", "status": "passed"}),
		"version-evidence.json":          marshalJSON(t, versionEvidence),
		"version-output.txt":             []byte(versionOutput + "\n"),
		"functional-smoke-evidence.json": marshalJSON(t, map[string]any{"command": "validate contract --path examples/valid/mission-record.json", "provider_calls": false, "status": "passed"}),
		"functional-smoke-output.json":   marshalJSON(t, validFunctionalSmokeOutput()),
		"sbom.json":                      marshalJSON(t, map[string]any{"GoVersion": "1.26.4", "Path": "github.com/uesugitorachiyo/ao-mission"}),
	}
	provenance := map[string]any{
		"approved_manifest_digest": environment["APPROVED_MANIFEST_DIGEST"],
		"archive":                  target.archive,
		"binary_format":            target.binaryFormat,
		"binary_sha256":            sha256Hex(binaryBytes),
		"go_version":               "go version go1.26.4 " + target.goos + "/" + target.goarch,
		"goarch":                   target.goarch,
		"goos":                     target.goos,
		"machine":                  target.machine,
		"release_notes_sha256":     environment["RELEASE_NOTES_SHA256"],
		"repository":               "ao-mission",
		"runner_arch":              target.runnerArch,
		"runner_label":             target.runnerLabel,
		"runner_os":                target.runnerOS,
		"schema_version":           "ao.release-rehearsal-provenance.v0.3",
		"source_sha":               environment["SOURCE_SHA"],
		"target_label":             target.targetLabel,
		"version":                  environment["RELEASE_VERSION"],
		"workflow_ref":             environment["WORKFLOW_REF"],
		"workflow_sha":             environment["SOURCE_SHA"],
	}
	summary := map[string]any{
		"approved_manifest_digest": environment["APPROVED_MANIFEST_DIGEST"],
		"archive":                  target.archive,
		"binary_format":            target.binaryFormat,
		"binary_sha256":            sha256Hex(binaryBytes),
		"checksum_file":            "SHA256SUMS",
		"functional_smoke":         map[string]any{"command": "validate contract --path examples/valid/mission-record.json", "provider_calls": false, "status": "passed"},
		"goarch":                   target.goarch,
		"goos":                     target.goos,
		"help":                     map[string]any{"command": "no-args-usage", "expected_exit": "nonzero", "status": "passed"},
		"machine":                  target.machine,
		"release_notes_sha256":     environment["RELEASE_NOTES_SHA256"],
		"repository":               "ao-mission",
		"runner_arch":              target.runnerArch,
		"runner_label":             target.runnerLabel,
		"runner_os":                target.runnerOS,
		"schema_version":           "ao.release-rehearsal-candidate.v0.3",
		"source_sha":               environment["SOURCE_SHA"],
		"target_label":             target.targetLabel,
		"version":                  environment["RELEASE_VERSION"],
		"version_evidence":         versionEvidence,
	}
	if mutate != nil {
		mutate(target, files, summary, provenance)
	}
	binaryBytes = files[target.entryPoint]
	binaryDigest := sha256Hex(binaryBytes)
	summary["binary_sha256"] = binaryDigest
	provenance["binary_sha256"] = binaryDigest
	provenanceBytes := marshalJSON(t, provenance)
	files["provenance.json"] = provenanceBytes

	candidateDir := filepath.Join(root, target.targetLabel)
	if err := os.MkdirAll(candidateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(candidateDir, target.archive)
	writeCandidateArchive(t, archivePath, target.archive, files)
	archiveBytes, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	archiveDigest := sha256Hex(archiveBytes)
	summary["archive_sha256"] = archiveDigest
	summary["provenance_sha256"] = sha256Hex(provenanceBytes)
	if err := os.WriteFile(filepath.Join(candidateDir, "SHA256SUMS"), []byte(fmt.Sprintf("%s  %s\n", archiveDigest, target.archive)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(candidateDir, "candidate-summary.json"), marshalJSON(t, summary), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(candidateDir, "provenance.json"), provenanceBytes, 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeCandidateArchive(t *testing.T, path, archiveName string, files map[string][]byte) {
	t.Helper()
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if strings.HasSuffix(archiveName, ".zip") {
		writer := zip.NewWriter(file)
		for _, name := range names {
			header := &zip.FileHeader{Name: name, Method: zip.Deflate}
			header.SetMode(0o644)
			entry, createErr := writer.CreateHeader(header)
			if createErr != nil {
				t.Fatal(createErr)
			}
			if _, writeErr := entry.Write(files[name]); writeErr != nil {
				t.Fatal(writeErr)
			}
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		return
	}
	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)
	for _, name := range names {
		header := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(files[name])), Typeflag: tar.TypeReg}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write(files[name]); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
}

func fixtureBinary(identity string) []byte {
	switch identity {
	case "elf-x86_64":
		raw := make([]byte, 64)
		copy(raw, []byte{0x7f, 'E', 'L', 'F'})
		raw[4], raw[5] = 2, 1
		binary.LittleEndian.PutUint16(raw[18:20], 0x3e)
		return raw
	case "macho-arm64":
		raw := make([]byte, 64)
		binary.LittleEndian.PutUint32(raw[0:4], 0xfeedfacf)
		binary.LittleEndian.PutUint32(raw[4:8], 0x0100000c)
		return raw
	case "pe-x86_64":
		raw := make([]byte, 256)
		copy(raw, []byte{'M', 'Z'})
		binary.LittleEndian.PutUint32(raw[0x3c:0x40], 0x80)
		copy(raw[0x80:0x84], []byte{'P', 'E', 0, 0})
		binary.LittleEndian.PutUint16(raw[0x84:0x86], 0x8664)
		return raw
	default:
		panic("unknown fixture binary identity: " + identity)
	}
}

func validFunctionalSmokeOutput() map[string]any {
	return map[string]any{
		"approves_work":        false,
		"blockers":             []any{},
		"contract":             "ao.mission.record.v0.1",
		"executes_work":        false,
		"generated_at_utc":     "2026-07-20T00:00:00Z",
		"mutates_repositories": false,
		"path":                 "examples/valid/mission-record.json",
		"read_only":            true,
		"schema":               "ao.mission.contract-validation.v0.1",
		"status":               "ready",
	}
}

func readCandidateSummaries(t *testing.T, root string) []any {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var summaries []any
	for _, entry := range entries {
		raw, readErr := os.ReadFile(filepath.Join(root, entry.Name(), "candidate-summary.json"))
		if readErr != nil {
			t.Fatal(readErr)
		}
		var summary map[string]any
		if err := json.Unmarshal(raw, &summary); err != nil {
			t.Fatal(err)
		}
		summaries = append(summaries, summary)
	}
	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].(map[string]any)["target_label"].(string) < summaries[j].(map[string]any)["target_label"].(string)
	})
	return summaries
}

func writePlanChecksum(t *testing.T, path string, raw []byte) {
	t.Helper()
	content := fmt.Sprintf("%s  immutable-promotion-plan.json\n", sha256Hex(raw))
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func releaseTargetFixtures(version string) []releaseTargetFixture {
	return []releaseTargetFixture{
		{
			archive: "ao-mission-" + version + "-linux-x86_64.tar.gz", architecture: "x86_64",
			binaryFormat: "elf", entryPoint: "ao-mission", goarch: "amd64", goos: "linux",
			machine: "x86_64", os: "linux", runnerArch: "X64", runnerLabel: "ubuntu-24.04",
			runnerOS: "Linux", targetLabel: "linux-x86_64",
		},
		{
			archive: "ao-mission-" + version + "-macos-aarch64.tar.gz", architecture: "aarch64",
			binaryFormat: "macho", entryPoint: "ao-mission", goarch: "arm64", goos: "darwin",
			machine: "arm64", os: "macos", runnerArch: "ARM64", runnerLabel: "macos-15",
			runnerOS: "macOS", targetLabel: "macos-aarch64",
		},
		{
			archive: "ao-mission-" + version + "-windows-x86_64.zip", architecture: "x86_64",
			binaryFormat: "pe", entryPoint: "ao-mission.exe", goarch: "amd64", goos: "windows",
			machine: "x86_64", os: "windows", runnerArch: "X64", runnerLabel: "windows-2025",
			runnerOS: "Windows", targetLabel: "windows-x86_64",
		},
	}
}

func releaseManifestFixture(sourceSHA, versionSourceDigest string) map[string]any {
	artifacts := make([]any, 0, 3)
	for _, target := range releaseTargetFixtures("0.1.0") {
		artifacts = append(artifacts, map[string]any{
			"archive":       target.archive,
			"architecture":  target.architecture,
			"binary_format": target.binaryFormat,
			"entry_point":   target.entryPoint,
			"goarch":        target.goarch,
			"goos":          target.goos,
			"machine":       target.machine,
			"os":            target.os,
			"runner_arch":   target.runnerArch,
			"runner_label":  target.runnerLabel,
			"runner_os":     target.runnerOS,
			"target_label":  target.targetLabel,
		})
	}
	return map[string]any{
		"schema_version":        "ao.release-rehearsal-manifest.v0.1",
		"repository":            "ao-mission",
		"version":               "0.1.0",
		"tag":                   "v0.1.0",
		"source_sha":            sourceSHA,
		"version_source":        "docs/sdd/AO-MISSION-V0.1.md",
		"version_source_sha256": versionSourceDigest,
		"artifacts":             artifacts,
	}
}

func marshalJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func cloneJSONMap(t *testing.T, value map[string]any) map[string]any {
	t.Helper()
	raw := marshalJSON(t, value)
	var clone map[string]any
	if err := json.Unmarshal(raw, &clone); err != nil {
		t.Fatal(err)
	}
	return clone
}

func sha256Hex(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
