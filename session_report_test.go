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
	t.Cleanup(func() { useCallSession(nil) })
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
		useCallSession(base.Args{"_meta": map[string]any{"threadId": session}})
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
		if wire.Len() > 4096 || envelope.Specific.Context != ctx || !strings.HasPrefix(ctx, rulesNeeded+"\n\n") ||
			!strings.Contains(ctx, sessionFetchNote+sessionReportNote) ||
			!strings.Contains(ctx, fmt.Sprintf("无纲要 %d", count)) || strings.Contains(ctx, paths[0]) {
			t.Fatalf("startup must carry a bounded rule notice and counts, without enumerating paths: %d bytes", wire.Len())
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
		finishRules(t)
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
			if !strings.HasPrefix(reset, rulesNeeded+"\n\n") || strings.Contains(reset, paths[0]) {
				t.Fatal("context reset must send a rule notice with counts")
			}
			if out, err := Gate("Read", filepath.Join(root, paths[0]), "", "", ""); err != nil || !strings.Contains(out, deniedMark) {
				t.Fatalf("reset must re-arm the unloaded gate: %q, %v", out, err)
			}
			if resent, err := Overview("", false); err != nil || !strings.Contains(resent, filepath.Base(paths[0])) {
				t.Fatalf("reset must re-send entries: %v", err)
			}
			finishRules(t)
			time.Sleep(10 * time.Millisecond)
		}
		t.Setenv("ORTG_AGENT", "claude")
		claude, _ := HookReplyFull("session", base.HookInput{Cwd: root, Source: "startup", SessionID: "claude-" + session})
		if strings.Contains(claude, paths[0]) || strings.Contains(claude, paths[len(paths)-1]) || !strings.Contains(claude, rulesNeeded) || len(claude) > 2048 {
			t.Fatal("Claude hook must keep its complete inventory in Status")
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

func TestOverviewCountsKeepCompleteStatus(t *testing.T) {
	defer func(c, x hostLimits) { claudeLimits, codexLimits = c, x }(claudeLimits, codexLimits)
	claudeLimits.whole, codexLimits.whole = 10, 10
	for _, host := range []string{"claude", "codex"} {
		t.Run(host, func(t *testing.T) {
			t.Setenv("ORTG_AGENT", host)
			smallSizes := map[string]int{}
			for _, count := range []int{1, 1024} {
				t.Run(fmt.Sprint(count), func(t *testing.T) {
					t.Setenv("TMPDIR", t.TempDir())
					root := t.TempDir()
					t.Chdir(root)
					dir := "unrelated_long_inventory_directory"
					if err := os.Mkdir(dir, 0o755); err != nil {
						t.Fatal(err)
					}
					paths := make([]string, count)
					for i := range paths {
						paths[i] = fmt.Sprintf("%s/unread_%04d_long_filename.go", dir, i)
						if err := os.WriteFile(paths[i], []byte("package sample"), 0o644); err != nil {
							t.Fatal(err)
						}
					}
					if err := os.WriteFile("owned.go", []byte("package sample"), 0o644); err != nil {
						t.Fatal(err)
					}
					if _, err := scanWithHeader(); err != nil {
						t.Fatal(err)
					}
					if _, err := Header(testHeader, nil, nil, nil, nil, nil, nil, "task"); err != nil {
						t.Fatal(err)
					}
					outline := "owned.go[5 L CL T]: Role:owned | Uses:- | API:- | Constraints:-"
					if _, err := Update("owned.go", outline); err != nil {
						t.Fatal(err)
					}
					get := func(branch, module string) string {
						t.Helper()
						out, err := Overview(module, false)
						if err != nil {
							t.Fatal(err)
						}
						_, report, ok := strings.Cut(out, "## 对账报告\n")
						if !ok || !strings.Contains(report, fmt.Sprintf("无纲要 %d", count)) ||
							!strings.Contains(report, "全库计数") || !strings.Contains(report, "ortg_status") ||
							strings.Contains(out, "unread_") || strings.Contains(out, dir) {
							t.Fatalf("%s must give global counts without unrelated paths:\n%s", branch, out)
						}
						if count == 1 {
							smallSizes[branch] = len(report)
						} else if baseline, ok := smallSizes[branch]; ok && len(report) > baseline+50 {
							t.Fatalf("%s report grew with the inventory: %d -> %d", branch, baseline, len(report))
						}
						return out
					}
					if out := get("over budget", ""); !strings.HasSuffix(out, overviewModulesFooter) || strings.Contains(out, outline) {
						t.Fatal("over-budget call must retain the header/module-list behavior")
					}
					if out := get("module", "CL"); !strings.Contains(out, outline) {
						t.Fatal("module call lost its real outline")
					}
					if out := get("short", "CL"); !strings.HasSuffix(out, overviewShortFooter) || strings.Contains(out, outline) {
						t.Fatal("repeat must retain the short reply")
					}
					status, err := Status()
					if err != nil {
						t.Fatal(err)
					}
					unknown, err := Overview("-", false)
					if err != nil {
						t.Fatal(err)
					}
					for _, p := range paths {
						if !strings.Contains(status, p) || !strings.Contains(unknown, filepath.Base(p)) {
							t.Fatalf("Status or later outline delivery lost %s", p)
						}
					}
					x, err := base.Open(root)
					if err != nil || len(x.Rows()) != count+1 {
						t.Fatalf("inventory changed: %v", err)
					}
					for _, r := range x.Rows() {
						if r.Ignored || r.Deleted || (r.Path == "owned.go" && r.Outline != outline) || (r.Path != "owned.go" && r.Outline != "") {
							t.Fatalf("inventory or true outline changed: %+v", r)
						}
					}
				})
			}
		})
	}
}

func TestOverviewWholeReplyBudget(t *testing.T) {
	for _, host := range []string{"claude", "codex"} {
		for _, budget := range []string{"window", "cap"} {
			t.Run(host+"/"+budget, func(t *testing.T) {
				t.Setenv("ORTG_AGENT", host)
				t.Setenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW", "200000")
				codexHome := t.TempDir()
				t.Setenv("CODEX_HOME", codexHome)
				config := filepath.Join(codexHome, "config.toml")
				writeFile(t, config, "model_context_window = 200000\n")
				root := updateRepo(t, "owned.go", "unread.go", "CONTRIBUTING.md")
				if _, err := scanWithHeader(); err != nil {
					t.Fatal(err)
				}
				if _, err := Header(testHeader, nil, nil, nil, nil, nil, nil, "task"); err != nil {
					t.Fatal(err)
				}
				outline := "owned.go[5 L CL T]: Role:owned | Uses:unread.go | API:- | Constraints:-"
				if _, err := Update("owned.go", outline); err != nil {
					t.Fatal(err)
				}
				before := mustOpen(t, root).Rows()
				t.Cleanup(func() { useCallSession(nil) })
				get := func(module string, force bool, sid string) string {
					t.Helper()
					useCallSession(base.Args{"_meta": map[string]any{"threadId": sid}})
					out, err := Overview(module, force)
					if err != nil {
						t.Fatal(err)
					}
					return out
				}
				full := get("", false, "budget-probe")
				if !strings.Contains(full, taskDependenciesHead) || !strings.Contains(full, policyCandidatesHead) {
					t.Fatal("probe must include dependency hints and policy candidates")
				}
				originalC, originalX := claudeLimits, codexLimits
				t.Cleanup(func() { claudeLimits, codexLimits = originalC, originalX })
				if budget == "window" {
					window := 2 * (estTokens(full) - 1)
					t.Setenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW", fmt.Sprint(window))
					writeFile(t, config, fmt.Sprintf("model_context_window = %d\n", window))
				} else {
					claudeLimits.whole, codexLimits.whole = outlineSize(full)-1, outlineSize(full)-1
				}
				for _, force := range []bool{false, true} {
					sid := fmt.Sprintf("budget-%v", force)
					out := get("", force, sid)
					if !strings.HasSuffix(out, overviewModulesFooter) || strings.Contains(out, outline) {
						t.Fatalf("complete reply exceeds %s budget but was sent whole: %d tokens, %d units", budget, estTokens(out), outlineSize(out))
					}
					if c := sentFor(root); len(c.rows) != 0 {
						t.Fatal("module list marked unsent outline rows as delivered")
					}
					out = get("CL", false, sid)
					if !strings.Contains(out, outline) || !strings.Contains(out, taskDependenciesHead) {
						t.Fatal("budget preview consumed later outline or dependency delivery")
					}
					out = get("", false, sid)
					if !strings.HasSuffix(out, fmt.Sprintf(overviewPartFooter, "整份")) || strings.Contains(out, outline) ||
						!strings.Contains(out, "CONTRIBUTING.md[") || !strings.Contains(out, fmt.Sprintf(overviewTaskInventoryNote, 1)) ||
						!strings.Contains(out, fmt.Sprintf(overviewIncrementNote, 1, 1)) {
						t.Fatal("fitting task delta must keep all real outlines and disclose omitted bare inventory")
					}
					if _, sent := sentFor(root).rows["unread.go"]; sent {
						t.Fatal("omitted inventory was marked delivered")
					}
					if out = get("", false, sid); !strings.HasSuffix(out, overviewShortFooter) {
						t.Fatal("all delivered rows must retain the short reply")
					}
					if out = get("*", true, sid); !strings.Contains(out, outline) || !strings.Contains(out, "unread.go") {
						t.Fatal("explicit whole force must bypass bare-call budgets")
					}
				}
				x := mustOpen(t, root)
				if len(x.Rows()) != 3 {
					t.Fatal("budget selection changed the inventory")
				}
				for i, r := range x.Rows() {
					if r != before[i] {
						t.Fatalf("budget selection changed known or unknown inventory: %+v", r)
					}
				}
			})
		}
	}
}

func TestTaskOverviewOmitsBareInventory(t *testing.T) {
	for _, host := range []string{"claude", "codex"} {
		t.Run(host, func(t *testing.T) {
			t.Setenv("ORTG_AGENT", host)
			t.Setenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW", "200000")
			codexHome := t.TempDir()
			t.Setenv("CODEX_HOME", codexHome)
			writeFile(t, filepath.Join(codexHome, "config.toml"), "model_context_window = 200000\n")
			root := updateRepo(t, "owned.go", "other.go", "target.go", "delete.go", "ignored.go", "vendor/lib/x.go")
			paths := make([]string, 1024)
			for i := range paths {
				paths[i] = fmt.Sprintf("unknown_inventory/unread_%04d_with_a_long_filename.go", i)
				writeFile(t, filepath.Join(root, paths[i]), "package sample")
			}
			if _, err := scanWithHeader(); err != nil {
				t.Fatal(err)
			}
			if _, err := Header(testHeader, nil, []string{"ignored.go"}, nil, []string{"vendor"}, nil, nil, "task"); err != nil {
				t.Fatal(err)
			}
			known := []string{updateLine("owned.go", "owned"), updateLine("other.go", "other"), "vendor/[3 L ST T]: Role:library | Uses:- | API:- | Constraints:-"}
			for i, p := range []string{"owned.go", "other.go", "vendor"} {
				if _, err := Update(p, known[i]); err != nil {
					t.Fatal(err)
				}
			}
			plans := []string{updateLine("target.go", "planned change"), updateLine("future.go", "planned creation")}
			for i, p := range []string{"target.go", "future.go"} {
				if _, err := Target(p, plans[i], false, ""); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := Target("delete.go", "", true, ""); err != nil {
				t.Fatal(err)
			}
			if _, err := Scan(); err != nil {
				t.Fatal(err)
			}
			before := mustOpen(t, root).Rows()
			sid := "task-default-" + host
			useCallSession(base.Args{"_meta": map[string]any{"threadId": sid}})
			t.Cleanup(func() { useCallSession(nil) })
			get := func(module string, force bool) string {
				t.Helper()
				out, err := Overview(module, force)
				if err != nil {
					t.Fatal(err)
				}
				return out
			}
			checkDefault := func(out string) {
				t.Helper()
				for _, line := range append(append([]string{}, known...), plans...) {
					if !strings.Contains(out, line) {
						t.Fatal("default omitted real knowledge or a target", line)
					}
				}
				if !strings.Contains(out, "delete.go\n"+prefixTargetDelete) || !strings.Contains(out, prefixPlanned+"future.go") ||
					!strings.Contains(out, fmt.Sprintf(overviewTaskInventoryNote, len(paths))) || strings.Contains(out, "unread_") ||
					!strings.HasSuffix(out, fmt.Sprintf(overviewPartFooter, "整份")) {
					t.Fatalf("task default lost plans, scope disclosure or whole reply: %.1000s", out)
				}
				for _, p := range paths {
					if _, sent := sentFor(root).rows[p]; sent {
						t.Fatal("omitted bare row consumed its Sent entry", p)
					}
				}
			}
			full := get("", false)
			checkDefault(full)
			originalC, originalX := claudeLimits, codexLimits
			t.Cleanup(func() { claudeLimits, codexLimits = originalC, originalX })
			// The selected known/plan reply fits; the bare inventory alone would exceed it.
			claudeLimits.whole, codexLimits.whole = outlineSize(full)+512, outlineSize(full)+512
			checkDefault(get("", true))
			if out := get("", false); !strings.HasSuffix(out, overviewShortFooter) || strings.Contains(out, known[0]) || strings.Contains(out, "unread_") {
				t.Fatal("unchanged task default must be a short reply")
			}
			for _, module := range []string{"-", "*"} {
				out := get(module, module == "*")
				for _, p := range paths {
					if !strings.Contains(out, filepath.Base(p)) {
						t.Fatal("explicit inventory query lost a bare row", module, p)
					}
				}
				if outlineSize(out) <= limits().whole {
					t.Fatal("fixture must put explicit inventory beyond the default reply budget")
				}
			}
			status, err := Status()
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range paths {
				if !strings.Contains(status, p) {
					t.Fatal("Status lost complete inventory", p)
				}
			}
			for _, source := range []string{"compact", "clear"} {
				time.Sleep(10 * time.Millisecond)
				HookReply("session", base.HookInput{Cwd: root, SessionID: sid, Source: source})
				checkDefault(get("", false))
				if out := get("-", false); !strings.Contains(out, filepath.Base(paths[len(paths)-1])) {
					t.Fatal("context reset consumed omitted bare inventory")
				}
			}
			x := mustOpen(t, root)
			if len(x.Rows()) != len(before) || !x.Observed("vendor/lib/x.go", reconcileOpts) || !x.Excluded("ignored.go", reconcileOpts) {
				t.Fatal("overview selection changed inventory or roles")
			}
			for i, r := range x.Rows() {
				if r != before[i] {
					t.Fatal("overview selection changed a stored row", r.Path)
				}
			}
			// Newly known or planned rows must enter the next default delta.
			line := updateLine(paths[0], "new knowledge")
			if _, err := Update(paths[0], line); err != nil {
				t.Fatal(err)
			}
			target := updateLine(paths[1], "new plan")
			if _, err := Target(paths[1], target, false, ""); err != nil {
				t.Fatal(err)
			}
			if _, err := Target(paths[2], "", true, ""); err != nil {
				t.Fatal(err)
			}
			out := get("", false)
			if !strings.Contains(out, line) || !strings.Contains(out, target) || !strings.Contains(out, filepath.Base(paths[2])+"\n"+prefixTargetDelete) ||
				strings.Contains(out, known[0]) || strings.Contains(out, filepath.Base(paths[3])) {
				t.Fatal("knowledge/plan transitions lost their incremental delivery")
			}
			if out := get("", false); !strings.HasSuffix(out, overviewShortFooter) {
				t.Fatal("newly delivered knowledge/plans did not enter Sent")
			}
			claudeLimits.whole, codexLimits.whole = 10, 10
			if out := get("", true); !strings.HasSuffix(out, overviewModulesFooter) || !strings.Contains(out, fmt.Sprintf("- -  %d 个文件", len(paths)+2)) || strings.Contains(out, known[0]) {
				t.Fatal("real knowledge over budget must retain complete module discovery")
			}
			for _, mode := range []string{"build", ""} {
				if _, err := Header("", nil, nil, nil, nil, nil, nil, mode); err != nil {
					t.Fatal(err)
				}
				claudeLimits.whole, codexLimits.whole = originalC.whole, originalX.whole
				if out := get("", true); !strings.Contains(out, filepath.Base(paths[3])) || strings.Contains(out, "默认正文未逐条发送") {
					t.Fatal("build/compatibility default inventory semantics changed", mode)
				}
			}
		})
	}
}
