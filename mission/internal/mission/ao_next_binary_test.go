package mission

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

type missionBinaryIdentity struct {
	version   string
	sourceSHA string
}

type missionBinaryReadback struct {
	Objective       string   `json:"objective"`
	Status          string   `json:"status"`
	CurrentRoute    string   `json:"current_route"`
	CurrentPhase    string   `json:"current_phase"`
	Blockers        []string `json:"blockers"`
	ExactNextAction string   `json:"exact_next_action"`
}

func TestAONextMissionAndCompatibilityCommandsBuild(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", ".."))
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}

	const (
		buildVersion   = "s1.5-test"
		buildSourceSHA = "0123456789abcdef0123456789abcdef01234567"
	)
	ldflags := strings.Join([]string{
		"-X github.com/uesugitorachiyo/ao-mission/internal/mission.BuildVersion=" + buildVersion,
		"-X github.com/uesugitorachiyo/ao-mission/internal/mission.BuildSourceSHA=" + buildSourceSHA,
	}, " ")
	buildArgs := []string{"build", "-trimpath", "-ldflags", ldflags}

	identities := map[string]missionBinaryIdentity{}
	readbacks := map[string][]missionBinaryReadback{}
	for _, command := range []string{"ao-mission", "ao-next-mission"} {
		output := filepath.Join(t.TempDir(), command+suffix)
		args := append(append([]string{}, buildArgs...), "-o", output, "./cmd/"+command)
		build := exec.Command("go", args...)
		build.Dir = root
		if combined, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v\n%s", command, err, combined)
		}

		version := exec.Command(output, "--version")
		combined, err := version.CombinedOutput()
		if err != nil {
			t.Fatalf("version %s: %v\n%s", command, err, combined)
		}
		identity := parseMissionBinaryIdentity(t, command, string(combined))
		if identity != (missionBinaryIdentity{version: buildVersion, sourceSHA: buildSourceSHA}) {
			t.Fatalf("identity %s = %+v", command, identity)
		}
		identities[command] = identity

		stateRoot := t.TempDir()
		start := runMissionBinaryJSON(t, output, "--home", stateRoot, "start", "candidate command smoke")
		missionID, ok := start["mission_id"].(string)
		if !ok || missionID == "" {
			t.Fatalf("start %s returned no mission_id: %#v", command, start)
		}
		status := runMissionBinaryJSON(t, output, "--home", stateRoot, "status", "--mission", missionID, "--json")
		readbacks[command] = []missionBinaryReadback{
			missionBinarySemantics(t, command+" start", start),
			missionBinarySemantics(t, command+" status", status),
		}
	}

	if identities["ao-mission"] != identities["ao-next-mission"] {
		t.Fatalf("binary identities differ: %#v", identities)
	}
	if !reflect.DeepEqual(readbacks["ao-mission"], readbacks["ao-next-mission"]) {
		t.Fatalf("binary readbacks differ: %#v", readbacks)
	}
}

func parseMissionBinaryIdentity(t *testing.T, command, output string) missionBinaryIdentity {
	t.Helper()
	fields := strings.Fields(output)
	if len(fields) != 3 || fields[0] != "ao-mission" {
		t.Fatalf("version %s has unexpected output %q", command, output)
	}
	version, versionOK := strings.CutPrefix(fields[1], "version=")
	sourceSHA, sourceOK := strings.CutPrefix(fields[2], "source_sha=")
	if !versionOK || !sourceOK || version == "" || sourceSHA == "" {
		t.Fatalf("version %s has unexpected output %q", command, output)
	}
	return missionBinaryIdentity{version: version, sourceSHA: sourceSHA}
}

func runMissionBinaryJSON(t *testing.T, binary string, args ...string) map[string]any {
	t.Helper()
	command := exec.Command(binary, args...)
	combined, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("run %s %s: %v\n%s", filepath.Base(binary), strings.Join(args, " "), err, combined)
	}
	var readback map[string]any
	if err := json.Unmarshal(combined, &readback); err != nil {
		t.Fatalf("decode %s %s: %v\n%s", filepath.Base(binary), strings.Join(args, " "), err, combined)
	}
	return readback
}

func missionBinarySemantics(t *testing.T, label string, readback map[string]any) missionBinaryReadback {
	t.Helper()
	body, err := json.Marshal(readback)
	if err != nil {
		t.Fatal(err)
	}
	var semantics missionBinaryReadback
	if err := json.Unmarshal(body, &semantics); err != nil {
		t.Fatalf("decode %s semantics: %v", label, err)
	}
	return semantics
}
