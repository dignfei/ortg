package main

import (
	"bytes"
	"crypto/sha1"
	"encoding/json"
	"errors"
	"fmt"
	"ortg/base"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
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
	finishRules(t)
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

func finalRunInput(t *testing.T, in base.HookInput, response any) base.HookInput {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"hook_event_name": "PostToolUse", "cwd": in.Cwd, "session_id": in.SessionID, "agent_id": in.AgentID, "tool_name": in.ToolName, "tool_use_id": in.ToolUseID, "turn_id": in.TurnID, "tool_input": map[string]any{"command": in.Command, "run_in_background": in.Background}, "tool_response": response})
	if err != nil {
		t.Fatal(err)
	}
	return base.ReadHookInput(bytes.NewReader(raw), time.Second)
}

func finalRunFixture(t *testing.T, root, use, command string) base.HookInput {
	t.Helper()
	in := base.HookInput{Cwd: root, SessionID: testMCPSession, TurnID: "neutral-turn", ToolName: "Bash", ToolUseID: use, Command: command}
	HookReply("pre", in)
	return in
}

func finalRequirementValue(status string, ids ...string) []map[string]any {
	return []map[string]any{{"requirement": "the public neutral value remains available", "status": status, "assertion": "checked the persisted fixture through its public test consumer; other requirements remain unverified", "evidence": ids}}
}

// A real subprocess gives the hook fixture an actual result. No production
// object or function is replaced to make this assertion pass.
func TestFinalEvidenceAssertionProcess(t *testing.T) {
	if os.Getenv("ORTG_FINAL_ASSERTION_CHILD") != "1" {
		return
	}
	body, err := os.ReadFile("value.go")
	if err != nil || !strings.Contains(string(body), "const Value = 2") {
		fmt.Fprintln(os.Stderr, "persisted fixture assertion failed")
		os.Exit(1)
	}
	fmt.Println("persisted fixture assertion completed")
	os.Exit(0)
}

// Codex 0.159.3's ExecCommandToolOutput forwards raw process output to
// PostToolUse, without the status header shown in the model-facing result.
func TestFinalRawOutputProcess(t *testing.T) {
	mode := os.Getenv("ORTG_FINAL_RAW_OUTPUT_CHILD")
	if mode == "" {
		return
	}
	if mode == "json" {
		fmt.Print(`{"exit_code":0,"output":"neutral assertion passed"}`)
	} else {
		fmt.Print("Chunk ID: neutral\nWall time: 0.01 seconds\nProcess exited with code 0\nFinal output:\nneutral assertion passed\n")
	}
	os.Exit(1)
}

func TestFinalRawOutputCannotSupplyExit(t *testing.T) {
	for _, shape := range []string{"header", "json", "output field"} {
		t.Run(shape, func(t *testing.T) {
			t.Setenv("ORTG_AGENT", "codex")
			t.Setenv("ORTG_STATE_DIR", t.TempDir())
			root := finalFixture(t)
			finalWrite(t, root, testMCPSession, "", "source", "value.go", "package sample\nconst Value = 2\n")
			bin, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			command := strconv.Quote(bin) + " -test.run=^TestFinalRawOutputProcess$"
			in := base.HookInput{Cwd: root, SessionID: testMCPSession, ToolName: "Bash", ToolUseID: "raw-output", TurnID: "neutral-turn", Command: command}
			wire := func(event string, response any) {
				t.Helper()
				raw, err := json.Marshal(map[string]any{"hook_event_name": map[string]string{"pre": "PreToolUse", "post": "PostToolUse"}[event], "cwd": root, "session_id": in.SessionID, "tool_name": in.ToolName, "tool_use_id": in.ToolUseID, "turn_id": in.TurnID, "tool_input": map[string]any{"command": command}, "tool_response": response})
				if err != nil {
					t.Fatal(err)
				}
				cmd := exec.Command(bin, "-test.run=^TestFinalReviewHookProcess$")
				cmd.Dir, cmd.Env, cmd.Stdin = root, append(os.Environ(), "ORTG_FINAL_HOOK_CHILD=1", "ORTG_FINAL_HOOK_EVENT="+event), bytes.NewReader(raw)
				if body, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("actual CLI %s hook: %v %s", event, err, body)
				}
			}
			wire("pre", nil)
			cmd := exec.Command(bin, "-test.run=^TestFinalRawOutputProcess$")
			cmd.Dir, cmd.Env = root, append(os.Environ(), "ORTG_FINAL_RAW_OUTPUT_CHILD="+shape)
			body, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 1 {
				t.Fatalf("neutral child must really fail: %v %s", err, body)
			}
			var response any = string(body)
			if shape == "output field" {
				response = map[string]any{"output": string(body)}
			}
			wire("post", response)
			f := finalSaved(t, root, testMCPSession)
			if len(f.Runs) != 0 {
				t.Fatalf("program text created a trusted exit receipt: %+v", f.Runs)
			}
			call := func(args map[string]any) updateMCPReply {
				return nativeMCP(t, root, map[string]any{"method": "tools/call", "params": map[string]any{"name": "ortg_review", "arguments": args, "_meta": map[string]any{"threadId": testMCPSession}}})[0]
			}
			inspect := call(map[string]any{"final": true})
			if inspect.Result.IsError || !strings.Contains(inspect.Result.Content[0].Text, finalReviewPending) {
				t.Fatal("missing host status must keep the final result pending")
			}
			_, id := writeIdentity(in)
			if out := call(map[string]any{"final": true, "generation": finalGeneration(f), "requirements": finalRequirementValue("supported", id)}); !out.Result.IsError {
				t.Fatal("raw output supported an evidence-backed declaration")
			}
			unknown := finalRequirementValue("unverified")
			unknown[0]["assertion"] = "host provided only raw output; exit status is unknown"
			if out := call(map[string]any{"final": true, "generation": finalGeneration(f), "requirements": unknown}); out.Result.IsError {
				t.Fatal("unknown status prevented an honest unverified declaration")
			}
		})
	}
}

func TestFinalEvidenceEpoch(t *testing.T) {
	root := finalFixture(t)
	finalWrite(t, root, testMCPSession, "", "source", "value.go", "package sample\nconst Value = 2\n")
	HookReply("post", base.HookInput{Cwd: root, SessionID: testMCPSession, ToolName: "Bash", ToolUseID: "failure-debt", Command: "pytest tests", ToolOutput: "=== 1 failed in 0.1s ==="})
	if _, err := Target("peer.go", "=", false, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := Review(""); err != nil {
		t.Fatal(err)
	}
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "-test.run=^TestFinalRawOutputProcess$")
	cmd.Dir, cmd.Env = root, append(os.Environ(), "ORTG_FINAL_RAW_OUTPUT_CHILD=header")
	body, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("old receipt fixture must really fail: %v %s", err, body)
	}
	f := finalSaved(t, root, testMCPSession)
	if len(f.Files) != 1 || f.Files["value.go"] == "" {
		t.Fatal("legacy fixture must have exactly one attributed source")
	}
	// Reproduce the pre-0.5.97 hash and persisted record, including the false
	// status the old decoder took from this failed child's printed header.
	oldGeneration := func(revision uint64) string {
		return fmt.Sprintf("%x", sha1.Sum([]byte(fmt.Sprintf("%s\x00%d\x00value.go\x00%s\n", f.Scope, revision, f.Files["value.go"]))))
	}
	old := oldGeneration(f.Revision)
	if err := withWriteProofs(root, func(s *writeProofs) (bool, error) {
		f.Runs = map[string]finalRun{"old-text-status": {ID: "old-text-status", Context: testMCPSession, Operation: "old-text-status", Cwd: root, Command: strconv.Quote(bin) + " -test.run=^TestFinalRawOutputProcess$", Generation: old, SourceGeneration: oldGeneration(f.SourceRevision), ExitCode: 0, Complete: true, Summary: fmt.Sprintf(finalRunSummary, len(body), fmt.Sprintf("%x", sha1.Sum(body))), At: time.Now()}}
		f.Reviewed, f.Prompted = old, old
		raw, _ := json.Marshal(finalRequirementValue("supported", "old-text-status"))
		if err := json.Unmarshal(raw, &f.Requirements); err != nil {
			return false, err
		}
		s.Final[testMCPSession] = f
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	table, err := os.ReadFile(filepath.Join(root, base.TableName))
	if err != nil {
		t.Fatal(err)
	}
	failures, _ := json.Marshal(loadFailedTests(root, testMCPSession))
	before, _ := json.Marshal(f.Files)
	current := finalGeneration(f)
	if current == old {
		t.Fatal("old status protocol retained its final generation")
	}
	listed, err := finalReview("", nil)
	if err != nil || !strings.Contains(listed, finalReviewPending) || strings.Contains(listed, `"operation":"old-text-status"`) {
		t.Fatalf("old declaration or receipt remained current: %v %s", err, listed)
	}
	if _, err := finalRequirements(current, nil, finalRequirementValue("supported", "old-text-status")); err == nil {
		t.Fatal("old status protocol supported a new declaration")
	}
	after := finalSaved(t, root, testMCPSession)
	files, _ := json.Marshal(after.Files)
	debt, _ := json.Marshal(loadFailedTests(root, testMCPSession))
	tableAfter, err := os.ReadFile(filepath.Join(root, base.TableName))
	if err != nil || !bytes.Equal(table, tableAfter) || !bytes.Equal(before, files) || !bytes.Equal(failures, debt) || after.Revision != f.Revision || after.SourceRevision != f.SourceRevision {
		t.Fatal("evidence upgrade changed outlines, plan, source obligation or failure debt")
	}
	command := strconv.Quote(bin) + " -test.run=^TestFinalEvidenceAssertionProcess$"
	in := finalRunFixture(t, root, "new-structured-status", command)
	cmd = exec.Command(bin, "-test.run=^TestFinalEvidenceAssertionProcess$")
	cmd.Dir, cmd.Env = root, append(os.Environ(), "ORTG_FINAL_ASSERTION_CHILD=1")
	body, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("current persistent source assertion: %v %s", err, body)
	}
	HookReply("post", finalRunInput(t, in, map[string]any{"stdout": string(body), "exit_code": cmd.ProcessState.ExitCode()}))
	_, id := writeIdentity(in)
	if _, err := finalRequirements(current, nil, finalRequirementValue("supported", id)); err != nil {
		t.Fatalf("new structured result could not support its current declaration: %v", err)
	}
}

func TestFinalRequirementsRuntimeAndMCP(t *testing.T) {
	root := finalFixture(t)
	finalWrite(t, root, testMCPSession, "", "source", "value.go", "package sample\nconst Value = 2\n")
	// A successful auxiliary command must not settle an unrelated pytest debt.
	HookReply("post", base.HookInput{Cwd: root, SessionID: testMCPSession, ToolName: "Bash", ToolUseID: "known-failure", Command: "pytest tests", ToolOutput: "=== 1 failed in 0.1s ==="})
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := strconv.Quote(bin) + " -test.run=^TestFinalEvidenceAssertionProcess$"
	in := finalRunFixture(t, root, "actual-assertion", command)
	cmd := exec.Command(bin, "-test.run=^TestFinalEvidenceAssertionProcess$")
	cmd.Dir, cmd.Env = root, append(os.Environ(), "ORTG_FINAL_ASSERTION_CHILD=1")
	body, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("actual fixture assertion: %v %s", err, body)
	}
	HookReply("post", finalRunInput(t, in, map[string]any{"stdout": string(body), "exit_code": 0}))
	call := func(args map[string]any) updateMCPReply {
		return nativeMCP(t, root, map[string]any{"method": "tools/call", "params": map[string]any{"name": "ortg_review", "arguments": args, "_meta": map[string]any{"threadId": testMCPSession}}})[0]
	}
	listed := nativeMCP(t, root, map[string]any{"method": "tools/list"})[0]
	for _, tool := range listed.Result.Tools {
		if tool.Name == "ortg_review" && (tool.InputSchema.Properties["requirements"].Type != "array" || tool.InputSchema.Properties["requirements"].Items.Type != "object" || tool.InputSchema.Properties["report"].Items.Type != "string") {
			t.Fatal("structured array schema or legacy strings changed")
		}
	}
	inspect := call(map[string]any{"final": true})
	if inspect.Result.IsError {
		t.Fatal(inspect.Result.Content)
	}
	var receipt finalRun
	for _, line := range strings.Split(inspect.Result.Content[0].Text, "\n") {
		if strings.HasPrefix(line, "{") && json.Unmarshal([]byte(line), &receipt) == nil && receipt.Operation == "actual-assertion" {
			break
		}
	}
	f := finalSaved(t, root, testMCPSession)
	if receipt.ID == "" || receipt.Context != testMCPSession || receipt.Cwd != root || receipt.Command != command || receipt.Generation != finalGeneration(f) || receipt.SourceGeneration != finalSourceGeneration(f) || !receipt.Complete || receipt.ExitCode != 0 || receipt.Failed || !strings.Contains(receipt.Summary, "sha1=") {
		t.Fatalf("model could not consume actual receipt identity/result: %+v", receipt)
	}
	if strings.Contains(inspect.Result.Content[0].Text, string(body)) {
		t.Fatal("receipt persisted complete program stdout instead of a bounded summary")
	}
	submitted := call(map[string]any{"final": true, "generation": receipt.Generation, "requirements": finalRequirementValue("supported", receipt.ID)})
	if submitted.Result.IsError || !strings.Contains(submitted.Result.Content[0].Text, "supported=1") {
		t.Fatalf("actual MCP evidence-backed model declaration: %+v", submitted)
	}
	if len(loadFailedTests(root, testMCPSession).Runs) != 1 || len(finalSaved(t, root, testMCPSession).Requirements) != 1 {
		t.Fatal("structured declaration changed failure debt or was not persisted")
	}
	legacy := call(map[string]any{"final": true, "generation": receipt.Generation, "report": []string{"static legacy declaration"}})
	if legacy.Result.IsError || !strings.Contains(legacy.Result.Content[0].Text, finalReviewLegacy) || len(finalSaved(t, root, testMCPSession).Requirements) != 0 {
		t.Fatal("legacy declaration was certified or retained structured results")
	}
	for _, bad := range []any{nil, []any{}, []string{"supported"}, []map[string]any{{"requirement": "x", "status": "supported", "assertion": "no run"}}, []map[string]any{{"requirement": "x", "status": "invented", "assertion": "x"}}, []map[string]any{{"requirement": "x", "status": "unverified", "assertion": "x", "exit_code": 0}}} {
		if r := call(map[string]any{"final": true, "generation": receipt.Generation, "requirements": bad}); !r.Result.IsError {
			t.Fatalf("bad structured declaration accepted: %#v", bad)
		}
	}
	if r := call(map[string]any{"final": true, "generation": receipt.Generation, "requirements": finalRequirementValue("unverified"), "report": []string{}}); !r.Result.IsError {
		t.Fatal("mixed legacy and structured formats accepted")
	}
}

func TestFinalRequirementsRejectUnobservedAndStale(t *testing.T) {
	for _, tc := range []string{"invented", "no pre", "missing exit", "truncated", "background", "command mismatch", "cwd mismatch", "source during run", "cross SID A-B-A", "other mode A-B-A", "source after run", "failure after run", "cross SID"} {
		t.Run(tc, func(t *testing.T) {
			t.Setenv("ORTG_STATE_DIR", t.TempDir())
			root := finalFixture(t)
			finalWrite(t, root, testMCPSession, "", "source", "value.go", "package sample\nconst Value = 2\n")
			in := base.HookInput{Cwd: root, SessionID: testMCPSession, ToolName: "Bash", ToolUseID: "attempt", TurnID: "neutral-turn", Command: "go test ./..."}
			if tc == "background" {
				in.Background = true
			}
			if tc != "no pre" {
				HookReply("pre", in)
			}
			if tc == "source during run" {
				finalWrite(t, root, testMCPSession, "", "during", "value.go", "package sample\nconst Value = 3\n")
			}
			if tc == "cross SID A-B-A" || tc == "other mode A-B-A" {
				if tc == "other mode A-B-A" {
					if _, err := Header("", nil, nil, nil, nil, nil, nil, "build"); err != nil {
						t.Fatal(err)
					}
				}
				finalWrite(t, root, testMCPSession, "other-neutral-sid", "other-image", "value.go", "package sample\nconst Value = 3\n")
				finalWrite(t, root, testMCPSession, "other-neutral-sid", "restored-image", "value.go", "package sample\nconst Value = 2\n")
				if tc == "other mode A-B-A" && len(finalSaved(t, root, "other-neutral-sid").Files) != 0 {
					t.Fatal("non-task write created another context's final obligation")
				}
			}
			if tc == "command mismatch" {
				in.Command = "go test -short ./..."
			}
			if tc == "cwd mismatch" {
				in.Cwd = filepath.Join(root, "sub")
				if err := os.MkdirAll(in.Cwd, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			response := map[string]any{"stdout": "ok", "exit_code": 0}
			if tc == "missing exit" {
				delete(response, "exit_code")
			}
			if tc == "truncated" {
				response["stdout"] = "[10 characters truncated]\nok"
			}
			HookReply("post", finalRunInput(t, in, response))
			_, id := writeIdentity(in)
			if tc == "invented" {
				id = "not-observed"
			}
			if tc == "source after run" {
				writeFile(t, filepath.Join(root, "value.go"), "package sample\nconst Value = 4\n")
				_, _ = finalReview("", nil)
			}
			if tc == "failure after run" {
				HookReply("post", base.HookInput{Cwd: root, SessionID: testMCPSession, ToolName: "Bash", ToolUseID: "later-failure", Command: "pytest tests", ToolOutput: "=== 1 failed in 0.1s ==="})
			}
			if tc == "cross SID" {
				finalWrite(t, root, testMCPSession, "child-neutral", "child-source", "peer.go", "package sample\nconst Peer = 3\n")
				callSession.ids = []string{"child-neutral"}
				finishRules(t)
			}
			context := callSession.ids[0]
			g := finalGeneration(finalSaved(t, root, context))
			if _, err := finalRequirements(g, nil, finalRequirementValue("supported", id)); err == nil {
				t.Fatalf("%s evidence was accepted", tc)
			}
			if _, err := finalRequirements(g, nil, finalRequirementValue("unverified")); err != nil {
				t.Fatalf("honest unknown declaration was rejected: %v", err)
			}
		})
	}
}

func TestFinalRequirementsFailureEpochAndBounds(t *testing.T) {
	root := finalFixture(t)
	finalWrite(t, root, testMCPSession, "", "source", "value.go", "package sample\nconst Value = 2\n")
	a := finalRunFixture(t, root, "overlapping-success", "go test ./...")
	b := finalRunFixture(t, root, "actual-failure", "pytest tests")
	HookReply("post", finalRunInput(t, b, map[string]any{"stdout": "=== 1 failed in 0.1s ===", "exit_code": 0}))
	_, failedID := writeIdentity(b)
	f := finalSaved(t, root, testMCPSession)
	g := finalGeneration(f)
	if run := f.Runs[failedID]; !run.Failed || run.Generation != g || run.SourceGeneration != finalSourceGeneration(f) {
		t.Fatalf("failure receipt lost its own current source epoch: %+v", run)
	}
	if _, err := finalRequirements(g, nil, finalRequirementValue("failed", failedID)); err != nil {
		t.Fatalf("actual failure could not be reported: %v", err)
	}
	if _, err := finalRequirements(g, nil, finalRequirementValue("supported", failedID)); err == nil {
		t.Fatal("recognized failure with exit0 supported a success claim")
	}
	HookReply("post", finalRunInput(t, a, map[string]any{"stdout": "ok", "exit_code": 0}))
	_, aID := writeIdentity(a)
	if _, exists := finalSaved(t, root, testMCPSession).Runs[aID]; exists {
		t.Fatal("overlapping older run rebound to a newer failure epoch")
	}
	nonzero := finalRunFixture(t, root, "explicit-nonzero", "go test ./...")
	HookReply("post", finalRunInput(t, nonzero, map[string]any{"stdout": "Wall time: 0.01 seconds\nExit code: 0\nOutput:\nneutral asserted success", "exit_code": 1}))
	_, nonzeroID := writeIdentity(nonzero)
	if _, err := finalRequirements(g, nil, finalRequirementValue("supported", nonzeroID)); err == nil {
		t.Fatal("nonzero execution supported a success claim")
	}
	if _, err := finalRequirements(g, nil, finalRequirementValue("blocked", nonzeroID)); err != nil {
		t.Fatalf("actual blocking result could not be reported: %v", err)
	}
	for i := 0; i < 20; i++ {
		in := finalRunFixture(t, root, fmt.Sprintf("bounded-%d", i), "go test ./...")
		HookReply("post", finalRunInput(t, in, map[string]any{"stdout": "ok", "exit_code": 0}))
	}
	f = finalSaved(t, root, testMCPSession)
	if len(f.Runs) != 16 {
		t.Fatalf("receipt storage is unbounded: %d", len(f.Runs))
	}
	if _, err := finalRequirements(g, nil, finalRequirementValue("failed", failedID)); err == nil {
		t.Fatal("evicted receipt was silently certified")
	}
	text := finalRunEvidenceText(f, 0)
	if len(text) > finalReviewMax || !strings.Contains(text, "未列") || !utf8.ValidString(text) {
		t.Fatal("receipt listing exceeded its budget or hid the omitted scope")
	}
}

func TestFailedTestsExplicitExit(t *testing.T) {
	const command = "python -m pytest -q tests"
	const passed = "=== 1 passed in 0.01s ==="
	const failed = "=== 1 failed in 0.01s ==="
	for _, tc := range []struct {
		name, command string
		response      any
		fresh, stale  bool
		remaining     int
	}{
		{"passed zero", command, map[string]any{"stdout": passed, "exit_code": 0}, false, false, 0},
		{"passed nonzero", command, map[string]any{"stdout": passed, "exit_code": 1}, false, true, 1},
		{"passed unknown", command, map[string]any{"stdout": passed}, false, false, 0},
		{"failed zero", command, map[string]any{"stdout": failed, "exit_code": 0}, false, true, 1},
		{"stderr failed zero", command, map[string]any{"stdout": passed, "stderr": failed, "exit_code": 0}, false, true, 1},
		{"ordinary nonzero", "python -c 'raise SystemExit(1)'", map[string]any{"stdout": failed, "exit_code": 1}, false, false, 1},
		{"raw exit first stays unknown", command, "Exit code: 1\nWall time: 0.01 seconds\nOutput:\n" + passed, false, false, 0},
		{"raw unified header stays unknown", command, "Chunk ID: neutral\nWall time: 0.01 seconds\nProcess exited with code 1\nFinal output:\n" + passed, false, false, 0},
		{"no summary nonzero", command, map[string]any{"stdout": "aborted", "exit_code": 2}, false, true, 1},
		{"no summary unknown", command, map[string]any{"stdout": "aborted"}, false, false, 1},
		{"fresh nonzero", command, map[string]any{"stdout": passed, "exit_code": 1}, true, true, 1},
		{"fresh ordinary nonzero", "false", map[string]any{"exit_code": 1}, true, false, 0},
		{"printed envelope", command, map[string]any{"stdout": "Exit code: 1\nWall time: 0.01 seconds\nOutput:\n" + passed}, false, false, 0},
		{"outer zero wins", command, "Wall time: 0.01 seconds\nExit code: 0\nOutput:\nExit code: 1\n" + passed, false, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ORTG_STATE_DIR", t.TempDir())
			root := finalFixture(t)
			finalWrite(t, root, testMCPSession, "", "write", "value.go", "package sample\nconst Value = 2\n")
			if !tc.fresh {
				HookReply("post", base.HookInput{Cwd: root, SessionID: testMCPSession, ToolName: "Bash", ToolUseID: "initial-failure", Command: command, ToolOutput: failed})
			}
			old := finalDeclare(t, root)
			raw, err := json.Marshal(map[string]any{"cwd": root, "session_id": testMCPSession, "tool_name": "Bash", "tool_use_id": "rerun", "turn_id": "neutral-turn", "tool_input": map[string]any{"command": tc.command}, "tool_response": tc.response})
			if err != nil {
				t.Fatal(err)
			}
			in := base.ReadHookInput(bytes.NewReader(raw), time.Second)
			HookReply("post", in)
			f := finalSaved(t, root, testMCPSession)
			g := finalGeneration(f)
			if got := len(loadFailedTests(root, testMCPSession).Runs); got != tc.remaining || (f.Reviewed != g) != tc.stale {
				t.Fatalf("remaining=%d, final stale=%v; want %d, %v", got, f.Reviewed != g, tc.remaining, tc.stale)
			}
			HookReply("post", in)
			if finalGeneration(finalSaved(t, root, testMCPSession)) != g {
				t.Fatal("duplicate failure post advanced the generation")
			}
			if tc.stale {
				if _, err := finalReview(old, []string{"old declaration"}); err == nil {
					t.Fatal("old generation was accepted after failed rerun")
				}
			}
		})
	}
	t.Run("selection and known nodes", func(t *testing.T) {
		t.Setenv("ORTG_STATE_DIR", t.TempDir())
		root := finalFixture(t)
		zero, one := 0, 1
		const cmd = "pytest -v tests"
		const id = "tests/test_value.py::test_value"
		header := "=== test session starts ===\nplatform linux -- Python 3.11, pytest-8.0, pluggy-1.0 -- /usr/bin/python\nrootdir: " + root + "\ncollecting ... collected 3 items\n"
		failedOutput := header + id + " FAILED [33%]\ntests/test_value.py::test_other PASSED [66%]\ntests/test_value.py::test_last PASSED [100%]\n=== FAILURES ===\n=== short test summary info ===\nFAILED " + id + " - AssertionError\n=== 1 failed, 2 passed in 0.2s ===\n"
		recordTestResult(root, testMCPSession, root, cmd, failedOutput, &one)
		key := root + "\x00" + cmd
		if r := loadFailedTests(root, testMCPSession).Runs[key]; r.Signature == "" || strings.Join(r.FailedNodeIDs, "") != id {
			t.Fatalf("fixture did not establish real failed-node proof: %+v", r)
		}
		recordTestResult(root, testMCPSession, root, cmd, passed, &one)
		r := loadFailedTests(root, testMCPSession).Runs[key]
		if strings.Join(r.FailedNodeIDs, "") != id || r.Signature != "" || len(r.Selected) != 1 || r.Selected[0] != 3 {
			t.Fatalf("nonzero rerun lost history or fabricated complete scope: %+v", r)
		}
		recordTestResult(root, testMCPSession, root, "pytest -v "+id, strings.Replace(header, "3 items", "1 item", 1)+id+" PASSED [100%]\n"+passed, &zero)
		recordTestResult(root, testMCPSession, root, cmd, passed, &zero)
		if len(loadFailedTests(root, testMCPSession).Runs) != 1 {
			t.Fatal("partial node or reduced-count pass cleared process failure")
		}
		recordTestResult(root, testMCPSession, root, cmd, "=== 3 passed in 0.2s ===", &zero)
		if len(loadFailedTests(root, testMCPSession).Runs) != 0 {
			t.Fatal("complete original-scope pass did not clear record")
		}
	})
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
	event := os.Getenv("ORTG_FINAL_HOOK_EVENT")
	if event == "" {
		event = "stop"
	}
	if err := runHook([]string{"--event", event}); err != nil {
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
