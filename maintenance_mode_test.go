package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ortg/base"
)

func TestTaskModePartialMaintenance(t *testing.T) {
	root := updateRepo(t, "README.md", "main.go", "other.go")
	if _, err := Scan(); err != nil {
		t.Fatal(err)
	}
	if _, err := Header("", nil, nil, nil, nil, nil, nil, "task"); err != nil {
		t.Fatal(err)
	}
	if _, err := Update("main.go", updateLine("main.go", "entry")); err == nil {
		t.Fatal("task mode must still require a grounded header")
	}
	if _, err := Header(testHeader, nil, nil, nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if got, err := Update("main.go", updateLine("main.go", "entry")); err != nil || strings.Contains(got, initCompleteNote) {
		t.Fatalf("partial task update: %s %v", got, err)
	}
	x := mustOpen(t, root)
	if x.Config().Mode != "task" {
		t.Fatal("mode did not persist across reopen/header-only update")
	}
	for _, p := range []string{"README.md", "other.go"} {
		r, _ := x.Row(p)
		if r.Outline != "" || x.Observed(p, reconcileOpts) || x.Excluded(p, reconcileOpts) {
			t.Fatalf("task mode must preserve unknown cognitive rows: %+v", r)
		}
	}
	if got, err := Status(); err != nil || !strings.Contains(got, "无纲要 2") {
		t.Fatalf("partial status must remain incomplete: %s %v", got, err)
	}
	if got, err := Update("README.md", updateLine("README.md", "docs")); err != nil || strings.Contains(got, initDocsCompleteNote) {
		t.Fatalf("task completion must not announce a full-build document phase: %s %v", got, err)
	}
	if got, err := Update("other.go", updateLine("other.go", "other")); err != nil || strings.Contains(got, initCompleteNote) || strings.Contains(got, distillNote) {
		t.Fatalf("task completion must not declare initialization or distillation: %s %v", got, err)
	}
	if err := os.WriteFile(filepath.Join(root, "NEW.md"), []byte("new docs"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Scan(); err != nil {
		t.Fatal(err)
	}
	if _, err := Header("", nil, nil, nil, nil, nil, nil, "build"); err != nil {
		t.Fatal(err)
	}
	if _, err := Update("main.go", updateLine("main.go", "entry")); err == nil {
		t.Fatal("explicit build must enforce remaining documents even with source rows")
	}
	if _, err := Header("", nil, nil, nil, nil, nil, nil, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := Update("main.go", updateLine("main.go", "entry")); err != nil {
		t.Fatalf("empty mode must restore mature-repository compatibility: %v", err)
	}
	if mustOpen(t, root).Config().Mode != "" {
		t.Fatal("empty mode did not reset persistence")
	}
}

func TestTaskModeValidationAtomicAndNativeAPI(t *testing.T) {
	root := updateRepo(t, "README.md", "main.go")
	if _, err := Scan(); err != nil {
		t.Fatal(err)
	}
	if _, err := Header(testHeader, nil, nil, nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	table := filepath.Join(root, base.TableName)
	before, _ := os.ReadFile(table)
	backupBefore, _ := os.ReadFile(table + ".bak")
	for _, mode := range []string{"unknown", "TASK"} {
		if _, err := Header("", nil, nil, nil, nil, nil, nil, mode); err == nil {
			t.Fatal("invalid mode accepted")
		}
	}
	if _, err := Header("#invalid", nil, nil, nil, nil, nil, nil, "task"); err == nil {
		t.Fatal("invalid header accepted")
	}
	if after, _ := os.ReadFile(table); !bytes.Equal(before, after) {
		t.Fatal("failed mode/header validation changed stored table")
	}
	for _, mode := range []any{"unknown", 42, nil} {
		reply := nativeMCP(t, root, map[string]any{"method": "tools/call", "params": map[string]any{"name": "ortg_header", "arguments": map[string]any{"mode": mode}}})[0]
		if !reply.Result.IsError {
			t.Fatalf("native MCP accepted invalid mode: %#v", mode)
		}
	}
	if after, _ := os.ReadFile(table); !bytes.Equal(before, after) {
		t.Fatal("invalid MCP mode changed stored table")
	}
	if backupAfter, _ := os.ReadFile(table + ".bak"); !bytes.Equal(backupBefore, backupAfter) {
		t.Fatal("invalid mode/header changed backup")
	}
	if err := runHeader([]string{"--mode", "task"}); err != nil {
		t.Fatal(err)
	}
	before, _ = os.ReadFile(table)
	if _, err := Update("main.go", "main.go[7 L ST T]: Role:entry | Uses:missing.go | API:- | Constraints:-"); err == nil {
		t.Fatal("task mode bypassed Uses validation")
	}
	if after, _ := os.ReadFile(table); !bytes.Equal(before, after) {
		t.Fatal("failed source update changed table")
	}
	if text, failed := nativeUpdate(t, root, map[string]any{"items": []string{"main.go " + updateLine("main.go", "entry")}}); failed || !strings.Contains(text, "已提交 main.go") {
		t.Fatalf("native task update: %s, failed=%v", text, failed)
	}
}
