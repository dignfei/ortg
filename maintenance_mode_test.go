package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
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

func TestOutlineLanguage(t *testing.T) {
	root := updateRepo(t, "README.md", "main.go")
	useTestMCPSession(t)
	call := func(name string, args map[string]any) updateMCPReply {
		t.Helper()
		return nativeMCP(t, root, map[string]any{"method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})[0]
	}
	prompt := "创建的纲要使用哪种语言？\nWhich language should the outline use?\n1. 中文\n   Chinese\n2. 英文\n   English"
	first := call("ortg_overview", map[string]any{"init": true})
	if first.Result.IsError || len(first.Result.Content) != 1 || !strings.Contains(first.Result.Content[0].Text, prompt) {
		t.Fatalf("first outline must offer the bilingual question and options: %+v", first)
	}
	if mustOpen(t, root).Config().Language != "" {
		t.Fatal("initialization must not choose a language for the user")
	}
	list := nativeMCP(t, root, map[string]any{"method": "tools/list"})[0]
	found := false
	for _, tool := range list.Result.Tools {
		if tool.Name == "ortg_overview" && !strings.Contains(tool.Description, prompt) {
			t.Fatal("initialization tool must explain the bilingual choice")
		}
		if tool.Name == "ortg_header" {
			prop, exists := tool.InputSchema.Properties["language"]
			found = exists && prop.Type == "string" && strings.Contains(prop.Description, "zh") && strings.Contains(prop.Description, "en")
		}
	}
	if !found {
		t.Fatal("MCP must expose the language parameter")
	}
	if _, err := Header(testHeader, nil, nil, nil, nil, nil, nil, "task"); err != nil {
		t.Fatal(err)
	}
	if _, err := Update("main.go", updateLine("main.go", "原有中文纲要")); err != nil {
		t.Fatal(err)
	}
	old := mustOpen(t, root)
	// A mature repository without a setting keeps its outlines and is not asked again.
	legacy := call("ortg_overview", map[string]any{})
	if legacy.Result.IsError || strings.Contains(legacy.Result.Content[0].Text, prompt) || mustOpen(t, root).Config().Language != "" {
		t.Fatalf("existing outlines must not acquire an inferred language: %+v", legacy)
	}
	assertLanguage := func(want string) {
		t.Helper()
		x := mustOpen(t, root)
		if x.Config().Language != want {
			t.Fatalf("reopened language: got %q, want %q", x.Config().Language, want)
		}
		data, err := os.ReadFile(filepath.Join(root, base.TableName))
		if err != nil {
			t.Fatal(err)
		}
		start := bytes.Index(data, []byte("\n{"))
		if start < 0 {
			t.Fatal("missing JSON configuration")
		}
		var cfg map[string]any
		if err := json.NewDecoder(bytes.NewReader(data[start+1:])).Decode(&cfg); err != nil || cfg["language"] != want {
			t.Fatalf("language must be stored in the JSON configuration: %+v, %v", cfg, err)
		}
	}
	for _, choice := range []struct{ value, notice string }{{"en", "Outline language: English (en)."}, {"zh", "Outline language: Chinese (zh)."}} {
		set := call("ortg_header", map[string]any{"language": choice.value})
		if set.Result.IsError || !strings.Contains(set.Result.Content[0].Text, choice.notice) {
			t.Fatalf("language selection: %+v", set)
		}
		assertLanguage(choice.value)
		x := mustOpen(t, root)
		if x.Header() != old.Header() || !slices.Equal(x.Rows(), old.Rows()) {
			t.Fatal("changing language must preserve existing headers, outlines and targets")
		}
		// The preceding legacy overview already delivered all entries to this session.
		for range 2 {
			out := call("ortg_overview", map[string]any{})
			if out.Result.IsError || !strings.Contains(out.Result.Content[0].Text, choice.notice) || strings.Contains(out.Result.Content[0].Text, prompt) || !strings.Contains(out.Result.Content[0].Text, "本会话都已发给你") {
				t.Fatalf("cached overview must deliver the current language without re-asking: %+v", out)
			}
		}
	}
	if set := call("ortg_header", map[string]any{"header": testHeader, "ignore_files": []string{"*.tmp"}}); set.Result.IsError {
		t.Fatalf("unrelated configuration change: %+v", set)
	}
	if err := runHeader([]string{"--mode", "task"}); err != nil {
		t.Fatal(err)
	}
	assertLanguage("zh")
	if err := runHeader([]string{"--language", "en"}); err != nil {
		t.Fatal(err)
	}
	assertLanguage("en")
	if cfg := mustOpen(t, root).Config(); cfg.Mode != "task" || !slices.Equal(cfg.IgnoreFiles, []string{"*.tmp"}) {
		t.Fatalf("language-only change must preserve other configuration: %+v", cfg)
	}
	// Validate the entire transaction before changing the table or its backup.
	table := filepath.Join(root, base.TableName)
	before, _ := os.ReadFile(table)
	backup, _ := os.ReadFile(table + ".bak")
	for _, args := range []map[string]any{
		{"language": "fr"}, {"language": ""}, {"language": "EN"}, {"language": 42}, {"language": nil},
		{"language": "zh", "header": "#invalid"}, {"language": "zh", "mode": "invalid"},
	} {
		if reply := call("ortg_header", args); !reply.Result.IsError {
			t.Fatalf("invalid language/header accepted: %+v", args)
		}
	}
	if err := runHeader([]string{"--language", ""}); err == nil {
		t.Fatal("CLI must reject an explicitly empty language")
	}
	if after, _ := os.ReadFile(table); !bytes.Equal(before, after) {
		t.Fatal("invalid language/header changed the stored table")
	}
	if after, _ := os.ReadFile(table + ".bak"); !bytes.Equal(backup, after) {
		t.Fatal("invalid language/header changed the backup")
	}
}
