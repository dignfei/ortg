package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ortg/base"
)

func TestCodexSessionCountsDelivery(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("ORTG_AGENT", "codex")
	var smallSize int
	for _, count := range []int{1, 4096} {
		root := t.TempDir()
		t.Chdir(root)
		dir := "nested_directory_with_a_long_inventory_path"
		if err := os.Mkdir(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
		paths := make([]string, count)
		for i := range paths {
			paths[i] = fmt.Sprintf("%s/unread_%04d_with_a_long_filename.go", dir, i)
			if err := os.WriteFile(filepath.Join(root, paths[i]), []byte("package sample"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := scanWithHeader(); err != nil {
			t.Fatal(err)
		}
		session := fmt.Sprintf("counts-%d", count)
		ctx, user := HookReplyFull("session", base.HookInput{Cwd: root, Source: "startup", SessionID: session})
		var wire bytes.Buffer
		base.WriteHookOutput(&wire, "codex", "SessionStart", ctx, user)
		var envelope struct {
			Specific struct {
				Context string `json:"additionalContext"`
			} `json:"hookSpecificOutput"`
		}
		if err := json.Unmarshal(wire.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.Specific.Context != ctx || !strings.HasPrefix(ctx, rulesText()+"\n\n") ||
			!strings.Contains(ctx, sessionFetchNote+sessionReportNote) ||
			!strings.Contains(ctx, fmt.Sprintf("无纲要 %d", count)) || strings.Contains(ctx, paths[0]) {
			t.Fatalf("startup must carry all rules and counts, without enumerating paths: %d bytes", wire.Len())
		}
		if count == 1 {
			smallSize = wire.Len()
		} else if wire.Len() > smallSize+100 {
			t.Fatalf("inventory grew hook payload: small=%d, 4096=%d", smallSize, wire.Len())
		}
		if out, err := Gate("Read", filepath.Join(root, paths[0]), "", "", ""); err != nil || !strings.Contains(out, deniedMark) {
			t.Fatalf("startup counts must not mark outline entries as loaded: %q, %v", out, err)
		}
		status, err := Status()
		if err != nil {
			t.Fatal(err)
		}
		overview, err := Overview("", false)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range paths {
			if !strings.Contains(status, p) || !strings.Contains(overview, filepath.Base(p)) {
				t.Fatalf("complete tools lost %s", p)
			}
		}
		if _, prompt := HookReplyFull("prompt", base.HookInput{Cwd: root, SessionID: session}); prompt != decisionPrompt {
			t.Fatal("original decision options must appear once")
		}
		if _, prompt := HookReplyFull("prompt", base.HookInput{Cwd: root, SessionID: session}); prompt != "" {
			t.Fatal("decision options repeated")
		}
		resume, _ := HookReplyFull("session", base.HookInput{Cwd: root, Source: "resume", SessionID: session})
		if !strings.HasPrefix(resume, resumeNote) || strings.Contains(resume, paths[0]) ||
			strings.Contains(resume, sessionFetchNote) || !strings.Contains(resume, decisionText) {
			t.Fatal("pending resume must preserve context and only count the inventory")
		}
		if out, err := Gate("Read", filepath.Join(root, paths[0]), "", "", ""); err != nil || strings.Contains(out, deniedMark) {
			t.Fatalf("resume must retain the loaded outline: %q, %v", out, err)
		}
		if _, prompt := HookReplyFull("prompt", base.HookInput{Cwd: root, SessionID: session}); prompt != decisionPrompt {
			t.Fatal("resume must re-arm the existing decision options")
		}
		time.Sleep(10 * time.Millisecond)
		for _, source := range []string{"compact", "clear"} {
			reset, _ := HookReplyFull("session", base.HookInput{Cwd: root, Source: source, SessionID: session})
			if !strings.HasPrefix(reset, rulesText()+"\n\n") || strings.Contains(reset, paths[0]) {
				t.Fatal("context reset must send complete rules with counts")
			}
			if out, err := Gate("Read", filepath.Join(root, paths[0]), "", "", ""); err != nil || !strings.Contains(out, deniedMark) {
				t.Fatalf("reset must re-arm the unloaded gate: %q, %v", out, err)
			}
			if resent, err := Overview("", false); err != nil || !strings.Contains(resent, filepath.Base(paths[0])) {
				t.Fatalf("reset must re-send entries: %v", err)
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Setenv("ORTG_AGENT", "claude")
		claude, _ := HookReplyFull("session", base.HookInput{Cwd: root, Source: "startup", SessionID: "claude-" + session})
		if !strings.Contains(claude, paths[0]) || !strings.Contains(claude, paths[len(paths)-1]) {
			t.Fatal("non-Codex session report changed")
		}
		t.Setenv("ORTG_AGENT", "codex")
		if count == 1 {
			outline := filepath.Base(paths[0]) + "[3 L ST T]: Role:sample | Uses:- | API:- | Constraints:-"
			if _, err := Update(paths[0], outline); err != nil {
				t.Fatal(err)
			}
			aligned, _ := HookReplyFull("session", base.HookInput{Cwd: root, Source: "resume", SessionID: session})
			if aligned != resumeNote {
				t.Fatal("aligned resume must stay a single note")
			}
		}
	}
}
