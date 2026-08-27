//go:build darwin || linux

package mission

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestCorrelationChainRejectsFIFOWithoutReading(t *testing.T) {
	path := filepath.Join(t.TempDir(), "artifact.fifo")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildCorrelationChain(correlationTestRecord(), []CorrelationArtifactSpec{{
		Role: "atlas-workgraph",
		Path: path,
	}}); err == nil {
		t.Fatal("FIFO artifact accepted")
	}
}

func TestCorrelationChainRacedFIFOOpenDoesNotBlock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "artifact.json")
	if err := os.WriteFile(path, []byte(`{"schema":"ao.atlas.workgraph.v1"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, _, err := readCanonicalCorrelationArtifactWithOpen(path, func(path string) (*os.File, error) {
			if err := os.Remove(path); err != nil {
				return nil, err
			}
			if err := syscall.Mkfifo(path, 0o600); err != nil {
				return nil, err
			}
			return openCorrelationInput(path)
		})
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("raced FIFO artifact was accepted")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("opening a raced FIFO blocked")
	}
}

func TestCorrelationChainRejectsPathReplacementAfterOpen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "artifact.json")
	replacement := filepath.Join(dir, "replacement.json")
	if err := os.WriteFile(path, []byte(`{"schema":"ao.atlas.original.v1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(replacement, []byte(`{"schema":"ao.atlas.replacement.v1"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	_, _, err := readCanonicalCorrelationArtifactWithOpen(path, func(path string) (*os.File, error) {
		file, err := openCorrelationInput(path)
		if err != nil {
			return nil, err
		}
		if err := os.Rename(replacement, path); err != nil {
			file.Close()
			return nil, err
		}
		return file, nil
	})
	if err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("input path replacement was accepted: %v", err)
	}
}
