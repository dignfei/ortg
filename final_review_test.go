package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"ortg/base"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

func finalFixture(t *testing.T) string {
	t.Helper()
	useTestMCPSession(t)
	root := updateRepo(t, "value.go", "peer.go")
	writeFile(t, filepath.Join(root, "value.go"), "package sample\nconst Value = 1\n")
	if _, err := Scan(); err != nil {
		t.Fatal(err)
	}
	if _, err := Header(testHeader, nil, nil, nil, nil, nil, nil, "task"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"value.go", "peer.go"} {
		if _, err := Update(p, updateLine(p, "neutral source")); err != nil {
			t.Fatal(err)
		}
	}
	return root
}
func finalSaved(t *testing.T, root, sid string) finalReviewState {
	t.Helper()
	var f finalReviewState
	if err := withWriteProofs(root, func(s *writeProofs) (bool, error) { f = s.Final[sid]; return false, nil }); err != nil {
		t.Fatal(err)
	}
	return f
}
func finalWrite(t *testing.T, root, sid, agent, use, path, body string) base.HookInput {
	t.Helper()
	in := base.HookInput{Cwd: root, SessionID: sid, AgentID: agent, ToolUseID: use, TurnID: "neutral-turn", ToolName: "Edit", FilePath: filepath.Join(root, path)}
	HookReply("pre", in)
	if body == "" {
		if err := os.Remove(in.FilePath); err != nil {
			t.Fatal(err)
		}
	} else {
		writeFile(t, in.FilePath, body)
	}
	HookReply("post", in)
	return in
}
func finalDeclare(t *testing.T, root string) string {
	t.Helper()
	if _, err := finalReview("", nil); err != nil {
		t.Fatal(err)
	}
	g := finalGeneration(finalSaved(t, root, testMCPSession))
	out, err := finalReview(g, []string{"已按公开任务静态检查当前持久源码；运行行为未验证。"})
	if err != nil || !strings.Contains(out, finalReviewRecorded) {
		t.Fatalf("declaration: %s %v", out, err)
	}
	return g
}
func TestFinalReviewGenerations(t *testing.T) {
	root := finalFixture(t)
	if f := finalSaved(t, root, testMCPSession); len(f.Files) != 0 {
		t.Fatal("reading/outlining source created final obligation")
	}
	// A reviewed pre-edit plan cannot settle final source review.
	if _, err := TargetItems([]string{"value.go =", "peer.go ="}); err != nil {
		t.Fatal(err)
	}
	if _, err := Review(); err != nil {
		t.Fatal(err)
	}
	unchanged := finalWrite(t, root, testMCPSession, "", "no-op", "value.go", "package sample\nconst Value = 1\n")
	if f := finalSaved(t, root, testMCPSession); len(f.Files) != 0 {
		t.Fatal("no-op armed final review")
	}
	in := unchanged
	in.ToolUseID = "outer-update"
	HookReply("pre", in)
	writeFile(t, in.FilePath, "package sample\nconst Value = 2\n")
	if _, err := Update("value.go", updateLine("value.go", "neutral source")); err != nil {
		t.Fatal(err)
	}
	HookReply("post", in)
	first := finalSaved(t, root, testMCPSession)
	g := finalGeneration(first)
	if len(first.Files) != 1 || g == "" || first.Reviewed == g {
		t.Fatalf("Update-before-post lost final obligation: %+v", first)
	}
	HookReply("post", in)
	if finalGeneration(finalSaved(t, root, testMCPSession)) != g {
		t.Fatal("duplicate post advanced generation")
	}
	if _, err := Review(); err != nil {
		t.Fatal(err)
	}
	if finalSaved(t, root, testMCPSession).Reviewed == g {
		t.Fatal("default Review settled final stage")
	}
	if _, err := Align("all", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := finalReview(g, []string{"  "}); err == nil {
		t.Fatal("blank declaration accepted")
	}
	if _, err := finalReview("stale", []string{"unverified"}); err == nil {
		t.Fatal("wrong generation accepted")
	}
	finalDeclare(t, root)
	// An old pre image that arrives after a newer review of this same image
	// is not a new write; a genuine return to an earlier image is a new epoch.
	old := in
	old.ToolUseID = "late-old-pre"
	HookReply("pre", old)
	finalWrite(t, root, testMCPSession, "", "new-image", "value.go", "package sample\nconst Value = 3\n")
	reviewed := finalDeclare(t, root)
	HookReply("post", old)
	if finalGeneration(finalSaved(t, root, testMCPSession)) != reviewed {
		t.Fatal("late post reopened the already reviewed image")
	}
	finalWrite(t, root, testMCPSession, "", "return-image", "value.go", "package sample\nconst Value = 2\n")
	if finalGeneration(finalSaved(t, root, testMCPSession)) == g {
		t.Fatal("A-B-A reused an old generation")
	}
	if _, err := finalReview(reviewed, []string{"old report"}); err == nil {
		t.Fatal("old declaration survived a new source image")
	}
}
func TestFinalReviewScopeAndFailures(t *testing.T) {
	root := finalFixture(t)
	finalWrite(t, root, testMCPSession, "", "create", "new.go", "package sample\nconst New = 1\n")
	created := finalGeneration(finalSaved(t, root, testMCPSession))
	if created == "" {
		t.Fatal("creation not tracked")
	}
	finalWrite(t, root, testMCPSession, "", "delete", "new.go", "")
	if f := finalSaved(t, root, testMCPSession); f.Files["new.go"] != "missing" || finalGeneration(f) == created {
		t.Fatal("deletion not tracked")
	}
	finalDeclare(t, root)
	HookReply("post", base.HookInput{Cwd: root, SessionID: testMCPSession, ToolName: "Bash", Command: "go test ./...", ToolUseID: "start-only"})
	if f := finalSaved(t, root, testMCPSession); f.Reviewed != finalGeneration(f) {
		t.Fatal("test command start changed declaration")
	}
	failure := base.HookInput{Cwd: root, SessionID: testMCPSession, ToolName: "Bash", ToolUseID: "failure-once", TurnID: "neutral-turn", Command: "python -m pytest -q", ToolOutput: "=== 1 failed in 0.01s ==="}
	HookReply("post", failure)
	failed := finalSaved(t, root, testMCPSession)
	if failed.Reviewed == finalGeneration(failed) {
		t.Fatal("recognized new failure did not invalidate declaration")
	}
	before := finalGeneration(failed)
	HookReply("post", failure)
	if finalGeneration(finalSaved(t, root, testMCPSession)) != before {
		t.Fatal("duplicate failure post advanced final generation")
	}
	// Child writes have their actual context; neither context may settle the
	// other's obligation. A changed tracked image invalidates its old review.
	finalWrite(t, root, testMCPSession, "child-neutral", "child-write", "value.go", "package sample\nconst Value = 7\n")
	child := finalSaved(t, root, "child-neutral")
	if len(child.Files) != 1 {
		t.Fatal("child context attribution lost")
	}
	if _, err := finalReview(finalGeneration(child), []string{"child claim from parent"}); err == nil {
		t.Fatal("parent settled child's generation")
	}
	finalWrite(t, root, testMCPSession, "", "parent-track", "value.go", "package sample\nconst Value = 8\n")
	parentGen := finalDeclare(t, root)
	finalWrite(t, root, testMCPSession, "child-neutral", "child-rewrite", "value.go", "package sample\nconst Value = 9\n")
	if _, err := finalReview(parentGen, []string{"old parent image"}); err == nil {
		t.Fatal("cross-context tracked source change left old report valid")
	}
	// A failure/read/metadata edit without source write does not create a
	// final source obligation in another context.
	HookReply("post", base.HookInput{Cwd: root, SessionID: "read-only", ToolName: "Read", FilePath: filepath.Join(root, "value.go")})
	HookReply("post", base.HookInput{Cwd: root, SessionID: "read-only", ToolName: "Bash", ToolUseID: "only-failure", Command: failure.Command, ToolOutput: failure.ToolOutput})
	finalWrite(t, root, "read-only", "", "metadata", "notes.txt", "metadata only")
	if len(finalSaved(t, root, "read-only").Files) != 0 {
		t.Fatal("no source delta created obligation")
	}
}
func TestFinalReviewBudgetAndActive(t *testing.T) {
	root := finalFixture(t)
	for i := 0; i < 12; i++ {
		finalWrite(t, root, testMCPSession, "", fmt.Sprint("path-", i), fmt.Sprintf("module_%02d/value.go", i), "package sample\nconst Value = 1\n")
	}
	in := base.HookInput{Cwd: root, SessionID: testMCPSession}
	g := finalGeneration(finalSaved(t, root, testMCPSession))
	in.StopHookActive = true
	if got := finalReviewStopNoteBudget(root, in, 4096); got != "" {
		t.Fatal("active Stop blocked")
	}
	if f := finalSaved(t, root, testMCPSession); f.Prompted != "" || f.Reviewed != "" {
		t.Fatal("active Stop marked delivery/completion")
	}
	in.StopHookActive = false
	if got := finalReviewStopNoteBudget(root, in, 1); got != "" {
		t.Fatal("over-budget final text sent")
	}
	if finalSaved(t, root, testMCPSession).Prompted != "" {
		t.Fatal("deferred text marked delivered")
	}
	out := finalReviewStopNoteBudget(root, in, 4096)
	if len(out) > 4096 || !utf8.ValidString(out) || !strings.Contains(out, g) || !strings.Contains(out, "另 4") {
		t.Fatalf("bounded complete generation view: %s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "- ") {
			q, err := strconv.QuotedPrefix(line[2:])
			if err != nil {
				t.Fatal(err)
			}
			p, err := strconv.Unquote(q)
			if err != nil || !strings.HasSuffix(p, "/value.go") {
				t.Fatal("incomplete path")
			}
		}
	}
	if got := finalReviewStopNoteBudget(root, in, 4096); got != "" {
		t.Fatal("same generation repeated Stop request")
	}
	if f := finalSaved(t, root, testMCPSession); f.Reviewed == g {
		t.Fatal("delivery was treated as a declaration")
	}
	finalWrite(t, root, testMCPSession, "", "new-stop-generation", "module_00/value.go", "package sample\nconst Value = 2\n")
	if got := finalReviewStopNoteBudget(root, in, 4096); got == "" {
		t.Fatal("new generation did not rearm Stop")
	}
	if _, err := finalReview(finalGeneration(finalSaved(t, root, testMCPSession)), []string{strings.Repeat("文", 1500)}); err == nil {
		t.Fatal("oversized report accepted")
	}
}

func TestFinalReviewLimitsAndUnknown(t *testing.T) {
	root := finalFixture(t)
	long := "000/" + strings.Repeat(strings.Repeat("长", 80)+"/", 15) + "value.go"
	quoted := "a/开发 \"指南\" (示例)/value.go"
	for i, path := range []string{long, quoted, "z/value.go"} {
		finalWrite(t, root, testMCPSession, "", fmt.Sprint("long-", i), path, "package sample\nconst Value = 1\n")
	}
	in := base.HookInput{Cwd: root, SessionID: testMCPSession}
	out := finalReviewStopNoteBudget(root, in, 4096)
	if len(out) > 4096 || !utf8.ValidString(out) || strings.Contains(out, strconv.Quote(long)) || !strings.Contains(out, strconv.Quote(quoted)) || !strings.Contains(out, "另 1") {
		t.Fatalf("UTF8 complete path overflow: %s", out)
	}
	// Omitted display paths still participate in the current generation check.
	g := finalGeneration(finalSaved(t, root, testMCPSession))
	writeFile(t, filepath.Join(root, long), "package sample\nconst Value = 2\n")
	if _, err := finalReview(g, []string{"old omitted source"}); err == nil {
		t.Fatal("omitted tracked source was excluded from generation")
	}
	current := finalDeclare(t, root)
	// A directory/unknown read result is not the missing-file token and must
	// not permit acknowledgement of an unverifiable image.
	path := filepath.Join(root, "z/value.go")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := finalReview(current, []string{"unknown image"}); err == nil {
		t.Fatal("unknown source image accepted")
	}
	note := finalReviewStopNoteBudget(root, in, 4096)
	if !strings.Contains(note, "无法取得内容指纹") {
		t.Fatalf("unknown current image silently passed: %s", note)
	}
	if finalReviewStopNoteBudget(root, in, 4096) != "" {
		t.Fatal("unknown image warning repeated within one generation")
	}
}

func TestFinalReviewNonSourceAndReset(t *testing.T) {
	root := finalFixture(t)
	if _, err := Header("", nil, []string{"ignored.go"}, nil, nil, []string{"watched.go"}, nil); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"ignored.go", "watched.go", "notes.txt", "package.json", "pyproject.toml", "setup.cfg", "config.ini", "go.mod"} {
		finalWrite(t, root, testMCPSession, "", p, p, "neutral metadata")
	}
	writeFile(t, filepath.Join(root, "value.go"), "package sample\nconst Value = 2\n")
	HookReply("post", base.HookInput{Cwd: root, SessionID: testMCPSession, ToolUseID: "unpaired", ToolName: "Edit", FilePath: filepath.Join(root, "value.go")})
	if len(finalSaved(t, root, testMCPSession).Files) != 0 {
		t.Fatal("role-excluded, metadata or unpaired writes armed final review")
	}
	if _, err := Header("", nil, nil, nil, nil, nil, nil, "build"); err != nil {
		t.Fatal(err)
	}
	finalWrite(t, root, testMCPSession, "", "build-source", "value.go", "package sample\nconst Value = 3\n")
	if len(finalSaved(t, root, testMCPSession).Files) != 0 {
		t.Fatal("build workflow acquired a task final obligation")
	}
	if _, err := Header("", nil, nil, nil, nil, nil, nil, "task"); err != nil {
		t.Fatal(err)
	}
	finalWrite(t, root, testMCPSession, "", "task-source", "value.go", "package sample\nconst Value = 4\n")
	in := base.HookInput{Cwd: root, SessionID: testMCPSession}
	if finalReviewStopNoteBudget(root, in, 4096) == "" {
		t.Fatal("real task source did not arm Stop")
	}
	before := finalSaved(t, root, testMCPSession)
	HookReply("compact", in)
	after := finalSaved(t, root, testMCPSession)
	if finalGeneration(before) != finalGeneration(after) || before.Prompted != after.Prompted || finalReviewStopNoteBudget(root, in, 4096) != "" {
		t.Fatal("compaction reset persistent final obligation/delivery")
	}
}

func TestFinalReviewOperationModeAndUnidentifiedFailure(t *testing.T) {
	root := finalFixture(t)
	in := base.HookInput{Cwd: root, SessionID: testMCPSession, ToolUseID: "task-to-build", ToolName: "Edit", FilePath: filepath.Join(root, "value.go")}
	HookReply("pre", in)
	if _, err := Header("", nil, nil, nil, nil, nil, nil, "build"); err != nil {
		t.Fatal(err)
	}
	HookReply("pre", in) // duplicate pre retains the original operation mode
	writeFile(t, in.FilePath, "package sample\nconst Value = 2\n")
	HookReply("post", in)
	f := finalSaved(t, root, testMCPSession)
	if len(f.Files) != 1 {
		t.Fatal("post mode switch erased the task operation's obligation")
	}
	if out, err := Status(); err != nil || !strings.Contains(out, finalReviewPending) || !strings.Contains(out, finalGeneration(f)) {
		t.Fatalf("status hid a task obligation after mode switch: %s %v", out, err)
	}
	finalDeclare(t, root)
	if out, err := Status(); err != nil || !strings.Contains(out, finalReviewDone) {
		t.Fatalf("status lost current declaration: %s %v", out, err)
	}
	// A source operation begun in build is not adopted merely because the
	// metadata mode becomes task before its post event.
	in.SessionID = "started-build"
	in.ToolUseID = "build-to-task"
	HookReply("pre", in)
	writeFile(t, in.FilePath, "package sample\nconst Value = 3\n")
	if _, err := Header("", nil, nil, nil, nil, nil, nil, "task"); err != nil {
		t.Fatal(err)
	}
	HookReply("post", in)
	if len(finalSaved(t, root, "started-build").Files) != 0 {
		t.Fatal("mode metadata adopted a build operation")
	}
	finalDeclare(t, root) // refreshes only this caller's already tracked image
	failure := base.HookInput{Cwd: root, SessionID: testMCPSession, ToolName: "Bash", Command: "python -m pytest -q", ToolOutput: "=== 1 failed in 0.01s ==="}
	out := HookReply("post", failure)
	if !strings.Contains(out, finalFailureUnknown) {
		t.Fatal("unidentified failure did not expose conservative attribution boundary")
	}
	old := finalGeneration(finalSaved(t, root, testMCPSession))
	finalDeclare(t, root)
	HookReply("post", failure)
	if finalGeneration(finalSaved(t, root, testMCPSession)) == old {
		t.Fatal("unknown replay/new-run ambiguity was treated as definite old evidence")
	}
}

// This child executes the actual CLI hook decoder and host output envelope.
func TestFinalReviewHookProcess(t *testing.T) {
	if os.Getenv("ORTG_FINAL_HOOK_CHILD") != "1" {
		return
	}
	if err := runHook([]string{"--event", "stop"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	os.Exit(0)
}
func finalHookWire(t *testing.T, root, host string, active bool) string {
	t.Helper()
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(map[string]any{"cwd": root, "session_id": testMCPSession, "stop_hook_active": active, "hook_event_name": "Stop"})
	cmd := exec.Command(bin, "-test.run=^TestFinalReviewHookProcess$")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "ORTG_FINAL_HOOK_CHILD=1", "ORTG_AGENT="+host)
	cmd.Stdin = bytes.NewReader(input)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("real CLI hook: %v %s", err, out)
	}
	return string(out)
}

// This follows the public wire path that obtains a generation from Status
// and submits directly, without relying on a prior final inspection or Stop.
func TestFinalReviewPendingStatusSubmission(t *testing.T) {
	for _, host := range []string{"claude", "codex"} {
		t.Run(host, func(t *testing.T) {
			t.Setenv("ORTG_AGENT", host)
			root := finalFixture(t)
			call := func(name string, args map[string]any) updateMCPReply {
				return nativeMCP(t, root, map[string]any{"method": "tools/call", "params": map[string]any{"name": name, "arguments": args, "_meta": map[string]any{"threadId": testMCPSession}}})[0]
			}
			status := call("ortg_status", map[string]any{})
			if status.Result.IsError || strings.Contains(status.Result.Content[0].Text, finalReviewInstruction) {
				t.Fatal("no-obligation Status must remain concise")
			}
			finalWrite(t, root, testMCPSession, "", "pending-status", "value.go", "package sample\nconst Value = 4\n")
			before, _ := json.Marshal(finalSaved(t, root, testMCPSession))
			var fromStatus string
			for i := 0; i < 2; i++ {
				pending := call("ortg_status", map[string]any{})
				if pending.Result.IsError {
					t.Fatal("Status failed")
				}
				text := pending.Result.Content[0].Text
				if !strings.Contains(text, finalReviewPending) || strings.Count(text, finalReviewInstruction) != 1 {
					t.Fatal("pending Status did not deliver the shared final guidance exactly once")
				}
				_, tail, ok := strings.Cut(text, "generation=")
				if !ok {
					t.Fatal("Status nonce missing")
				}
				quoted, err := strconv.QuotedPrefix(tail)
				if err != nil {
					t.Fatal(err)
				}
				fromStatus, err = strconv.Unquote(quoted)
				if err != nil {
					t.Fatal(err)
				}
				after, _ := json.Marshal(finalSaved(t, root, testMCPSession))
				if !bytes.Equal(before, after) {
					t.Fatal("Status delivery changed final generation/declaration/Stop markers")
				}
			}
			listed := nativeMCP(t, root, map[string]any{"method": "tools/list"})[0]
			found := false
			for _, tool := range listed.Result.Tools {
				if tool.Name == "ortg_review" {
					found = true
					for _, wireText := range []string{tool.Description, tool.InputSchema.Properties["report"].Description} {
						for _, need := range []string{"显式条件", "真实断言", "未验证", "计数"} {
							if !strings.Contains(wireText, need) {
								t.Fatalf("client descriptor did not carry assertion guidance %q", need)
							}
						}
					}
				}
			}
			if !found {
				t.Fatal("review descriptor missing")
			}
			if wire := finalHookWire(t, root, host, true); strings.TrimSpace(wire) != "{}" {
				t.Fatal("active Stop must remain quiet after pending Status")
			}
			submitted := call("ortg_review", map[string]any{"final": true, "generation": fromStatus, "report": []string{"按公开任务静态核对当前常量；运行断言尚未执行，保持未验证。"}})
			if submitted.Result.IsError || !strings.Contains(submitted.Result.Content[0].Text, finalReviewRecorded) {
				t.Fatal("direct same-generation declaration from Status failed")
			}
			f := finalSaved(t, root, testMCPSession)
			if f.Reviewed != fromStatus || f.Prompted != "" {
				t.Fatal("Status/submit consumed Stop opportunity or failed to preserve identity")
			}
			reviewed := call("ortg_status", map[string]any{})
			if reviewed.Result.IsError || !strings.Contains(reviewed.Result.Content[0].Text, finalReviewDone) || strings.Contains(reviewed.Result.Content[0].Text, finalReviewInstruction) {
				t.Fatal("reviewed Status must stay concise")
			}
			plan := call("ortg_review", map[string]any{"final": false})
			if plan.Result.IsError || plan.Result.Content[0].Text != reviewEmpty || finalSaved(t, root, testMCPSession).Reviewed != fromStatus {
				t.Fatal("final=false changed default plan Review or final state")
			}
		})
	}
}

func TestFinalReviewNativeDelivery(t *testing.T) {
	for _, host := range []string{"claude", "codex"} {
		t.Run(host, func(t *testing.T) {
			t.Setenv("ORTG_AGENT", host)
			root := finalFixture(t)
			finalWrite(t, root, testMCPSession, "", "native-write", "value.go", "package sample\nconst Value = 4\n")
			g := finalGeneration(finalSaved(t, root, testMCPSession))
			listed := nativeMCP(t, root, map[string]any{"method": "tools/list"})[0]
			found := false
			for _, tool := range listed.Result.Tools {
				if tool.Name == "ortg_review" {
					found = true
					if tool.InputSchema.Properties["final"].Type != "boolean" || tool.InputSchema.Properties["generation"].Type != "string" || tool.InputSchema.Properties["report"].Type != "array" {
						t.Fatal("final review schema missing")
					}
				}
			}
			if !found {
				t.Fatal("review missing")
			}
			wire := finalHookWire(t, root, host, false)
			var envelope struct{ Decision, Reason string }
			if err := json.Unmarshal([]byte(wire), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Decision != "block" || !strings.Contains(envelope.Reason, g) || !strings.Contains(envelope.Reason, "ortg_review") || !strings.Contains(envelope.Reason, strings.TrimSpace(finalReviewInstruction)) {
				t.Fatalf("final text did not cross actual CLI envelope: %s", wire)
			}
			if wire := finalHookWire(t, root, host, true); strings.TrimSpace(wire) != "{}" {
				t.Fatal("active CLI hook must be empty JSON")
			}
			if wire := finalHookWire(t, root, host, false); strings.TrimSpace(wire) != "{}" {
				t.Fatal("persisted Stop marker did not survive new process")
			}
			call := func(args map[string]any, sid string) updateMCPReply {
				return nativeMCP(t, root, map[string]any{"method": "tools/call", "params": map[string]any{"name": "ortg_review", "arguments": args, "_meta": map[string]any{"threadId": sid}}})[0]
			}
			inspect := call(map[string]any{"final": true}, testMCPSession)
			if inspect.Result.IsError || !strings.Contains(inspect.Result.Content[0].Text, g) || !strings.Contains(inspect.Result.Content[0].Text, finalReviewInstruction) {
				t.Fatal("native final inspection missing current generation")
			}
			wrong := call(map[string]any{"final": true, "generation": g, "report": []string{"unverified"}}, "other-neutral-context")
			if !wrong.Result.IsError {
				t.Fatal("other SID closed declaration")
			}
			accepted := call(map[string]any{"final": true, "generation": g, "report": []string{"静态检查公开任务所需的当前常量；运行行为未验证。"}}, testMCPSession)
			if accepted.Result.IsError || !strings.Contains(accepted.Result.Content[0].Text, finalReviewRecorded) {
				t.Fatalf("native current generation declaration: %+v", accepted)
			}
			finalWrite(t, root, testMCPSession, "", "changed-after-report", "value.go", "package sample\nconst Value = 5\n")
			stale := call(map[string]any{"final": true, "generation": g, "report": []string{"stale claim"}}, testMCPSession)
			if !stale.Result.IsError {
				t.Fatal("old report accepted after source edit")
			}
		})
	}
}
