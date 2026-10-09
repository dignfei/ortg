package base

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestReadHookInputTimeoutAndBOM(t *testing.T) {
	pr, pw := io.Pipe()
	go func() {
		pw.Write([]byte("\uFEFF{\"tool_name\":\"Read\",\"tool_input\":{\"notebook_path\":\"/a.ipynb\"}}"))
	}()
	start := time.Now()
	in := ReadHookInput(pr, 300*time.Millisecond)
	if time.Since(start) > 2*time.Second {
		t.Fatal("hook read hung without EOF")
	}
	if in.ToolName != "Read" || in.FilePath != "/a.ipynb" {
		t.Fatalf("payload parsed wrong: %+v", in)
	}
	if got := ReadHookInput(strings.NewReader("garbage"), time.Second); got.ToolName != "" {
		t.Fatal("garbage must yield empty input")
	}
}

// Claude Code's interactive SessionStart names the model, [1m] included.
func TestReadHookInputModel(t *testing.T) {
	in := ReadHookInput(strings.NewReader(`{"hook_event_name":"SessionStart","source":"startup","model":"claude-opus-5-5[1m]"}`), time.Second)
	if in.Model != "claude-opus-5-5[1m]" || in.Source != "startup" {
		t.Fatalf("model not read: %+v", in)
	}
}

// Codex's window: model_context_window capped at the model's maximum, else
// the model's own window, times the share Codex uses; 0 when unreadable.
func TestCodexContextWindow(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	if got := CodexContextWindow(); got != 0 {
		t.Fatalf("no config: got %d, want 0", got)
	}
	os.WriteFile(filepath.Join(home, "models_cache.json"), []byte(`{"models":[{"slug":"m1","context_window":272000,"max_context_window":872000,"effective_context_window_percent":95},{"slug":"m2","context_window":400000}]}`), 0o644)
	for _, c := range []struct {
		config string
		want   int
	}{
		{"model = \"m1\"\n", 258400},
		{"model = \"m1\"\nmodel_context_window = 1000000\n[projects.\"/x\"]\nmodel_context_window = 5\n", 828400},
		{"model = \"m1\"\nmodel_context_window = 500_000 # raised\n", 475000},
		{"model = 'm2'\n", 400000},
		{"model = \"unknown\"\nmodel_context_window = 300000\n", 300000},
		{"[projects.\"/x\"]\nmodel = \"m1\"\n", 0},
	} {
		os.WriteFile(filepath.Join(home, "config.toml"), []byte(c.config), 0o644)
		if got := CodexContextWindow(); got != c.want {
			t.Fatalf("config %q: got %d, want %d", c.config, got, c.want)
		}
	}
}

func TestHostDetectionWithCodexCompatibilityVariables(t *testing.T) {
	keys := []string{"ORTG_AGENT", "PLUGIN_ROOT", "PLUGIN_DATA", "CLAUDE_PLUGIN_ROOT", "CLAUDE_PLUGIN_DATA"}
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{name: "default", want: "claude"},
		{name: "claude compatibility variables only", env: map[string]string{
			"CLAUDE_PLUGIN_ROOT": "/claude/plugin",
			"CLAUDE_PLUGIN_DATA": "/claude/data",
		}, want: "claude"},
		{name: "codex native variables", env: map[string]string{
			"PLUGIN_ROOT": "/codex/plugin",
			"PLUGIN_DATA": "/codex/data",
		}, want: "codex"},
		{name: "codex native and claude compatibility variables", env: map[string]string{
			"PLUGIN_ROOT":        "/codex/plugin",
			"PLUGIN_DATA":        "/codex/data",
			"CLAUDE_PLUGIN_ROOT": "/codex/plugin",
			"CLAUDE_PLUGIN_DATA": "/codex/data",
		}, want: "codex"},
		{name: "explicit codex override", env: map[string]string{
			"ORTG_AGENT":         "codex",
			"CLAUDE_PLUGIN_ROOT": "/claude/plugin",
		}, want: "codex"},
		{name: "explicit claude override", env: map[string]string{
			"ORTG_AGENT":  "claude",
			"PLUGIN_ROOT": "/codex/plugin",
		}, want: "claude"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, key := range keys {
				t.Setenv(key, "")
			}
			for key, value := range tt.env {
				t.Setenv(key, value)
			}
			if got := Host(); got != tt.want {
				t.Fatalf("Host() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestReadHookInputApplyPatchPaths(t *testing.T) {
	command := strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: src/a.go",
		"@@",
		"+*** Delete File: not-a-header.go",
		" *** Add File: also-not-a-header.go",
		"*** Add File: docs/file name.md\r",
		"*** Delete File: old.go",
		"*** Update File: src/a.go",
		"*** Add File:",
		"*** Update File: bad\x00name",
		"*** End Patch",
	}, "\n")
	raw, err := json.Marshal(map[string]any{
		"tool_name":  "apply_patch",
		"tool_input": map[string]string{"command": command},
	})
	if err != nil {
		t.Fatal(err)
	}
	in := ReadHookInput(bytes.NewReader(raw), time.Second)
	want := []string{"src/a.go", "docs/file name.md", "old.go"}
	if !reflect.DeepEqual(in.FilePaths, want) {
		t.Fatalf("FilePaths = %#v, want %#v", in.FilePaths, want)
	}
	if in.FilePath != want[0] {
		t.Fatalf("primary FilePath = %q, want %q", in.FilePath, want[0])
	}

	// A shell command containing patch-looking text is not an apply_patch
	// payload and must not be promoted to file paths.
	raw, err = json.Marshal(map[string]any{
		"tool_name":  "Bash",
		"tool_input": map[string]string{"command": command},
	})
	if err != nil {
		t.Fatal(err)
	}
	in = ReadHookInput(bytes.NewReader(raw), time.Second)
	if in.FilePath != "" || len(in.FilePaths) != 0 {
		t.Fatalf("non-apply_patch command produced paths: %+v", in)
	}
}

func TestWriteHookOutputEnvelopes(t *testing.T) {
	var b bytes.Buffer
	WriteHookOutput(&b, "claude", "SessionStart", "rules", "对齐 3")
	if !strings.Contains(b.String(), `"additionalContext":"rules"`) || !strings.Contains(b.String(), `"systemMessage":"对齐 3"`) {
		t.Fatalf("claude SessionStart must carry both the model context and the user-visible line: %s", b.String())
	}
	b.Reset()
	WriteHookOutput(&b, "claude", "PreToolUse", "x", "")
	if !strings.Contains(b.String(), `"hookEventName":"PreToolUse"`) || !strings.Contains(b.String(), `"additionalContext":"x"`) {
		t.Fatalf("claude envelope wrong: %s", b.String())
	}
	b.Reset()
	WriteHookOutput(&b, "codex", "SessionStart", "x", "")
	if !strings.Contains(b.String(), `"systemMessage"`) {
		t.Fatalf("codex needs systemMessage: %s", b.String())
	}
	b.Reset()
	WriteHookOutput(&b, "claude", "SessionStart", "", "")
	if b.Len() != 0 {
		t.Fatal("empty text must write nothing")
	}
}

// TestHookBinary spawns the real binary: a directory without ortg.tsv must
// stay silent and exit 0 for every event.
func TestHookBinary(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "ortg")
	build := exec.Command("go", "build", "-o", bin, "ortg")
	build.Dir = ".."
	if out, err := build.CombinedOutput(); err != nil {
		t.Skipf("build failed: %v %s", err, out)
	}
	empty := t.TempDir()
	for _, ev := range []string{"session", "subagent", "pre", "post"} {
		cmd := exec.Command(bin, "hook", "--event", ev)
		cmd.Dir = empty
		cmd.Env = append(os.Environ(), "ORTG_AGENT=claude")
		cmd.Stdin = strings.NewReader(`{"hook_event_name":"PreToolUse","tool_name":"Edit","tool_input":{"file_path":"` + filepath.Join(empty, "a.go") + `"},"cwd":"` + empty + `"}`)
		out, err := cmd.Output()
		if err != nil || len(out) != 0 {
			t.Fatalf("event %s in repo without table: err=%v out=%q", ev, err, out)
		}
	}
	// Stop must always answer with JSON: Codex rejects an empty reply.
	cmd := exec.Command(bin, "hook", "--event", "stop")
	cmd.Dir = empty
	cmd.Env = append(os.Environ(), "ORTG_AGENT=codex")
	cmd.Stdin = strings.NewReader(`{"hook_event_name":"Stop","stop_hook_active":false,"cwd":"` + empty + `"}`)
	if out, err := cmd.Output(); err != nil || string(out) != "{}" {
		t.Fatalf("stop in repo without table: err=%v out=%q", err, out)
	}
}

// Stop payloads carry stop_hook_active, Bash calls run_in_background; a Stop
// reply blocks with a reason or says {} on both hosts.
func TestStopHookIO(t *testing.T) {
	in := ReadHookInput(strings.NewReader(`{"hook_event_name":"Stop","stop_hook_active":true,"session_id":"s"}`), time.Second)
	if !in.StopHookActive || in.Event != "Stop" {
		t.Fatalf("stop_hook_active not read: %+v", in)
	}
	in = ReadHookInput(strings.NewReader(`{"hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"make test","run_in_background":true}}`), time.Second)
	if !in.Background || in.Command != "make test" {
		t.Fatalf("run_in_background not read: %+v", in)
	}
	for _, host := range []string{"claude", "codex"} {
		var b bytes.Buffer
		WriteHookOutput(&b, host, "Stop", "", "")
		if b.String() != "{}" {
			t.Fatalf("%s: no objection must be {}: %q", host, b.String())
		}
		b.Reset()
		WriteHookOutput(&b, host, "Stop", "wait", "")
		var m map[string]string
		if json.Unmarshal(b.Bytes(), &m) != nil || m["decision"] != "block" || m["reason"] != "wait" || len(m) != 2 {
			t.Fatalf("%s: block reply: %q", host, b.String())
		}
	}
}

// ProcessAlive finds a process running the words — as a command, or as a
// shell whose -c script holds them — with its working directory inside the
// root and started after the given moment; never this process or its
// ancestors, never a process elsewhere or an older one.
func TestProcessAlive(t *testing.T) {
	if _, err := os.Stat("/proc/self/cmdline"); err != nil {
		t.Skip("no /proc")
	}
	root := t.TempDir()
	marker := "ortg-alive-probe-" + strconv.Itoa(os.Getpid())
	since := time.Now()
	wrapped := exec.Command("sh", "-c", "sleep 30; : "+marker+" --all")
	wrapped.Dir = root
	direct := exec.Command("sleep", "31")
	direct.Dir = root
	elsewhere := exec.Command("sleep", "32")
	elsewhere.Dir = t.TempDir()
	for _, c := range []*exec.Cmd{wrapped, direct, elsewhere} {
		if err := c.Start(); err != nil {
			t.Skip(err)
		}
		defer c.Process.Kill()
	}
	time.Sleep(150 * time.Millisecond)
	if !ProcessAlive(root, []string{marker, "--all"}, since) {
		t.Fatal("a shell whose -c script runs the words")
	}
	if !ProcessAlive(root, []string{"sleep", "31"}, since) {
		t.Fatal("the command itself")
	}
	if ProcessAlive(root, []string{"sleep", "32"}, since) {
		t.Fatal("a process outside the root")
	}
	if ProcessAlive(root, []string{"sleep", "31"}, since.Add(time.Minute)) {
		t.Fatal("a process started before the moment")
	}
	if ProcessAlive(root, nil, since) {
		t.Fatal("no words match nothing")
	}
	wrapped.Process.Kill()
	wrapped.Wait()
	if ProcessAlive(root, []string{marker, "--all"}, since) {
		t.Fatal("a finished process")
	}
}

// The pre-edit hook's call-graph answer serves the next post-edit hook once:
// an empty answer is still an answer, a failed (nil) one is not kept.
func TestSaveTakeRelations(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	SaveRelations("s", "a.go", []Relation{{Path: "b.go"}, {Path: "c.go", Calls: true}})
	if rs, ok := TakeRelations("s", "a.go"); !ok || len(rs) != 2 || rs[1] != (Relation{Path: "c.go", Calls: true}) {
		t.Fatalf("take = %v %v", rs, ok)
	}
	if _, ok := TakeRelations("s", "a.go"); ok {
		t.Fatal("an answer serves one edit only")
	}
	SaveRelations("s", "a.go", []Relation{})
	if rs, ok := TakeRelations("s", "a.go"); !ok || len(rs) != 0 {
		t.Fatalf("empty answer must be kept: %v %v", rs, ok)
	}
	SaveRelations("s", "a.go", nil)
	if _, ok := TakeRelations("s", "a.go"); ok {
		t.Fatal("a failed query must not be kept")
	}
}

// A PostToolUse payload's tool_response becomes ToolOutput: Claude Code's
// Bash object (stdout, then stderr), Codex's plain string, an object's output
// field; a long output keeps its tail, where a test summary sits.
func TestReadHookInputToolOutput(t *testing.T) {
	cases := map[string]string{
		`{"tool_name":"Bash","tool_response":{"stdout":"3 skipped in 0.1s","stderr":"warn"}}`: "3 skipped in 0.1s\nwarn",
		`{"tool_name":"Bash","tool_response":"1 passed in 0.2s"}`:                             "1 passed in 0.2s",
		`{"tool_name":"Bash","tool_response":{"output":"ok"}}`:                                "ok",
		`{"tool_name":"Bash"}`: "",
	}
	for payload, want := range cases {
		if got := ReadHookInput(strings.NewReader(payload), time.Second).ToolOutput; got != want {
			t.Errorf("%s: ToolOutput %q, want %q", payload, got, want)
		}
	}
	long := strings.Repeat("x", toolOutputMax) + "TAIL"
	b, _ := json.Marshal(map[string]any{"tool_response": map[string]string{"stdout": long}})
	if got := ReadHookInput(strings.NewReader(string(b)), time.Second).ToolOutput; len(got) != toolOutputMax || !strings.HasSuffix(got, "TAIL") {
		t.Errorf("a long output keeps its last %d bytes: len %d", toolOutputMax, len(got))
	}
}

// Color codes are removed from ToolOutput: a test runner forced to color its
// output wraps every summary word in them.
func TestToolOutputStripsColor(t *testing.T) {
	payload := `{"tool_name":"Bash","tool_response":{"stdout":"\u001b[33m\u001b[33m\u001b[1m11 skipped\u001b[0m\u001b[33m in 0.05s\u001b[0m\u001b[0m"}}`
	if got := ReadHookInput(strings.NewReader(payload), time.Second).ToolOutput; got != "11 skipped in 0.05s" {
		t.Fatalf("colors stripped: %q", got)
	}
}
