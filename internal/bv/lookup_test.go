package bv

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The two branches below are the whole point of lookupBV: a PATH lookup cannot
// tell "installed elsewhere" from "not installed at all", but it can tell "not
// on this PATH" from "on this PATH and not executable", and an operator who can
// read the PATH that was searched can diagnose the rest themselves.

func TestLookupBVNotOnPATH(t *testing.T) {
	pathDir := t.TempDir()
	t.Setenv("PATH", pathDir)

	resolved, err := lookupBV()
	if err == nil {
		t.Fatalf("lookupBV() = %q, want an error for an empty PATH", resolved)
	}
	if !errors.Is(err, ErrNotInstalled) {
		t.Errorf("errors.Is(err, ErrNotInstalled) = false; err = %v", err)
	}
	if !errors.Is(err, ErrNotOnPATH) {
		t.Errorf("errors.Is(err, ErrNotOnPATH) = false; err = %v", err)
	}
	if errors.Is(err, ErrNotExecutable) {
		t.Errorf("errors.Is(err, ErrNotExecutable) = true, want false; err = %v", err)
	}
	if !strings.Contains(err.Error(), pathDir) {
		t.Errorf("error does not name the PATH it searched: %v", err)
	}
	if strings.Contains(err.Error(), "bv is not installed") {
		t.Errorf("error claims bv is not installed, which the lookup cannot know: %v", err)
	}
}

func TestLookupBVFoundButNotExecutable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses the executable permission bit, so the branch is unreachable")
	}
	pathDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(pathDir, "bv"), []byte("#!/bin/sh\nexit 0\n"), 0o644); err != nil {
		t.Fatalf("write non-executable bv: %v", err)
	}
	t.Setenv("PATH", pathDir)

	resolved, err := lookupBV()
	if err == nil {
		t.Fatalf("lookupBV() = %q, want an error for a non-executable bv", resolved)
	}
	if !errors.Is(err, ErrNotInstalled) {
		t.Errorf("errors.Is(err, ErrNotInstalled) = false; err = %v", err)
	}
	if !errors.Is(err, ErrNotExecutable) {
		t.Errorf("errors.Is(err, ErrNotExecutable) = false; err = %v", err)
	}
	if errors.Is(err, ErrNotOnPATH) {
		t.Errorf("errors.Is(err, ErrNotOnPATH) = true, want false; err = %v", err)
	}
	if !strings.Contains(err.Error(), pathDir) {
		t.Errorf("error does not name the PATH it searched: %v", err)
	}
}

func TestLookupBVResolvesExecutable(t *testing.T) {
	pathDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(pathDir, "bv"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write executable bv: %v", err)
	}
	t.Setenv("PATH", pathDir)

	resolved, err := lookupBV()
	if err != nil {
		t.Fatalf("lookupBV() error = %v, want nil", err)
	}
	if want := filepath.Join(pathDir, "bv"); resolved != want {
		t.Errorf("lookupBV() = %q, want %q", resolved, want)
	}
	if !IsInstalled() {
		t.Error("IsInstalled() = false while lookupBV resolved bv")
	}
}
