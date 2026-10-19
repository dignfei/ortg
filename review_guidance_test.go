package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"ortg/base"
)

// Every call uses a new production MCP dispatcher with the same session.
// Switching mode selects genuinely different built-in review guidance;
// deleting only the new field exercises the pre-fingerprint cache format.
func TestReviewGuidanceCache(t *testing.T) {
	for _, transition := range []string{"mode", "legacy"} {
		t.Run(transition, func(t *testing.T) {
			t.Setenv("ORTG_AGENT", "codex")
			useTestMCPSession(t)
			root := updateRepo(t, "a.go", "b.go", "consumer.go")
			if _, err := scanWithHeader(); err != nil {
				t.Fatal(err)
			}
			call := func(name string, args map[string]any) string {
				t.Helper()
				r := nativeMCP(t, root, map[string]any{"method": "tools/call", "params": map[string]any{
					"name": name, "arguments": args, "_meta": map[string]any{"threadId": testMCPSession},
				}})[0]
				if r.Result.IsError || len(r.Result.Content) != 1 {
					t.Fatalf("%s: %+v", name, r)
				}
				return r.Result.Content[0].Text
			}
			mode := "task"
			if transition == "mode" {
				mode = "build"
			}
			decision := "#【契约】stable：Neutral fixture contract；【裁决】保持"
			call("ortg_header", map[string]any{"mode": mode, "header": testHeader + "\n" + decision})
			for _, p := range []string{"a.go", "b.go", "consumer.go"} {
				line := updateLine(p, "fixture")
				if p == "a.go" {
					line += " | Keys:stable(执行)"
				}
				if p == "consumer.go" {
					line = strings.Replace(line, "Uses:-", "Uses:a.go", 1)
				}
				call("ortg_update", map[string]any{"path": p, "outline": line})
			}
			call("ortg_overview", nil)
			collectRules(t, call)
			call("ortg_target", map[string]any{"items": []string{
				"a.go " + updateLine("a.go", "updated fixture") + " | Keys:stable(执行)",
				"b.go " + updateLine("b.go", "updated fixture"),
			}})
			first := call("ortg_review", nil)
			if !strings.Contains(first, reviewChecklistFor(mode)) || strings.Contains(first, "已收敛") {
				t.Fatal("fixture must leave a review open", first)
			}
			call("ortg_overview", nil) // Cache the unchanged source rows and targets.
			before := loadSent(root, testMCPSession)
			mark, _ := ruleIdentity(root, testMCPSession, "", before.born)
			_, receipt := base.OverviewMark(root, mark)
			if transition == "mode" {
				call("ortg_header", map[string]any{"mode": "task"})
			} else {
				_, wire := base.OverviewMark(root, sentMark(testMCPSession))
				var legacy map[string]any
				if err := json.Unmarshal([]byte(wire), &legacy); err != nil {
					t.Fatal(err)
				}
				delete(legacy, "ReviewGuidance")
				data, err := json.Marshal(legacy)
				if err != nil {
					t.Fatal(err)
				}
				base.MarkOverview(root, sentMark(testMCPSession), string(data))
			}
			fresh := call("ortg_review", map[string]any{"rulings": []string{"stable：保持"}})
			if !strings.Contains(fresh, reviewTaskChecklist) || !strings.Contains(fresh, reviewConflict) || strings.Contains(fresh, "已收敛") {
				t.Fatal("changed or legacy guidance must be sent and reviewed again", fresh)
			}
			if strings.Contains(fresh, "目标: ") || !strings.Contains(fresh, "正文与参与者见本计划上次核对") {
				t.Fatal("unchanged targets and contract quotations must stay incremental", fresh)
			}
			after := loadSent(root, testMCPSession)
			if !reflect.DeepEqual(before.rows, after.rows) || !reflect.DeepEqual(before.plan.Quoted, after.plan.Quoted) || !reflect.DeepEqual(before.plan.Affected, after.plan.Affected) {
				t.Fatal("guidance invalidation discarded unrelated delivery state")
			}
			second := call("ortg_review", map[string]any{"rulings": []string{"stable：保持"}})
			if !strings.Contains(second, "已收敛") || strings.Contains(second, "逐项核对") || strings.Contains(second, reviewConflict) {
				t.Fatal("the same guidance must retain the incremental second review", second)
			}
			out := call("ortg_overview", nil)
			if strings.Contains(out, "[7 L ST T]") || strings.Contains(out, rulesNeeded) {
				t.Fatal("guidance change resent source rows or invalidated the rule ACK", out)
			}
			_, gotReceipt := base.OverviewMark(root, mark)
			if receipt == "" || receipt != gotReceipt {
				t.Fatal("unchanged rules receipt was altered")
			}
			wire, err := os.ReadFile(filepath.Join(root, "ortg.tsv"))
			if err != nil || !strings.Contains(string(wire), decision) {
				t.Fatal("the unchanged contract decision was lost", err)
			}
		})
	}
}

func TestReviewGuidanceGate(t *testing.T) {
	t.Setenv("ORTG_AGENT", "codex")
	useTestMCPSession(t)
	root := updateRepo(t, "a.go", "b.go", "consumer.go")
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	must := func(_ string, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{"a.go", "b.go", "consumer.go"} {
		line := updateLine(p, "fixture")
		if p == "consumer.go" {
			line = strings.Replace(line, "Uses:-", "Uses:a.go", 1)
		}
		if _, err := Update(p, line); err != nil {
			t.Fatal(err)
		}
	}
	mode := "build"
	if _, err := Header("", nil, nil, nil, nil, nil, nil, mode); err != nil {
		t.Fatal(err)
	}
	must(Overview("", false))
	finishRules(t)
	must(TargetItems([]string{"a.go " + updateLine("a.go", "updated fixture"), "b.go ="}))
	must(Review())
	must(Review())
	gate := func(p string) string {
		t.Helper()
		out, err := Gate("Edit", filepath.Join(root, p), "", "", "")
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if out := gate("a.go"); out != "" {
		t.Fatal("converged plan must permit writes", out)
	}
	mode = "task"
	must(Header("", nil, nil, nil, nil, nil, nil, mode))
	if out := gate("a.go"); !strings.Contains(out, deniedMark) {
		t.Fatal("old guidance must not retain current target permission", out)
	}
	must(Review())
	if out := gate("a.go"); !strings.Contains(out, deniedMark) {
		t.Fatal("a fresh nonconverged review must not retain old permission", out)
	}
	must(Review())
	if out := gate("a.go") + gate("b.go"); out != "" {
		t.Fatal("newly converged guidance must permit writes", out)
	}
	must(Update("a.go", updateLine("a.go", "updated fixture")))
	if out := gate("b.go"); out != "" {
		t.Fatal("submitting one outline must preserve the remaining reviewed targets", out)
	}
	must(Update("b.go", updateLine("b.go", "fixture")))
	mode = "build"
	must(Header("", nil, nil, nil, nil, nil, nil, mode))
	if out := gate("a.go"); out != "" {
		t.Fatal("a finished file with no target must retain its permission", out)
	}
	must(Target("consumer.go", "=", false, ""))
	must(Review())
	must(Update("consumer.go", strings.Replace(updateLine("consumer.go", "fixture"), "Uses:-", "Uses:a.go", 1)))
	must(Target("a.go", "=", false, ""))
	if out := gate("a.go"); !strings.Contains(out, deniedMark) {
		t.Fatal("another plan must not refresh an old target's guidance permission", out)
	}
}
