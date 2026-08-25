package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestGoTestCommandIsExact(t *testing.T) {
	want := []string{"go", "test", "./...", "-count=1"}
	if got := goTestCommand(); !reflect.DeepEqual(got, want) {
		t.Fatalf("go test command = %q, want %q", got, want)
	}
}

func TestSummaryCollectorBoundsPartialLinesAndRetainedSummaries(t *testing.T) {
	collector := newSummaryCollector(2)
	writer := collector.newLineWriter(64)
	_, _ = writer.Write([]byte("--- FAIL: Test" + strings.Repeat("X", 1000)))
	if len(writer.partial) > 64 {
		t.Fatalf("partial line retained %d bytes, want at most 64", len(writer.partial))
	}
	_, _ = writer.Write([]byte("\n--- FAIL: TestAlpha (0.01s)\n--- FAIL: TestBeta (0.02s)\n--- FAIL: TestGamma (0.03s)\n"))

	want := []string{"--- FAIL: TestAlpha (0.01s)", "--- FAIL: TestBeta (0.02s)"}
	if got := collector.Summaries(); !reflect.DeepEqual(got, want) {
		t.Fatalf("summaries = %#v, want %#v", got, want)
	}
	if len(collector.seen) > 2 {
		t.Fatalf("deduplication retained %d entries, want at most 2", len(collector.seen))
	}
}

func TestSummaryCollectorConcurrentWritesRemainIntactAndBounded(t *testing.T) {
	collector := newSummaryCollector(20)
	writer := collector.newLineWriter(128)
	var writers sync.WaitGroup
	for i := 0; i < 50; i++ {
		writers.Add(1)
		go func(i int) {
			defer writers.Done()
			_, _ = fmt.Fprintf(writer, "--- FAIL: TestConcurrent%02d (0.01s)\n", i)
		}(i)
	}
	writers.Wait()

	got := collector.Summaries()
	if len(got) != 20 {
		t.Fatalf("retained %d summaries, want 20", len(got))
	}
	seen := make(map[string]struct{}, len(got))
	for _, line := range got {
		if !strings.HasPrefix(line, "--- FAIL: TestConcurrent") || !strings.HasSuffix(line, " (0.01s)") {
			t.Fatalf("concurrent write produced corrupted summary %q", line)
		}
		seen[line] = struct{}{}
	}
	if len(seen) != len(got) {
		t.Fatalf("concurrent summaries contain duplicates: %#v", got)
	}
}

func TestSummaryCollectorSelectsOnlyStrictFailureLines(t *testing.T) {
	collector := newSummaryCollector(20)
	writer := collector.newLineWriter(256)
	_, _ = writer.Write([]byte(strings.Join([]string{
		"ok example.invalid/ok 0.01s",
		"diagnostic-detail: must-not-be-annotated",
		"FAIL",
		"--- FAIL: TestAlpha (0.01s)",
		"FAIL example.invalid/internal/mission 1.23s",
		"panic: test timed out after 10m0s",
		"--- FAIL: TestAlpha (0.01s)",
	}, "\n") + "\n"))

	want := []string{
		"--- FAIL: TestAlpha (0.01s)",
		"FAIL example.invalid/internal/mission 1.23s",
		"panic: test timed out after 10m0s",
	}
	if got := collector.Summaries(); !reflect.DeepEqual(got, want) {
		t.Fatalf("summaries = %#v, want %#v", got, want)
	}
}

func TestSummaryCollectorKeepsConcurrentStreamPartialsSeparate(t *testing.T) {
	collector := newSummaryCollector(20)
	stdout := collector.newLineWriter(128)
	stderr := collector.newLineWriter(128)
	_, _ = stdout.Write([]byte("--- FAIL: TestStd"))
	_, _ = stderr.Write([]byte("FAIL example.invalid/stderr 0.01s\n"))
	_, _ = stdout.Write([]byte("out (0.01s)\n"))

	want := []string{"FAIL example.invalid/stderr 0.01s", "--- FAIL: TestStdout (0.01s)"}
	if got := collector.Summaries(); !reflect.DeepEqual(got, want) {
		t.Fatalf("cross-stream summaries = %#v, want %#v", got, want)
	}
}

func TestRunCommandMapsStartFailureToFallbackAnnotation(t *testing.T) {
	var console, annotations bytes.Buffer
	code := runCommand(exec.Command("definitely-missing-ao-mission-ci-helper"), &console, &console, &annotations)
	if code != 1 {
		t.Fatalf("start failure exit = %d, want 1", code)
	}
	if got := annotations.String(); !strings.Contains(got, "go test failed without a recognized bounded summary line") {
		t.Fatalf("fallback annotation missing from %q", got)
	}
}

func TestRunCommandPreservesPositiveExitAndStreamsBothOutputs(t *testing.T) {
	var stdout, stderr, annotations bytes.Buffer
	code := runCommand(helperCommand(t, "exit-seven"), &stdout, &stderr, &annotations)
	if code != 7 {
		t.Fatalf("child exit = %d, want 7", code)
	}
	if !strings.Contains(stdout.String(), "--- FAIL: TestStdout") || !strings.Contains(stderr.String(), "FAIL example.invalid/stderr 0.01s") {
		t.Fatalf("ordinary console streams were not preserved: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	for _, want := range []string{"TestStdout", "example.invalid/stderr"} {
		if !strings.Contains(annotations.String(), want) {
			t.Fatalf("annotation %q missing %q", annotations.String(), want)
		}
	}
	if strings.Contains(annotations.String(), "must-not-be-annotated") {
		t.Fatalf("arbitrary console output leaked into annotations: %q", annotations.String())
	}
}

func TestRunCommandCollectsConcurrentStdoutAndStderrWithinBounds(t *testing.T) {
	var stdout, stderr, annotations bytes.Buffer
	code := runCommand(helperCommand(t, "concurrent"), &stdout, &stderr, &annotations)
	if code != 8 {
		t.Fatalf("child exit = %d, want 8", code)
	}
	if stdout.Len() == 0 || stderr.Len() == 0 {
		t.Fatalf("expected both console streams, stdout=%d stderr=%d", stdout.Len(), stderr.Len())
	}
	if got := strings.Count(annotations.String(), "::error "); got != maxAnnotations {
		t.Fatalf("retained %d annotations, want %d", got, maxAnnotations)
	}
}

func TestRunCommandMapsSignaledProcessToFailureWhereSupported(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose Unix signal exit status")
	}
	var annotations bytes.Buffer
	code := runCommand(exec.Command("sh", "-c", "kill -9 $$"), &bytes.Buffer{}, &bytes.Buffer{}, &annotations)
	if code != 1 {
		t.Fatalf("signaled child exit = %d, want 1", code)
	}
}

func TestSanitizeAnnotationRedactsAbsolutePathsWithoutChangingIdentifiers(t *testing.T) {
	paths := []string{
		`"C:\Users\Runner Name\work\repo\file.go:12"`,
		`"\\server\share name\repo\file.go:12"`,
		`/private/var/folders/runner/repo/file.go:12`,
		`/home/runner/work/repo/file.go:12`,
		`/workspace/repo/file.go:12`,
		`/opt/build/repo/file.go:12`,
	}
	for _, path := range paths {
		if got := sanitizeAnnotation("failure: " + path + ", 100%\r\nnext"); strings.Contains(got, path) || !strings.Contains(got, "[path]") {
			t.Fatalf("sanitized annotation %q did not redact %q", got, path)
		}
	}
	for _, identifier := range []string{"--- FAIL: TestAlpha/subcase (0.01s)", "FAIL github.com/org/repo 1.23s"} {
		if got := sanitizeAnnotation(identifier); strings.Contains(got, "[path]") {
			t.Fatalf("identifier %q was over-redacted as %q", identifier, got)
		}
	}
}

func helperCommand(t *testing.T, mode string) *exec.Cmd {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=TestCIHelperProcess", "--", mode)
	command.Env = append(os.Environ(), "AO_MISSION_CI_HELPER_PROCESS=1")
	return command
}

func TestCIHelperProcess(t *testing.T) {
	if os.Getenv("AO_MISSION_CI_HELPER_PROCESS") != "1" {
		return
	}
	switch os.Args[len(os.Args)-1] {
	case "exit-seven":
		fmt.Fprintln(os.Stdout, "--- FAIL: TestStdout (0.01s)")
		fmt.Fprintln(os.Stdout, "diagnostic-detail: must-not-be-annotated")
		fmt.Fprintln(os.Stderr, "FAIL example.invalid/stderr 0.01s")
		os.Exit(7)
	case "concurrent":
		var writers sync.WaitGroup
		for i := 0; i < 25; i++ {
			writers.Add(2)
			go func(i int) {
				defer writers.Done()
				fmt.Fprintf(os.Stdout, "--- FAIL: TestStdout%02d (0.01s)\n", i)
			}(i)
			go func(i int) {
				defer writers.Done()
				fmt.Fprintf(os.Stderr, "FAIL example.invalid/stderr%02d 0.01s\n", i)
			}(i)
		}
		writers.Wait()
		os.Exit(8)
	default:
		os.Exit(2)
	}
}
