package main

import (
	"bytes"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"ortg/base"
)

// Run the production hook reader and host JSON writer in a separate process.
func TestResumeRulesHookProcess(t *testing.T) {
	if event := os.Getenv("ORTG_RESUME_RULES_HOOK"); event != "" {
		if err := runHook([]string{"--event", event}); err != nil {
			os.Stderr.WriteString(err.Error())
			os.Exit(2)
		}
		os.Exit(0)
	}
}

func resumeRulesHook(t *testing.T, root, event string, payload map[string]any) string {
	t.Helper()
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	wire, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "-test.run=^TestResumeRulesHookProcess$")
	cmd.Dir, cmd.Stdin = root, bytes.NewReader(wire)
	cmd.Env = append(os.Environ(), "ORTG_RESUME_RULES_HOOK="+event)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("hook: %v\n%s", err, out)
	}
	var reply struct {
		Specific struct {
			Context string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out, &reply); err != nil {
		t.Fatalf("hook JSON: %v\n%s", err, out)
	}
	return reply.Specific.Context
}

// Keep one real MCP process alive while independent hook processes deliver
// rules. Its permission cache must stay warm throughout both delivery orders.
func resumeRulesMCP(t *testing.T, root string) func(string, map[string]any) string {
	t.Helper()
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "-test.run=^TestUpdateMCPProcess$")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "ORTG_UPDATE_MCP_CHILD=1", "CLAUDE_CODE_SESSION_ID="+testMCPSession)
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		in.Close()
		if err := cmd.Wait(); err != nil {
			t.Errorf("MCP: %v\n%s", err, stderr.String())
		}
	})
	enc, dec, id := json.NewEncoder(in), json.NewDecoder(out), 0
	return func(name string, args map[string]any) string {
		t.Helper()
		id++
		params := map[string]any{"name": name, "arguments": args}
		if base.Host() == "codex" {
			params["_meta"] = map[string]any{"threadId": testMCPSession}
		}
		request := map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call", "params": params}
		if err := enc.Encode(request); err != nil {
			t.Fatal(err)
		}
		var reply updateMCPReply
		if err := dec.Decode(&reply); err != nil || len(reply.Error) > 0 || reply.Result.IsError || len(reply.Result.Content) != 1 {
			t.Fatalf("MCP reply: %v %+v\n%s", err, reply, stderr.String())
		}
		return reply.Result.Content[0].Text
	}
}

var ruleCursorRE = regexp.MustCompile(`rules="([0-9a-f]+)"`)

func rulePage(t *testing.T, text string) (string, string) {
	t.Helper()
	fits, _ := ruleReplyFits(repoRoot())
	if !fits(text) || !utf8.ValidString(text) {
		t.Fatal("invalid or oversized native rule page", len(text))
	}
	_, body, ok := strings.Cut(text, "\n---规则正文---\n")
	if !ok {
		t.Fatal("missing rule page head", text)
	}
	body, _, ok = strings.Cut(body, "\n---规则正文结束---\n")
	if !ok {
		t.Fatal("missing rule page tail")
	}
	m := ruleCursorRE.FindStringSubmatch(text)
	if len(m) != 2 {
		t.Fatal("missing continuation", text)
	}
	return body, m[1]
}

func TestRuleReplyBudgets(t *testing.T) {
	t.Setenv("ORTG_AGENT", "codex")
	useTestMCPSession(t)
	for _, c := range []struct {
		name, config string
		pages        int
	}{
		{"large", "tool_output_token_limit = 500000\nmodel_context_window = 258400\n", 1},
		{"small output", "tool_output_token_limit = 1024\nmodel_context_window = 258400\n", 7},
		{"small context", "tool_output_token_limit = 500000\nmodel_context_window = 3000\n", 0},
		{"unknown output", "model_context_window = 258400\n", 7},
		{"unknown context", "tool_output_token_limit = 500000\n", 7},
		{"unknown", "", 7},
	} {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("CODEX_HOME", home)
			writeFile(t, filepath.Join(home, "config.toml"), c.config)
			root := updateRepo(t, "a.go")
			scanWithHeader()
			mcp := resumeRulesMCP(t, root)
			out := mcp("ortg_overview", map[string]any{"rules": ""})
			body, count := "", 0
			for strings.Contains(out, "---规则正文---") {
				page, cursor := rulePage(t, out)
				body += page
				count++
				if mcpRulesKnown(root) {
					t.Fatal("page emission completed the receipt before ACK")
				}
				out = mcp("ortg_overview", map[string]any{"rules": cursor})
			}
			if out != rulesComplete || body != rulePayload() || c.pages > 0 && count != c.pages || count == 0 {
				t.Fatalf("pages=%d want=%d body bytes=%d: %s", count, c.pages, len(body), out)
			}
			t.Logf("pages=%d calls=%d body_bytes=%d", count, count+1, len(body))
		})
	}
}

func TestRuleBudgetChangesKeepGeometry(t *testing.T) {
	t.Setenv("ORTG_AGENT", "codex")
	useTestMCPSession(t)
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	config := func(output int) {
		writeFile(t, filepath.Join(home, "config.toml"), fmt.Sprintf("tool_output_token_limit = %d\nmodel_context_window = 258400\n", output))
	}
	config(1024)
	root := updateRepo(t, "a.go")
	scanWithHeader()
	// Every call is a new actual MCP process, sharing only production state.
	call := func(args map[string]any) updateMCPReply {
		return nativeMCP(t, root, ruleRequest(args, testMCPSession))[0]
	}
	first := call(map[string]any{"rules": ""}).Result.Content[0].Text
	body, cursor := rulePage(t, first)
	mark := "rules-disc-" + sessionKey(testMCPSession)
	_, old := base.OverviewMark(root, mark)
	var state ruleDelivery
	json.Unmarshal([]byte(old), &state)
	state.BodyMax = 0 // A partial chain written by the original implementation.
	wire, _ := json.Marshal(state)
	base.MarkOverview(root, mark, string(wire))
	config(500000)
	if retry := call(map[string]any{"rules": ""}).Result.Content[0].Text; retry != first {
		t.Fatal("budget increase or legacy geometry changed the previous response")
	}
	for len(body) < len(rulePayload()) {
		out := call(map[string]any{"rules": cursor}).Result.Content[0].Text
		page, next := rulePage(t, out)
		if len(page) > ruleBodyMax {
			t.Fatal("mid-chain budget increase changed geometry")
		}
		body, cursor = body+page, next
	}
	if body != rulePayload() || call(map[string]any{"rules": cursor}).Result.Content[0].Text != rulesComplete {
		t.Fatal("legacy chain lost bytes or ACK")
	}
	large := call(map[string]any{"rules": "", "force": true}).Result.Content[0].Text
	_, ack := rulePage(t, large)
	_, old = base.OverviewMark(root, mark)
	config(1024)
	if r := call(map[string]any{"rules": ""}); !r.Result.IsError {
		t.Fatal("replayed large response exceeded the reduced budget")
	}
	if _, after := base.OverviewMark(root, mark); after != old {
		t.Fatal("budget rejection changed progress")
	}
	// A smaller budget does not erase rules already emitted safely: their
	// short final ACK still completes, and a completed receipt stays valid.
	if r := call(map[string]any{"rules": ack}); r.Result.IsError || r.Result.Content[0].Text != rulesComplete {
		t.Fatal("budget reduction invalidated an already delivered final page", r)
	}
	if r := call(map[string]any{"rules": ""}); r.Result.IsError || r.Result.Content[0].Text != rulesComplete {
		t.Fatal("budget change erased a completed receipt")
	}
	_, old = base.OverviewMark(root, mark)
	config(1)
	if r := call(map[string]any{"rules": ""}); !r.Result.IsError {
		t.Fatal("completed receipt replay exceeded the reduced budget")
	}
	if _, after := base.OverviewMark(root, mark); after != old {
		t.Fatal("completed replay rejection changed the receipt")
	}
	config(1024)
	forced := call(map[string]any{"rules": "", "force": true}).Result.Content[0].Text
	page, _ := rulePage(t, forced)
	if len(page) > ruleBodyMax {
		t.Fatal("force did not select the reduced budget")
	}
	_, old = base.OverviewMark(root, mark)
	config(1)
	if r := call(map[string]any{"rules": "", "force": true}); !r.Result.IsError {
		t.Fatal("unfittable envelope accepted")
	}
	if _, after := base.OverviewMark(root, mark); after != old {
		t.Fatal("unfittable envelope changed progress")
	}
}

// Exercise the actual public page API and the extra completion call. These
// helpers do not create a receipt or replace any production function.
func collectRules(t *testing.T, call func(string, map[string]any) string) string {
	t.Helper()
	var body strings.Builder
	out := call("ortg_overview", map[string]any{"rules": ""})
	if out == rulesComplete {
		return ""
	}
	for i := 0; i < 64; i++ {
		page, next := rulePage(t, out)
		body.WriteString(page)
		out = call("ortg_overview", map[string]any{"rules": next})
		if out == rulesComplete {
			return body.String()
		}
	}
	t.Fatal("rule pagination did not terminate")
	return ""
}
func finishRules(t *testing.T) {
	t.Helper()
	call := func(_ string, a map[string]any) string {
		s, e := RulesTool(a["rules"].(string), false)
		if e != nil {
			t.Fatal(e)
		}
		return s
	}
	collectRules(t, call)
}
func ruleRequest(args map[string]any, sid string) map[string]any {
	p := map[string]any{"name": "ortg_overview", "arguments": args}
	if sid != "" {
		p["_meta"] = map[string]any{"threadId": sid}
	}
	return map[string]any{"method": "tools/call", "params": p}
}
func TestResumeRuleDelivery(t *testing.T) {
	for _, host := range []string{"claude", "codex"} {
		for _, legacy := range []string{"", fmt.Sprintf("%x", sha1.Sum([]byte(disciplineText())))} {
			t.Run(host+"/"+fmt.Sprint(len(legacy)), func(t *testing.T) {
				t.Setenv("ORTG_AGENT", host)
				root := updateRepo(t, "a.go")
				scanWithHeader()
				Update("a.go", updateLine("a.go", "neutral source"))
				seed := resumeRulesMCP(t, root)
				seed("ortg_overview", nil)
				_, saved := base.OverviewMark(root, sentMark(testMCPSession))
				var cache map[string]any
				json.Unmarshal([]byte(saved), &cache)
				cache["Rules"] = fmt.Sprintf("%x", sha1.Sum([]byte(disciplineText())))
				cache["Reviews"] = 2
				cache["Planned"] = map[string]bool{"a.go": true}
				cache["Solo"] = map[string]bool{"pending.go": true}
				cache["FutureField"] = map[string]int{"kept": 7}
				wire, _ := json.Marshal(cache)
				base.MarkOverview(root, sentMark(testMCPSession), string(wire))
				mark := "disc-" + sessionKey(testMCPSession)
				base.MarkOverview(root, mark, legacy)
				proofs := `{"Before":{},"Pending":{},"Final":{},"future":"neutral"}`
				for _, m := range []string{"writes", failedTestsMark(testMCPSession)} {
					base.MarkOverview(root, m, proofs)
				}
				source, _ := os.ReadFile(filepath.Join(root, "a.go"))
				mcp := resumeRulesMCP(t, root)
				hook := func() string {
					return resumeRulesHook(t, root, "session", map[string]any{"cwd": root, "session_id": testMCPSession, "source": "resume"})
				}
				for i := 0; i < 2; i++ {
					out := hook()
					if len(out) > 2048 || !strings.Contains(out, rulesNeeded) || strings.Contains(out, entryContractRule) {
						t.Fatal("hook must only request bounded MCP delivery", out)
					}
				}
				if _, value := base.OverviewMark(root, mark); value != legacy {
					t.Fatal("hook completed a legacy attempt")
				}
				// A real write gate sees the old loaded outline but must not consume its
				// existing plan or solo grants while the page protocol is incomplete.
				out := mcp("ortg_gate", map[string]any{"tool": "Edit", "path": filepath.Join(root, "a.go")})
				if !strings.Contains(out, deniedMark) || !strings.Contains(out, "rules=") {
					t.Fatal("write was allowed before rule completion", out)
				}
				delivered := collectRules(t, mcp)
				if delivered != rulePayload() {
					t.Fatal("page chain lost or changed rule bytes")
				}
				if !strings.Contains(delivered, "公开契约决定的比较标准") || !strings.Contains(delivered, "三条硬规则") || !strings.Contains(delivered, finalSourceRule) {
					t.Fatal("incomplete normative rule set")
				}
				if got := hook(); got != resumeNote {
					t.Fatal("completed rules repeated on resume", got)
				}
				if out := mcp("ortg_overview", map[string]any{"rules": ""}); out != rulesComplete {
					t.Fatal("completed context restarted without force")
				}
				if _, after := base.OverviewMark(root, sentMark(testMCPSession)); after != string(wire) {
					t.Fatal("rules changed Sent/permissions/unknown fields")
				}
				for _, m := range []string{"writes", failedTestsMark(testMCPSession)} {
					if _, v := base.OverviewMark(root, m); v != proofs {
						t.Fatal("rules changed unrelated proofs", m)
					}
				}
				if after, _ := os.ReadFile(filepath.Join(root, "a.go")); !bytes.Equal(source, after) {
					t.Fatal("rule transfer changed source")
				}
				short := mcp("ortg_overview", nil)
				if !strings.HasSuffix(short, overviewShortFooter) || strings.Contains(short, updateLine("a.go", "neutral source")) || strings.Contains(short, rulesNeeded) {
					t.Fatal("rule completion changed outline delivery", short)
				}
			})
		}
	}
}
func TestRuleCursorBoundaries(t *testing.T) {
	t.Setenv("ORTG_AGENT", "codex")
	useTestMCPSession(t)
	root := updateRepo(t, "a.go")
	scanWithHeader()
	Update("a.go", updateLine("a.go", "neutral source"))
	if out, err := Gate("Bash", "", "", `await tools.mcp__ortg__ortg_overview({rules:""})`, ""); err != nil || strings.Contains(out, deniedMark) {
		t.Fatal("code-mode rule fetch was gated", out, err)
	}
	// Code-mode calls to our own tools pass the hook even before any pages.
	if out, err := gateHook("mcp__plugin_ortg_ortg__ortg_overview", "", "", "", "", "neutral-use"); err != nil || out != "" {
		t.Fatal("rules fetch deadlocked its own gate", out, err)
	}
	mcp := resumeRulesMCP(t, root)
	first := mcp("ortg_overview", map[string]any{"rules": ""})
	body, cursor := rulePage(t, first)
	seen := len(body)
	mark := "rules-disc-" + sessionKey(testMCPSession)
	_, saved := base.OverviewMark(root, mark)
	if retry := mcp("ortg_overview", map[string]any{"rules": ""}); retry != first {
		t.Fatal("first-page retry changed the page or cursor")
	}
	if _, after := base.OverviewMark(root, mark); after != saved {
		t.Fatal("first-page retry changed progress")
	}
	// Native validation must reject an invented cursor, a different context,
	// mixed outline/rule requests and malformed optional values.
	for _, request := range []map[string]any{
		ruleRequest(map[string]any{"rules": strings.Repeat("0", 40)}, testMCPSession),
		ruleRequest(map[string]any{"rules": cursor}, "other-neutral-context"),
		ruleRequest(map[string]any{"rules": cursor, "force": true}, testMCPSession),
		ruleRequest(map[string]any{"rules": "", "module": "ST"}, testMCPSession),
		ruleRequest(map[string]any{"rules": nil}, testMCPSession),
	} {
		r := nativeMCP(t, root, request)[0]
		if !r.Result.IsError && len(r.Error) == 0 {
			t.Fatal("invalid rule request accepted", r)
		}
	}
	if _, after := base.OverviewMark(root, mark); after != saved {
		t.Fatal("rejection changed current progress")
	}
	// Last page emission still does not complete the receipt: stop immediately
	// before the final cursor and inspect state through the public hook.
	var state ruleDelivery
	json.Unmarshal([]byte(saved), &state)
	for seen < len(rulePayload()) {
		input := cursor
		out := mcp("ortg_overview", map[string]any{"rules": cursor})
		_, beforeRetry := base.OverviewMark(root, mark)
		if retry := mcp("ortg_overview", map[string]any{"rules": input}); retry != out {
			t.Fatal("continuation retry lost its exact page/cursor")
		}
		if _, after := base.OverviewMark(root, mark); after != beforeRetry {
			t.Fatal("continuation retry advanced twice")
		}
		var page string
		page, cursor = rulePage(t, out)
		seen += len(page)
		_, saved = base.OverviewMark(root, mark)
		json.Unmarshal([]byte(saved), &state)
	}
	if state.Done || mcpRulesKnown(root) {
		t.Fatal("last page was treated as a consumed receipt")
	}
	if out := mcp("ortg_overview", map[string]any{"rules": cursor}); out != rulesComplete {
		t.Fatal("final ack failed")
	}
	if out := mcp("ortg_overview", map[string]any{"rules": cursor}); out != rulesComplete {
		t.Fatal("same final ack was not idempotent")
	}
	forced := mcp("ortg_overview", map[string]any{"rules": "", "force": true})
	_, newCursor := rulePage(t, forced)
	if newCursor == cursor || mcpRulesKnown(root) {
		t.Fatal("force reused a completed chain")
	}
	// Changed rule generation and own compaction each reject outstanding cursors.
	for _, change := range []string{"generation", "compact"} {
		if change == "generation" {
			_, data := base.OverviewMark(root, mark)
			json.Unmarshal([]byte(data), &state)
			state.Fingerprint = "rules-mcp-v0:neutral"
			wire, _ := json.Marshal(state)
			base.MarkOverview(root, mark, string(wire))
		} else {
			resumeRulesHook(t, root, "session", map[string]any{"cwd": root, "session_id": testMCPSession, "source": "compact"})
		}
		r := nativeMCP(t, root, ruleRequest(map[string]any{"rules": newCursor}, testMCPSession))[0]
		if !r.Result.IsError {
			t.Fatal("old cursor survived", change)
		}
		page := mcp("ortg_overview", map[string]any{"rules": ""})
		_, newCursor = rulePage(t, page)
	}
	collectRules(t, mcp)

}
func TestResumeRuleContextBoundaries(t *testing.T) {
	t.Setenv("ORTG_AGENT", "codex")
	useTestMCPSession(t)
	root := updateRepo(t, "a.go")
	scanWithHeader()
	finishRules(t)
	parent := callSession
	callSession.ids = []string{"neutral-child"}
	if mcpRulesKnown(root) {
		t.Fatal("child inherited parent receipt")
	}
	finishRules(t)
	markReset(root, "", "neutral-child", "compact")
	if mcpRulesKnown(root) {
		t.Fatal("child compact retained its chain")
	}
	callSession = parent
	if !mcpRulesKnown(root) {
		t.Fatal("child compact reset parent")
	}
	other := updateRepo(t, "b.go")
	scanWithHeader()
	if mcpRulesKnown(other) {
		t.Fatal("receipt crossed repository roots")
	}
	callSession.ids = nil
	callSession.fromEnv = false
	if _, e := RulesTool("", false); e == nil {
		t.Fatal("unknown context completed a chain")
	}
}
func TestClaudeResumeRuleAfterClear(t *testing.T) {
	t.Setenv("ORTG_AGENT", "claude")
	root := updateRepo(t, "a.go")
	scanWithHeader()
	Update("a.go", updateLine("a.go", "neutral source"))
	mcp := resumeRulesMCP(t, root)
	collectRules(t, mcp)
	tr := filepath.Join(t.TempDir(), "neutral.jsonl")
	writeFile(t, tr, `{"type":"user"}`+"\n")
	payload := map[string]any{"cwd": root, "session_id": "neutral-new-sid", "source": "clear", "transcript_path": tr}
	resumeRulesHook(t, root, "session", payload)
	collectRules(t, mcp)
	payload["source"] = "resume"
	if out := resumeRulesHook(t, root, "session", payload); out != resumeNote {
		t.Fatal("old MCP SID/new hook SID did not meet at own transcript", out)
	}
	delete(payload, "transcript_path")
	if out := resumeRulesHook(t, root, "session", payload); out != resumeNote {
		t.Fatal("same context without transcript path forgot its completed rules", out)
	}
	// Startup without its path must invalidate that session's own prior transcript.
	resumeRulesHook(t, root, "session", map[string]any{"cwd": root, "session_id": "neutral-new-sid", "source": "startup"})
	if out := resumeRulesHook(t, root, "session", payload); !strings.Contains(out, rulesNeeded) {
		t.Fatal("old transcript overrode newer startup")
	}
	if body := collectRules(t, mcp); body != rulePayload() {
		t.Fatal("warm old-id server did not resend after startup")
	}
}

func TestRuleFinalDeclarationGate(t *testing.T) {
	t.Setenv("ORTG_AGENT", "codex")
	root := finalFixture(t)
	if _, err := Overview("", false); err != nil {
		t.Fatal(err)
	}
	if _, err := RulesTool("", true); err != nil {
		t.Fatal(err)
	}
	finalWrite(t, root, testMCPSession, "", "neutral-change", "value.go", "package sample\nconst Value = 2\n")
	if _, err := finalReview("", nil); err != nil {
		t.Fatal("inspection should remain available", err)
	}
	generation := finalGeneration(finalSaved(t, root, testMCPSession))
	_, before := base.OverviewMark(root, "writes")
	_, sent := base.OverviewMark(root, sentMark(testMCPSession))
	if out, err := Gate("Edit", filepath.Join(root, "value.go"), "", "", ""); err != nil || !strings.Contains(out, deniedMark) {
		t.Fatal("write escaped incomplete rule transfer", out, err)
	}
	if _, after := base.OverviewMark(root, sentMark(testMCPSession)); after != sent {
		t.Fatal("refused rule gate consumed permission state")
	}
	r := nativeMCP(t, root, map[string]any{"method": "tools/call", "params": map[string]any{"name": "ortg_review", "arguments": map[string]any{"final": true, "generation": generation, "report": []string{"Static source checked; behavior unverified."}}, "_meta": map[string]any{"threadId": testMCPSession}}})[0]
	if !r.Result.IsError || !strings.Contains(r.Result.Content[0].Text, rulesNeeded) {
		t.Fatal("final declaration bypassed incomplete rules", r)
	}
	if _, after := base.OverviewMark(root, "writes"); after != before {
		t.Fatal("rejected declaration changed Before/Pending/Final")
	}
	finishRules(t)
	if _, err := finalReview(generation, []string{"Static source checked; behavior unverified."}); err != nil {
		t.Fatal(err)
	}
}
