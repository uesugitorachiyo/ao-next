package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
)

const (
	maxAnnotations = 20
	maxLineBytes   = 4096
)

var (
	testFailure    = regexp.MustCompile(`^--- FAIL: Test\S+(?: \(\d+(?:\.\d+)?s\))?$`)
	packageFailure = regexp.MustCompile(`^FAIL\s+\S+(?:\s+\d+(?:\.\d+)?s)?$`)
	timeoutFailure = regexp.MustCompile(`^panic: test timed out after \S+$`)
	quotedPath     = regexp.MustCompile(`(?i)(?:"(?:[a-z]:[\\/]|\\\\|/)[^"\r\n]+"|'(?:[a-z]:[\\/]|\\\\|/)[^'\r\n]+')`)
	windowsPath    = regexp.MustCompile(`(?i)(?:[a-z]:[\\/]|\\\\)[^,\r\n]+`)
	posixPath      = regexp.MustCompile(`(^|[\s(=])/(?:[^,\r\n]+)`)
)

type summaryCollector struct {
	mu           sync.Mutex
	maxSummaries int
	summaries    []string
	seen         map[string]struct{}
}

type summaryLineWriter struct {
	mu           sync.Mutex
	collector    *summaryCollector
	maxLineBytes int
	partial      []byte
	dropping     bool
}

func main() {
	args := goTestCommand()
	os.Exit(runCommand(exec.Command(args[0], args[1:]...), os.Stdout, os.Stderr, os.Stdout))
}

func goTestCommand() []string {
	return []string{"go", "test", "./...", "-count=1"}
}

func newSummaryCollector(summaryLimit int) *summaryCollector {
	return &summaryCollector{
		maxSummaries: summaryLimit,
		summaries:    make([]string, 0, summaryLimit),
		seen:         make(map[string]struct{}, summaryLimit),
	}
}

func (collector *summaryCollector) newLineWriter(lineLimit int) *summaryLineWriter {
	return &summaryLineWriter{collector: collector, maxLineBytes: lineLimit}
}

func (writer *summaryLineWriter) Write(data []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()

	written := len(data)
	for len(data) != 0 {
		newline := bytes.IndexByte(data, '\n')
		if newline < 0 {
			writer.appendLocked(data)
			break
		}
		writer.appendLocked(data[:newline])
		writer.finishLineLocked()
		data = data[newline+1:]
	}
	return written, nil
}

func (writer *summaryLineWriter) Finish() {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if len(writer.partial) != 0 || writer.dropping {
		writer.finishLineLocked()
	}
}

func (collector *summaryCollector) Summaries() []string {
	collector.mu.Lock()
	defer collector.mu.Unlock()
	return append([]string(nil), collector.summaries...)
}

func (writer *summaryLineWriter) appendLocked(fragment []byte) {
	if writer.dropping {
		return
	}
	remaining := writer.maxLineBytes - len(writer.partial)
	if remaining < 0 || len(fragment) > remaining {
		writer.partial = writer.partial[:0]
		writer.dropping = true
		return
	}
	writer.partial = append(writer.partial, fragment...)
}

func (writer *summaryLineWriter) finishLineLocked() {
	if !writer.dropping {
		line := strings.TrimSpace(strings.TrimSuffix(string(writer.partial), "\r"))
		writer.collector.add(line)
	}
	writer.partial = writer.partial[:0]
	writer.dropping = false
}

func (collector *summaryCollector) add(line string) {
	if !isFailureSummary(line) {
		return
	}
	collector.mu.Lock()
	defer collector.mu.Unlock()
	if len(collector.summaries) == collector.maxSummaries {
		return
	}
	if _, duplicate := collector.seen[line]; duplicate {
		return
	}
	collector.seen[line] = struct{}{}
	collector.summaries = append(collector.summaries, line)
}

func isFailureSummary(line string) bool {
	return testFailure.MatchString(line) || packageFailure.MatchString(line) || timeoutFailure.MatchString(line)
}

func runCommand(command *exec.Cmd, stdout, stderr, annotations io.Writer) int {
	collector := newSummaryCollector(maxAnnotations)
	stdoutLines := collector.newLineWriter(maxLineBytes)
	stderrLines := collector.newLineWriter(maxLineBytes)
	command.Stdout = io.MultiWriter(stdout, stdoutLines)
	command.Stderr = io.MultiWriter(stderr, stderrLines)
	err := command.Run()
	stdoutLines.Finish()
	stderrLines.Finish()
	if err == nil {
		return 0
	}

	summaries := collector.Summaries()
	if len(summaries) == 0 {
		summaries = []string{"go test failed without a recognized bounded summary line"}
	}
	// Only bounded GitHub annotations are sanitized for public diagnostics.
	// The tee'd console streams remain ordinary signed-in CI logs.
	for _, line := range summaries {
		fmt.Fprintf(annotations, "::error title=go test failure::%s\n", sanitizeAnnotation(line))
	}

	var exitError *exec.ExitError
	if errors.As(err, &exitError) && exitError.ExitCode() > 0 {
		return exitError.ExitCode()
	}
	return 1
}

func sanitizeAnnotation(value string) string {
	value = quotedPath.ReplaceAllString(value, "[path]")
	value = windowsPath.ReplaceAllString(value, "[path]")
	value = posixPath.ReplaceAllString(value, "${1}[path]")
	return strings.NewReplacer(
		"%", "%25",
		"\r", "%0D",
		"\n", "%0A",
		":", "%3A",
		",", "%2C",
	).Replace(value)
}
