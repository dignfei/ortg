package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestResumeRuleDelivery(t *testing.T) {
	for _, host := range []string{"claude", "codex"} {
		for _, first := range []string{"hook", "overview"} {
			for _, previous := range []string{"missing", "changed", "current"} {
				t.Run(host+"/"+first+"/"+previous, func(t *testing.T) {
					t.Setenv("ORTG_AGENT", host)
					root := updateRepo(t, "a.go")
					if _, err := scanWithHeader(); err != nil {
						t.Fatal(err)
					}
					outline := updateLine("a.go", "neutral fixture")
					if _, err := Update("a.go", outline); err != nil {
						t.Fatal(err)
					}
					// The earlier context really received an outline. Reproduce the
					// legacy receipt and saved rule identities without resetting it.
					seed := resumeRulesMCP(t, root)
					seed("ortg_overview", nil)
					_, saved := base.OverviewMark(root, sentMark(testMCPSession))
					var cache map[string]any
					if err := json.Unmarshal([]byte(saved), &cache); err != nil {
						t.Fatal(err)
					}
					if previous == "missing" {
						delete(cache, "Rules")
					} else if previous == "changed" {
						cache["Rules"] = "previous neutral rules"
					}
					cache["Reviews"], cache["LastReview"] = 2, "neutral-plan"
					cache["Planned"], cache["Solo"] = map[string]bool{"a.go": true}, map[string]bool{"unsubmitted.go": true}
					cache["Denied"], cache["FutureField"] = true, map[string]any{"preserved": 7}
					wire, _ := json.Marshal(cache)
					base.MarkOverview(root, sentMark(testMCPSession), string(wire))
					base.MarkOverview(root, "disc-"+sessionKey(testMCPSession), "")
					base.InjectOnce(testMCPSession, "discipline")
					marks := []string{"writes", failedTestsMark(testMCPSession)}
					for _, mark := range marks {
						base.MarkOverview(root, mark, `{"Before":{},"Pending":{},"Final":{},"future":"neutral"}`)
					}
					mcp := resumeRulesMCP(t, root)
					mcp("ortg_gate", map[string]any{"tool": "Read", "path": filepath.Join(root, "a.go")})
					check := func(stage, text string, full bool) {
						t.Helper()
						want := 0
						if full {
							want = 1
						}
						if strings.Count(text, "纲要行纪律：") != want || strings.Count(text, "任务依赖闭环：") != want {
							t.Fatalf("%s full=%v:\n%s", stage, full, text)
						}
					}
					get := func() string { return mcp("ortg_overview", map[string]any{"module": "ST"}) }
					payload := map[string]any{"cwd": root, "session_id": testMCPSession, "source": "resume"}
					resume := func(wantFull bool) {
						t.Helper()
						_, before := base.OverviewMark(root, sentMark(testMCPSession))
						text := resumeRulesHook(t, root, "session", payload)
						check("resume", text, wantFull)
						if !wantFull && text != resumeNote || wantFull && !strings.HasPrefix(text, resumeRulesNote) {
							t.Fatalf("resume note: %s", text)
						}
						_, after := base.OverviewMark(root, sentMark(testMCPSession))
						if before != after {
							t.Fatal("resume rewrote saved outline/permissions/unknown fields")
						}
						for _, mark := range marks {
							if _, value := base.OverviewMark(root, mark); value != `{"Before":{},"Pending":{},"Final":{},"future":"neutral"}` {
								t.Fatal("resume changed unrelated state", mark)
							}
						}
					}
					if first == "overview" {
						check("overview first", get(), previous != "current")
						resume(false)
					} else {
						resume(previous != "current")
					}
					resume(false)
					short := get()
					check("warm MCP after hook", short, false)
					if strings.Contains(short, outline) || !strings.HasSuffix(short, overviewShortFooter) {
						t.Fatal("rule delivery invalidated the outline cache")
					}
					writeFile(t, filepath.Join(root, "a.go"), "package fixture // edited")
					check("edit after resume", resumeRulesHook(t, root, "post", map[string]any{
						"cwd": root, "session_id": testMCPSession, "tool_name": "Edit", "tool_input": map[string]any{"file_path": filepath.Join(root, "a.go")},
					}), false)
				})
			}
		}
	}
}

func TestResumeRuleContextBoundaries(t *testing.T) {
	for _, host := range []string{"claude", "codex"} {
		t.Run(host, func(t *testing.T) {
			t.Setenv("ORTG_AGENT", host)
			root := updateRepo(t, "a.go")
			scanWithHeader()
			Update("a.go", updateLine("a.go", "neutral fixture"))
			in := base.HookInput{Cwd: root, SessionID: "neutral-parent"}
			if got := disciplineFor(root, in); got != disciplineText() {
				t.Fatal("initial context did not get current rules")
			}
			child := in
			child.AgentID = "neutral-child"
			if got := disciplineFor(root, child); got != disciplineText() || disciplineFor(root, in) != disciplineSeen {
				t.Fatal("parent and child rule receipts crossed")
			}
			for _, source := range []string{"clear", "compact", "startup"} {
				HookReply("session", base.HookInput{Cwd: root, SessionID: in.SessionID, AgentID: child.AgentID, Source: source})
				if disciplineFor(root, child) != disciplineText() || disciplineFor(root, in) != disciplineSeen {
					t.Fatal("child reset changed the parent", source)
				}
			}
			if host == "claude" {
				tr := filepath.Join(t.TempDir(), "parent.jsonl")
				writeFile(t, tr, `{"type":"user"}`+"\n")
				in.TranscriptPath, child.TranscriptPath = tr, tr
				HookReply("session", base.HookInput{Cwd: root, SessionID: in.SessionID, Source: "compact", TranscriptPath: tr})
				writeFile(t, base.SubagentTranscript(tr, child.AgentID), fmt.Sprintf("{\"type\":\"system\",\"subtype\":\"compact_boundary\",\"timestamp\":%q}\n", time.Now().UTC().Format(time.RFC3339Nano)))
				if disciplineFor(root, in) != disciplineSeen || disciplineFor(root, child) != disciplineText() {
					t.Fatal("ambiguous compact did not use each context's own transcript")
				}
			}
			unknown := base.HookInput{Cwd: root}
			if disciplineFor(root, unknown) != disciplineText() || disciplineFor(root, unknown) != disciplineText() {
				t.Fatal("unknown session must remain conservative")
			}
			other := updateRepo(t, "b.go")
			scanWithHeader()
			in.Cwd = other
			if disciplineFor(other, in) != disciplineText() {
				t.Fatal("rule receipts crossed repository roots")
			}
		})
	}
}

func TestClaudeResumeRuleAfterClear(t *testing.T) {
	t.Setenv("ORTG_AGENT", "claude")
	root := updateRepo(t, "a.go")
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	if _, err := Update("a.go", updateLine("a.go", "neutral fixture")); err != nil {
		t.Fatal(err)
	}
	mcp := resumeRulesMCP(t, root) // Claude environment id, no thread metadata
	if text := mcp("ortg_overview", nil); !strings.Contains(text, disciplineText()) {
		t.Fatal("initial context did not receive rules")
	}
	tr := filepath.Join(t.TempDir(), "after-clear.jsonl")
	writeFile(t, tr, `{"type":"user"}`+"\n")
	payload := map[string]any{"cwd": root, "session_id": "neutral-after-clear", "source": "clear", "transcript_path": tr}
	resumeRulesHook(t, root, "session", payload)
	if text := mcp("ortg_overview", nil); !strings.Contains(text, disciplineText()) {
		t.Fatal("warm Claude server did not recognize /clear with its old environment id")
	}
	payload["source"] = "resume"
	if text := resumeRulesHook(t, root, "session", payload); text != resumeNote {
		t.Fatalf("resume duplicated the actual post-clear delivery: %s", text)
	}
	// An older transcript receipt cannot override a newer startup for the
	// same session even when that startup omits its transcript path.
	resumeRulesHook(t, root, "session", map[string]any{"cwd": root, "session_id": "neutral-after-clear", "source": "startup"})
	if text := resumeRulesHook(t, root, "session", payload); !strings.Contains(text, disciplineText()) {
		t.Fatal("an old transcript receipt bypassed a newer session reset")
	}
	if text := mcp("ortg_overview", nil); strings.Contains(text, disciplineText()) {
		t.Fatal("warm old-id server duplicated the hook delivery")
	}
}
