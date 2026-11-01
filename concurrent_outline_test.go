package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestConcurrentOutlineRefresh(t *testing.T) {
	useTestMCPSession(t)
	root := updateRepo(t, "a.go", "b.go")
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	update := func(role string) {
		t.Helper()
		if _, err := Update("a.go", updateLine("a.go", role)); err != nil {
			t.Fatal(err)
		}
	}
	update("initial")
	if _, err := Update("b.go", updateLine("b.go", "unrelated")); err != nil {
		t.Fatal(err)
	}
	view := func() string {
		t.Helper()
		got, err := Overview("*", false)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	read := func() string {
		t.Helper()
		got, err := Gate("Read", filepath.Join(root, "a.go"), "", "", "")
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	view()
	callSession.ids = []string{"parallel-writer"}
	update("parallel")
	callSession.ids = []string{testMCPSession}
	if got := read(); !strings.Contains(got, "纲要在本会话取过之后被改过") {
		t.Fatalf("another context's update must request a refresh: %s", got)
	}
	if got := read(); got != "" {
		t.Fatalf("one reminder per version: %s", got)
	}
	if got := view(); !strings.Contains(got, "Role:parallel") || strings.Contains(got, "b.go[") {
		t.Fatalf("only the changed outline must be delivered: %s", got)
	}
	if got := read(); got != "" {
		t.Fatalf("a refreshed outline must be silent: %s", got)
	}
	update("self")
	if _, err := Target("a.go", "=", false, ""); err != nil {
		t.Fatal(err)
	}
	// Reload the persisted receipt, as a resumed process would.
	overviewMu.Lock()
	delete(overviewSent, root+"\x00"+testMCPSession)
	overviewMu.Unlock()
	if got := read(); got != "" {
		t.Fatalf("a context must recognize its own Update/Target after reload: %s", got)
	}
	if got := view(); !strings.Contains(got, "Role:self") || !strings.Contains(got, "#目标:") {
		t.Fatalf("authorship must not consume incremental outline/plan delivery: %s", got)
	}
	callSession.ids = []string{"parallel-writer"}
	update("parallel-again")
	callSession.ids = []string{testMCPSession}
	if _, err := Target("a.go", "=", false, ""); err != nil {
		t.Fatal(err)
	}
	if got := read(); !strings.Contains(got, "纲要在本会话取过之后被改过") {
		t.Fatalf("a local target must not acknowledge an unseen parallel outline: %s", got)
	}
	if got := view(); !strings.Contains(got, "Role:parallel-again") {
		t.Fatalf("the unseen parallel outline must remain deliverable: %s", got)
	}
}
