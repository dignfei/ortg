package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ortg/base"
)

func TestTaskDependencyHints(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	root := t.TempDir()
	t.Chdir(root)
	for _, p := range []string{"core.go", "peer.go", "bare.go", "stale.go", "done.go", "leaf.go", "ignored.go", "observed.go", "unrelated.go", "pkg/member.go"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, p), []byte("package fixture"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	entry := func(p, module, uses string) string {
		return filepath.Base(p) + "[4 L " + module + " T]: Role:fixture | Uses:" + uses + " | API:- | Constraints:-"
	}
	for _, e := range [][2]string{
		{"done.go", entry("done.go", "CL", "leaf.go")},
		{"stale.go", entry("stale.go", "CL", "-")},
		{"core.go", entry("core.go", "ST", "bare.go, stale.go, done.go, ignored.go, observed.go, pkg/")},
		{"peer.go", entry("peer.go", "ST", "bare.go")},
	} {
		if _, err := Update(e[0], e[1]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Header(testHeader, nil, []string{"ignored.go"}, nil, nil, []string{"observed.go"}, nil, "task"); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "stale.go"), []byte("package fixture // changed"), 0o644)
	view, err := Overview("ST", false)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(view, taskDependenciesHead, 2)
	if len(parts) != 2 {
		t.Fatal("delivered core outline must expose pending direct dependencies")
	}
	hints := parts[1]
	for _, p := range []string{"bare.go", "stale.go"} {
		if strings.Count(hints, " → "+p+"（") != 1 {
			t.Fatalf("missing/deduplicated edge for %s: %s", p, hints)
		}
	}
	for _, p := range []string{"leaf.go", "unrelated.go", "ignored.go", "observed.go", "pkg/member.go"} {
		if strings.Contains(hints, p) {
			t.Fatalf("hint expanded scope to %s", p)
		}
	}
	status, err := Status()
	if err != nil || strings.Contains(status, taskDependenciesHead) {
		t.Fatalf("same-context status must not repeat hints: %v", err)
	}
	time.Sleep(10 * time.Millisecond)
	HookReply("session", base.HookInput{Cwd: root, Source: "compact", SessionID: "dependency-fixture"})
	if view, err = Overview("ST", false); err != nil || !strings.Contains(view, taskDependenciesHead) {
		t.Fatalf("context reset must permit hints again: %v", err)
	}
	if _, err := Update("bare.go", entry("bare.go", "CL", "-")); err != nil {
		t.Fatal(err)
	}
	if _, err := Update("stale.go", entry("stale.go", "CL", "-")); err != nil {
		t.Fatal(err)
	}
	if status, err = Status(); err != nil || strings.Contains(status, taskDependenciesHead) {
		t.Fatalf("resolved dependencies still hinted: %v", err)
	}
	// More than five Uses remains legal. A large frontier is bounded and
	// reported as unchecked candidates, rather than implicitly complete.
	var uses []string
	for i := 0; i < 40; i++ {
		p := fmt.Sprintf("pending_%02d.go", i)
		os.WriteFile(filepath.Join(root, p), []byte("package fixture"), 0o644)
		uses = append(uses, p)
	}
	if _, err := Scan(); err != nil {
		t.Fatal(err)
	}
	if _, err := Update("core.go", entry("core.go", "ST", strings.Join(uses, ","))); err != nil {
		t.Fatal(err)
	}
	view, err = Overview("ST", false)
	if err != nil {
		t.Fatal(err)
	}
	hints = strings.SplitN(view, taskDependenciesHead, 2)[1]
	if strings.Count(hints, " → ") != 25 || !strings.Contains(hints, fmt.Sprintf(taskDependenciesMore, 15)) {
		t.Fatalf("frontier must remain bounded: %s", hints)
	}
	status, err = Status()
	if err != nil || strings.Count(strings.SplitN(status, taskDependenciesHead, 2)[1], " → ") != 15 {
		t.Fatalf("pending dependency26 must not starve: %v", err)
	}
	if status, err = Status(); err != nil || strings.Contains(status, taskDependenciesHead) {
		t.Fatalf("drained frontier must not repeat: %v", err)
	}
	if _, err := Header(testHeader, nil, nil, nil, nil, []string{"observed.go", uses[39]}, nil, "task"); err != nil {
		t.Fatal(err)
	}
	Status() // the observed dependency leaves the live pending namespace
	if _, err := Header(testHeader, nil, nil, nil, nil, []string{"observed.go"}, nil, "task"); err != nil {
		t.Fatal(err)
	}
	if status, err = Status(); err != nil || !strings.Contains(status, " → "+uses[39]+"（") {
		t.Fatalf("returning cognitive dependency must be hinted again: %v", err)
	}
	// Aligned dependencies that sort first cannot consume the display limit.
	var aligned []string
	for i := 0; i < 25; i++ {
		p := fmt.Sprintf("aa_aligned_%02d.go", i)
		os.WriteFile(filepath.Join(root, p), []byte("package fixture"), 0o644)
		aligned = append(aligned, p)
	}
	Scan()
	for _, p := range aligned {
		if _, err := Update(p, entry(p, "CL", "-")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Update("core.go", entry("core.go", "ST", "-")); err != nil {
		t.Fatal(err)
	}
	Overview("ST", false) // removing the edges also clears only their hint marks
	if _, err := Update("core.go", entry("core.go", "ST", strings.Join(append(aligned, uses[39]), ","))); err != nil {
		t.Fatal(err)
	}
	view, err = Overview("ST", false)
	if err != nil || !strings.Contains(view, " → "+uses[39]+"（") {
		t.Fatalf("25 aligned prefixes starved the real pending edge: %v", err)
	}
	if _, err := Header(testHeader, nil, nil, nil, nil, nil, nil, "build"); err != nil {
		t.Fatal(err)
	}
	if view, err = Overview("ST", true); err != nil || strings.Contains(view, taskDependenciesHead) {
		t.Fatalf("explicit build contract changed: %v", err)
	}
	// A filename delivered as a skeleton is not the later outline written
	// by another actor. Only receiving that real version makes it a root.
	if _, err := Header(testHeader, nil, nil, nil, nil, nil, nil, "task"); err != nil {
		t.Fatal(err)
	}
	if _, err := Overview("*", true); err != nil {
		t.Fatal(err)
	}
	if _, err := Update("leaf.go", entry("leaf.go", "CL", "unrelated.go")); err != nil {
		t.Fatal(err)
	}
	status, err = Status()
	if err != nil || strings.Contains(status, "leaf.go → unrelated.go") {
		t.Fatalf("a bare filename became a root without outline delivery: %v", err)
	}
	view, err = Overview("CL", false)
	if err != nil || !strings.Contains(view, "leaf.go → unrelated.go") {
		t.Fatalf("receiving the actual outline must enable its one-hop hints: %v", err)
	}
}
