package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"ortg/base"
)

// TestDisabledChild is the process the disabled-repository test starts: the
// real `ortg mcp` or `ortg hook` entry, as the host would run it.
func TestDisabledChild(t *testing.T) {
	switch os.Getenv("ORTG_DISABLED_CHILD") {
	case "mcp":
		if err := runMCP(nil); err != nil {
			os.Exit(2)
		}
		os.Exit(0)
	case "":
		return
	default:
		if err := runHook([]string{"--event", os.Getenv("ORTG_DISABLED_CHILD")}); err != nil {
			os.Exit(2)
		}
		os.Exit(0)
	}
}

func disabledChild(t *testing.T, root, mode, stdin string) string {
	t.Helper()
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "-test.run=^TestDisabledChild$")
	cmd.Dir, cmd.Stdin = root, strings.NewReader(stdin)
	cmd.Env = append(os.Environ(), "ORTG_DISABLED_CHILD="+mode)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s child: %v\n%s", mode, err, out)
	}
	return string(out)
}

// An ortg.disable at the repository root turns ORTG off for good: the MCP
// server exits before the handshake, the hooks say nothing (Stop only the {}
// Codex requires), the gate lets everything through, the tools that still
// get called answer disabledReply, and no entry point creates or writes the
// table — not ortg_overview init=true, not the CLI scan, not an existing one.
func TestDisabledRepo(t *testing.T) {
	root := updateRepo(t, "a.go")
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	if _, err := Update("a.go", updateLine("a.go", "存储")); err != nil {
		t.Fatal(err)
	}
	if Disabled("") {
		t.Fatal("no ortg.disable yet")
	}
	writeFile(t, filepath.Join(root, disableName), "")
	table := filepath.Join(root, base.TableName)
	before, _ := os.ReadFile(table)
	bak, _ := os.ReadFile(table + ".bak")

	if !Disabled("") || !Disabled(filepath.Join(root, "sub")) {
		t.Fatal("ortg.disable at the root must disable the repository")
	}
	for _, f := range []func() (string, error){
		func() (string, error) { return OverviewTool("", false, false) },
		func() (string, error) { return OverviewTool("", false, true) },
		StatusTool,
	} {
		if got, err := f(); err != nil || got != disabledReply {
			t.Fatalf("tool reply = %q, %v", got, err)
		}
	}
	for name, f := range map[string]func() (string, error){
		"update":   func() (string, error) { return Update("a.go", updateLine("a.go", "改")) },
		"target":   func() (string, error) { return Target("a.go", "=", false, "") },
		"review":   func() (string, error) { return Review() },
		"align":    func() (string, error) { return Align("all", nil) },
		"header":   func() (string, error) { return Header(testHeader, nil, nil, nil, nil, nil, nil) },
		"overview": func() (string, error) { return Overview("", false) },
		"scan":     Scan,
		"status":   Status,
	} {
		if _, err := f(); !errors.Is(err, errDisabled) {
			t.Fatalf("%s must refuse with errDisabled: %v", name, err)
		}
	}
	for _, event := range []string{"session", "subagent", "prompt", "pre", "post", "compact", "stop"} {
		in := base.HookInput{Cwd: root, SessionID: "s", Source: "startup", ToolName: "Read", FilePath: filepath.Join(root, "a.go")}
		if ctx, msg := HookReplyFull(event, in); ctx != "" || msg != "" {
			t.Fatalf("%s hook must say nothing: %q %q", event, ctx, msg)
		}
	}
	if got, _ := Gate("Read", filepath.Join(root, "a.go"), "", "", ""); got != "" {
		t.Fatalf("gate must let everything through: %q", got)
	}
	if got, _ := Gate("Edit", filepath.Join(root, "a.go"), "", "", ""); got != "" {
		t.Fatalf("gate must let writes through: %q", got)
	}

	// The real processes the host starts.
	init := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}` + "\n"
	if out := disabledChild(t, root, "mcp", init); out != "" {
		t.Fatalf("the MCP server must exit without a word: %q", out)
	}
	payload := `{"hook_event_name":"%s","session_id":"s","cwd":"` + root + `","source":"startup"}`
	for event, host := range map[string]string{"session": "SessionStart", "prompt": "UserPromptSubmit", "post": "PostToolUse"} {
		if out := disabledChild(t, root, event, strings.Replace(payload, "%s", host, 1)); out != "" {
			t.Fatalf("%s hook must print nothing: %q", event, out)
		}
	}
	if out := disabledChild(t, root, "stop", strings.Replace(payload, "%s", "Stop", 1)); out != "{}" {
		t.Fatalf("Stop hook prints only {}: %q", out)
	}

	after, _ := os.ReadFile(table)
	bakAfter, _ := os.ReadFile(table + ".bak")
	if !bytes.Equal(before, after) || !bytes.Equal(bak, bakAfter) {
		t.Fatal("the existing table and its backup must stay byte for byte")
	}

	// Without a table, nothing creates one.
	fresh := updateRepo(t, "b.go")
	writeFile(t, filepath.Join(fresh, disableName), "")
	if got, _ := OverviewTool("", false, true); got != disabledReply {
		t.Fatalf("init must not enable ORTG: %q", got)
	}
	if _, err := Scan(); !errors.Is(err, errDisabled) {
		t.Fatalf("scan must refuse: %v", err)
	}
	HookReplyFull("session", base.HookInput{Cwd: fresh, SessionID: "s", Source: "startup"})
	if _, err := os.Stat(filepath.Join(fresh, base.TableName)); !os.IsNotExist(err) {
		t.Fatal("no entry point may create the table")
	}
}

// ortg.disable at the git top level counts too, even when the ortg root —
// where ortg.tsv is — sits below it.
func TestDisabledAtGitRoot(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	top := updateRepo(t)
	if out, err := exec.Command("git", "-C", top, "init", "-q").CombinedOutput(); err != nil {
		t.Skipf("git init: %v %s", err, out)
	}
	sub := filepath.Join(top, "pkg")
	writeFile(t, filepath.Join(sub, "a.go"), "package a")
	t.Chdir(sub)
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	if Disabled("") {
		t.Fatal("no ortg.disable yet")
	}
	writeFile(t, filepath.Join(top, disableName), "")
	if !Disabled("") {
		t.Fatal("ortg.disable at the git top level must disable the repository")
	}
}
