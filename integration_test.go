package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestIntegrationBootstrap is a binary-level integration test. It builds the
// ortg binary, runs it in a sandbox repository containing a legacy
// overview.txt and a few source files, and verifies that `ortg scan`
// bootstraps from the legacy file, moves it aside to overview.txt.bak and
// writes nothing back to overview.txt, and that `ortg status` still reports.
func TestIntegrationBootstrap(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// Build the binary once, in a temp dir.
	binDir := t.TempDir()
	bin := filepath.Join(binDir, "ortg")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Dir = worktreeDir(t)
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build failed: %v\n%s", err, out)
	}

	// Prepare the sandbox repository.
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.go"), "package a\n")
	writeFile(t, filepath.Join(root, "b.go"), "package b\n")

	legacyHeader := testHeader
	legacy := legacyHeader + "\n\n===sandbox /" + filepath.ToSlash(root) + "/===\na.go[LST7T]: F:legacy-a | R:- | A:- | S:-\nb.go[LST5S]: F:legacy-b | R:- | A:- | S:-\n"
	writeFile(t, filepath.Join(root, "overview.txt"), legacy)

	// ---- first scan: bootstrap from legacy overview.txt ----
	out := runOrtg(t, bin, root, "scan")
	if !strings.Contains(out, "已从 overview.txt 建表") {
		t.Fatalf("first scan did not bootstrap: %s", out)
	}

	// ortg.tsv must now exist.
	if _, err := os.Stat(filepath.Join(root, "ortg.tsv")); err != nil {
		t.Fatalf("ortg.tsv missing after scan: %v", err)
	}

	// The legacy document is moved aside and nothing is exported back.
	if _, err := os.Stat(filepath.Join(root, "overview.txt.bak")); err != nil {
		t.Fatalf("legacy overview.txt not backed up: %v", err)
	}
	runOrtg(t, bin, root, "scan")
	if _, err := os.Stat(filepath.Join(root, "overview.txt")); err == nil {
		t.Fatal("scan must not write overview.txt")
	}

	// ---- status: read-only, no side effects ----
	out = runOrtg(t, bin, root, "status")
	if !strings.Contains(out, "对账报告") {
		t.Fatalf("status output missing report: %s", out)
	}
}

// worktreeDir returns the directory containing the ortg source (where go build
// should be run). In this test harness it is the directory of the test binary,
// which is the repository root.
func worktreeDir(t *testing.T) string {
	t.Helper()
	// runtime.Caller returns the file of this test; we want its directory.
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Dir(file)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runOrtg(t *testing.T, bin, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "HOME="+root)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("ortg %v failed: %v\n%s", args, err, out)
	}
	return string(out)
}
