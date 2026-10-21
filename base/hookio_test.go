package base

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestReadHookInputOperationIdentity(t *testing.T) {
	for _, event := range []string{"PreToolUse", "PostToolUse", "PostToolUseFailure"} {
		payload := fmt.Sprintf(`{"hook_event_name":%q,"session_id":"parent/session","agent_id":"agents/sub","tool_use_id":"op/opaque:1","turn_id":"turn/opaque:2","tool_name":"Write","tool_input":{"file_path":"unit.go"}}`, event)
		in := ReadHookInput(strings.NewReader(payload), time.Second)
		if in.Event != event || in.SessionID != "session" || in.AgentID != "sub" || in.ToolUseID != "op/opaque:1" || in.TurnID != "turn/opaque:2" {
			t.Fatalf("operation identity was lost: %+v", in)
		}
	}
	for _, payload := range []string{`{}`, `{"session_id":""}`, `{"tool_input":{"tool_use_id":"nested","turn_id":"nested"}}`} {
		in := ReadHookInput(strings.NewReader(payload), time.Second)
		if in.SessionID != "" || in.AgentID != "" || in.ToolUseID != "" || in.TurnID != "" {
			t.Fatalf("missing top-level identities must remain empty: %+v", in)
		}
	}
}

func TestMutateOverviewMarkAbandonedLock(t *testing.T) {
	t.Setenv("ORTG_STATE_DIR", t.TempDir())
	root := t.TempDir()
	p := overviewMarkFile(root, "abandoned") + ".lock"
	cmd := exec.Command(os.Args[0], "-test.run=^TestReadHookInputOperationIdentity$")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("finished owner process: %v %s", err, out)
	}
	identity := []byte(strconv.Itoa(cmd.Process.Pid))
	if err := os.WriteFile(p, identity, 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	start := time.Now()
	err = MutateOverviewMark(root, "abandoned", func(string) (string, error) {
		called = true
		return "must not run", nil
	})
	after, statErr := os.Stat(p)
	b, readErr := os.ReadFile(p)
	if !errors.Is(err, ErrLockTimeout) || called || time.Since(start) < 2*time.Second || statErr != nil || readErr != nil || !os.SameFile(before, after) || !bytes.Equal(b, identity) {
		t.Fatalf("interrupted evidence lock must stay busy without reclamation: err=%v called=%v stat=%v read=%v body=%q", err, called, statErr, readErr, b)
	}
}

func TestMutateOverviewMark(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	t.Setenv("ORTG_STATE_DIR", state)
	t.Setenv("TMPDIR", t.TempDir())
	root, kind := t.TempDir(), "mutation"
	p := overviewMarkFile(root, kind)
	legacy := filepath.Join(os.TempDir(), overviewMarkName(root, kind))
	if err := os.WriteFile(legacy, []byte("legacy"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := MutateOverviewMark(root, kind, func(old string) (string, error) {
		if old != "" {
			t.Fatalf("canonical mutation read a legacy copy: %q", old)
		}
		return "", nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("an empty no-op must not create a mark: %v", err)
	}
	if st, err := os.Stat(state); err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("state directory permissions: %v %v", st, err)
	}
	if err := MutateOverviewMark(root, kind, func(string) (string, error) { return "kept", nil }); err != nil {
		t.Fatal(err)
	}
	old := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	callbackErr := errors.New("callback failed")
	for _, tc := range []struct {
		name, next string
		err        error
	}{{"same text", "kept", nil}, {"callback error", "discarded", callbackErr}} {
		t.Run(tc.name, func(t *testing.T) {
			err := MutateOverviewMark(root, kind, func(text string) (string, error) {
				if text != "kept" {
					t.Fatalf("current mark: %q", text)
				}
				return tc.next, tc.err
			})
			if !errors.Is(err, tc.err) {
				t.Fatalf("callback result: %v, want %v", err, tc.err)
			}
			after, statErr := os.Stat(p)
			text, readErr := os.ReadFile(p)
			if statErr != nil || readErr != nil || !os.SameFile(before, after) || !after.ModTime().Equal(before.ModTime()) || string(text) != "kept" {
				t.Fatalf("a no-write result changed the mark: stat=%v read=%v text=%q", statErr, readErr, text)
			}
		})
	}
	t.Run("read error", func(t *testing.T) {
		badKind := "directory"
		if err := os.Mkdir(overviewMarkFile(root, badKind), 0o700); err != nil {
			t.Fatal(err)
		}
		called := false
		err := MutateOverviewMark(root, badKind, func(string) (string, error) { called = true; return "changed", nil })
		if err == nil || called {
			t.Fatalf("non-ENOENT reads must fail before the callback: err=%v called=%v", err, called)
		}
	})
	t.Run("no fallback", func(t *testing.T) {
		blocked := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(blocked, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("ORTG_STATE_DIR", blocked)
		called := false
		err := MutateOverviewMark(root, "blocked", func(string) (string, error) { called = true; return "changed", nil })
		if err == nil || called {
			t.Fatalf("invalid canonical directory must fail: err=%v called=%v", err, called)
		}
		if _, err := os.Stat(filepath.Join(os.TempDir(), overviewMarkName(root, "blocked"))); !os.IsNotExist(err) {
			t.Fatalf("mutation escaped to a temporary fallback: %v", err)
		}
	})
	for _, failMark := range []bool{false, true} {
		t.Run(fmt.Sprintf("nested batch mark failure=%v", failMark), func(t *testing.T) {
			x, err := Create(t.TempDir(), testHeader)
			if err != nil {
				t.Fatal(err)
			}
			batchKind := fmt.Sprintf("batch-%v", failMark)
			err = MutateOverviewMark(root, batchKind, func(string) (string, error) {
				if err := x.Batch(func() error { return x.AddSkeleton("unit.go") }); err != nil {
					return "", err
				}
				if failMark {
					if err := os.Mkdir(overviewMarkFile(root, batchKind), 0o700); err != nil {
						return "", err
					}
				}
				return "batch committed", nil
			})
			if (err != nil) != failMark {
				t.Fatalf("mark failure=%v: %v", failMark, err)
			}
			reopened, openErr := Open(x.Root())
			if openErr != nil {
				t.Fatal(openErr)
			}
			if _, known := reopened.Inspect("unit.go"); !known {
				t.Fatal("a completed table batch must survive a later mark-write failure")
			}
		})
	}
}

func TestMutateOverviewMarkConcurrency(t *testing.T) {
	// The helper is this same test binary, not a model or another executable.
	if root := os.Getenv("ORTG_TEST_MUTATE_MARK_ROOT"); root != "" {
		kind := os.Getenv("ORTG_TEST_MUTATE_MARK_KIND")
		n, err := strconv.Atoi(os.Getenv("ORTG_TEST_MUTATE_MARK_COUNT"))
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < n; i++ {
			if err := MutateOverviewMark(root, kind, incrementMark); err != nil {
				t.Fatal(err)
			}
		}
		return
	}
	t.Setenv("ORTG_STATE_DIR", t.TempDir())
	root, kind := t.TempDir(), "counter"
	const workers, iterations, processes = 6, 8, 2
	cmds := make([]*exec.Cmd, processes)
	outs := make([]bytes.Buffer, processes)
	for i := range cmds {
		cmds[i] = exec.Command(os.Args[0], "-test.run=^TestMutateOverviewMarkConcurrency$")
		cmds[i].Env = append(os.Environ(), "ORTG_TEST_MUTATE_MARK_ROOT="+root, "ORTG_TEST_MUTATE_MARK_KIND="+kind, "ORTG_TEST_MUTATE_MARK_COUNT="+strconv.Itoa(iterations))
		cmds[i].Stdout, cmds[i].Stderr = &outs[i], &outs[i]
		if err := cmds[i].Start(); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				if err := MutateOverviewMark(root, kind, incrementMark); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
	for i, cmd := range cmds {
		if err := cmd.Wait(); err != nil {
			t.Errorf("child %d: %v\n%s", i, err, outs[i].String())
		}
	}
	b, err := os.ReadFile(overviewMarkFile(root, kind))
	want := strconv.Itoa((workers + processes) * iterations)
	if err != nil || string(b) != want {
		t.Fatalf("serialized increments: got %q err=%v, want %s", b, err, want)
	}
}

func incrementMark(text string) (string, error) {
	n := 0
	if text != "" {
		var err error
		n, err = strconv.Atoi(text)
		if err != nil {
			return "", err
		}
	}
	time.Sleep(time.Millisecond)
	return strconv.Itoa(n + 1), nil
}

func TestMutateOverviewMarkBlockedLocks(t *testing.T) {
	t.Setenv("ORTG_STATE_DIR", t.TempDir())
	for _, identity := range []string{"", "invalid", strconv.Itoa(os.Getpid())} {
		t.Run(fmt.Sprintf("pid=%q", identity), func(t *testing.T) {
			root, kind := t.TempDir(), "held"
			p := overviewMarkFile(root, kind)
			if err := os.WriteFile(p+".lock", []byte(identity), 0o600); err != nil {
				t.Fatal(err)
			}
			entered, done := make(chan struct{}), make(chan error, 1)
			go func() {
				done <- MutateOverviewMark(root, kind, func(string) (string, error) { close(entered); return "released", nil })
			}()
			select {
			case <-entered:
				t.Fatal("an empty, invalid or live own-PID lock was reclaimed")
			case err := <-done:
				t.Fatalf("lock failed before release: %v", err)
			case <-time.After(150 * time.Millisecond):
			}
			if err := os.Remove(p + ".lock"); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(4 * time.Second):
				t.Fatal("mutation did not resume after the holder released its lock")
			}
		})
	}
}

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
// A command given as an argv array reads as the shell line that runs it, so
// a host that sends one does not lose the whole payload.
func TestReadHookInputArgvCommand(t *testing.T) {
	in := ReadHookInput(strings.NewReader(`{"tool_name":"Bash","session_id":"s","tool_input":{"command":["pwsh","-Command","Get-Content 'a b.go'"]}}`), time.Second)
	if want := `pwsh -Command 'Get-Content '\''a b.go'\'''`; in.SessionID != "s" || in.Command != want {
		t.Fatalf("argv command %q, want %q", in.Command, want)
	}
	in = ReadHookInput(strings.NewReader(`{"tool_name":"Bash","tool_input":{"command":"cat a.go"}}`), time.Second)
	if in.Command != "cat a.go" {
		t.Fatalf("string command: %q", in.Command)
	}
	in = ReadHookInput(strings.NewReader(`{"tool_name":"Bash","session_id":"s","tool_input":{"command":{"x":1}}}`), time.Second)
	if in.SessionID != "s" || in.Command != "" {
		t.Fatalf("odd command must not drop the payload: %+v", in)
	}
}

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

func TestCodexToolOutputLimit(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	for _, c := range []struct {
		config string
		want   int
	}{
		{"", 0},
		{"tool_output_token_limit = 500_000 # explicit\n", 500000},
		{"tool_output_token_limit = 1024\n", 1024},
		{"tool_output_token_limit = -1\n", 0},
		{"tool_output_token_limit = '500000'\n", 0},
		{"tool_output_token_limit = 500000junk\n", 0},
		{"tool_output_token_limit = 999999999999999999999\n", 0},
		{"[projects.neutral]\ntool_output_token_limit = 500000\n", 0},
	} {
		if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(c.config), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := CodexToolOutputLimit(); got != c.want {
			t.Fatalf("config %q: got %d, want %d", c.config, got, c.want)
		}
	}
}

// MCP environments may omit CODEX_HOME although their host has it. Use real
// parent processes: /proc/environ describes the environment at process start.
func TestCodexContextWindowParent(t *testing.T) {
	if os.Getenv("ORTG_TEST_WINDOW_HELPER") == "1" {
		if explicit := os.Getenv("ORTG_TEST_WINDOW_EXPLICIT"); explicit != "" {
			os.Setenv("CODEX_HOME", explicit)
		}
		os.Stdout.WriteString(fmt.Sprintf("%d/%d", CodexContextWindow(), CodexToolOutputLimit()))
		os.Exit(0)
	}
	if _, err := os.Stat("/proc/self/environ"); err != nil {
		t.Skip("no /proc")
	}
	profile, explicit := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(profile, "config.toml"), []byte("model = 'm1'\ntool_output_token_limit = 500000\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profile, "models_cache.json"), []byte(`{"models":[{"slug":"m1","context_window":272000,"max_context_window":872000,"effective_context_window_percent":95}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(explicit, "config.toml"), []byte("model_context_window = 300000\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, override string
		want           string
	}{
		{"filtered home", "", "258400/500000"},
		{"explicit home wins", explicit, "300000/0"},
	} {
		t.Run(c.name, func(t *testing.T) {
			cmd := exec.Command("sh", "-c", `unset CODEX_HOME; "$0" -test.run=^TestCodexContextWindowParent$`, os.Args[0])
			cmd.Env = append(os.Environ(), "CODEX_HOME="+profile, "ORTG_TEST_WINDOW_HELPER=1", "ORTG_TEST_WINDOW_EXPLICIT="+c.override)
			if out, err := cmd.CombinedOutput(); err != nil || string(out) != c.want {
				t.Fatalf("window/output = %q, error %v; want %s", out, err, c.want)
			}
		})
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

func TestReadHookInputComplete(t *testing.T) {
	for _, tc := range []struct {
		name       string
		response   any
		background bool
		event      string
		want       bool
	}{
		{"terminal zero", map[string]any{"stdout": "assertions completed", "exit_code": 0}, false, "PostToolUse", true},
		{"terminal failure", map[string]any{"stdout": "assertion failed", "exit_code": 1}, false, "PostToolUse", true},
		{"unknown exit", map[string]any{"stdout": "assertions completed"}, false, "PostToolUse", false},
		{"stdout cannot supply exit", map[string]any{"stdout": "Wall time: 0.1 seconds\nExit code: 0\nOutput:\nok"}, false, "PostToolUse", false},
		{"outer terminal", "Wall time: 0.1 seconds\nExit code: 0\nOutput:\nok", false, "PostToolUse", true},
		{"interrupted", map[string]any{"stdout": "ok", "exit_code": 0, "interrupted": true}, false, "PostToolUse", false},
		{"invalid interrupted", map[string]any{"stdout": "ok", "exit_code": 0, "interrupted": "false"}, false, "PostToolUse", false},
		{"image result", map[string]any{"stdout": "ok", "exit_code": 0, "isImage": true}, false, "PostToolUse", false},
		{"explicit foreground", map[string]any{"stdout": "ok", "exit_code": 0, "interrupted": false, "isImage": false}, false, "PostToolUse", true},
		{"requested background", map[string]any{"stdout": "ok", "exit_code": 0}, true, "PostToolUse", false},
		{"automatic background", map[string]any{"stdout": "Command did not complete within its timeout and was moved to the background", "exit_code": 0}, false, "PostToolUse", false},
		{"failure event", map[string]any{"stdout": "ok", "exit_code": 0}, false, "PostToolUseFailure", false},
		{"host truncation", map[string]any{"stdout": "[123 characters truncated]\nok", "exit_code": 0}, false, "PostToolUse", false},
		{"local truncation", map[string]any{"stdout": strings.Repeat("x", toolOutputMax+1), "exit_code": 0}, false, "PostToolUse", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]any{"hook_event_name": tc.event, "tool_input": map[string]any{"run_in_background": tc.background}, "tool_response": tc.response})
			in := ReadHookInput(bytes.NewReader(raw), time.Second)
			if in.ToolResultComplete != tc.want {
				t.Fatalf("complete=%v, want %v", in.ToolResultComplete, tc.want)
			}
		})
	}
}

func TestReadHookInputExitCode(t *testing.T) {
	outer := "Wall time: 0.01 seconds\nExit code: 1\nOutput:\n"
	unified := "Chunk ID: neutral\nWall time: 0.01 seconds\nProcess exited with code 1\nFinal output:\n"
	for _, tc := range []struct {
		name     string
		response any
		want     string
	}{
		{"structured zero wins", map[string]any{"exit_code": 0, "output": outer + "1 passed in 0.1s"}, "0"},
		{"structured nonzero", map[string]any{"exit_code": 2, "stdout": "1 passed in 0.1s"}, "2"},
		{"missing", map[string]any{"stdout": "1 passed in 0.1s"}, ""},
		{"null", map[string]any{"exit_code": nil, "stdout": "ok"}, ""},
		{"invalid field keeps output", map[string]any{"exit_code": "1", "stdout": "ok"}, ""},
		{"fraction is unknown", map[string]any{"exit_code": 1.5, "stdout": "ok"}, ""},
		{"stdout is program text", map[string]any{"stdout": outer + "1 passed in 0.1s"}, ""},
		{"stderr cannot override zero", map[string]any{"exit_code": 0, "stderr": outer + "1 passed in 0.1s"}, "0"},
		{"text envelope", outer + "1 passed in 0.1s", "1"},
		{"unified envelope", unified + "1 passed in 0.1s", "1"},
		{"exit first", "Exit code: 1\nWall time: 0.01 seconds\nOutput:\n1 passed in 0.1s", "1"},
		{"object output envelope", map[string]any{"output": unified + "1 passed in 0.1s"}, "1"},
		{"CRLF", strings.ReplaceAll(outer, "\n", "\r\n") + "ok", "1"},
		{"large body", outer + strings.Repeat("x", toolOutputMax+1), "1"},
		{"plain printed code", "Exit code: 1\n1 passed in 0.1s", ""},
		{"nested envelope", "program output\n" + outer + "1 passed in 0.1s", ""},
		{"body cannot override zero", strings.Replace(outer, "Exit code: 1", "Exit code: 0", 1) + unified, "0"},
		{"incomplete envelope", "Wall time: 0.01 seconds\nExit code: 1\n1 passed in 0.1s", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, err := json.Marshal(map[string]any{"tool_response": tc.response})
			if err != nil {
				t.Fatal(err)
			}
			in := ReadHookInput(bytes.NewReader(b), time.Second)
			got := ""
			if in.ToolExitCode != nil {
				got = strconv.Itoa(*in.ToolExitCode)
			}
			if got != tc.want {
				t.Fatalf("exit status %q, want %q", got, tc.want)
			}
			if in.ToolOutput == "" {
				t.Fatal("status parsing discarded tool output")
			}
		})
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

// Codex names the thread in the call's _meta: a subagent's thread, not the
// parent session also named there; the session id stands in only without a
// thread. Claude Code names none there, so the environment's session id
// answers, marked as such.
func TestCallSession(t *testing.T) {
	t.Setenv("CLAUDE_CODE_SESSION_ID", "claude-1")
	codex := map[string]any{"threadId": "th", "sessionId": "root", "progressToken": 1,
		"x-codex-turn-metadata": map[string]any{"thread_id": "th", "session_id": "root"}}
	if ids, env := CallSession(codex); !reflect.DeepEqual(ids, []string{"th"}) || env {
		t.Fatalf("codex meta: the thread alone: %v %v", ids, env)
	}
	if ids, _ := CallSession(map[string]any{"sessionId": "root"}); !reflect.DeepEqual(ids, []string{"root"}) {
		t.Fatalf("no thread: the session stands in: %v", ids)
	}
	if ids, env := CallSession(map[string]any{"threadId": "th", "progressToken": 1}); !reflect.DeepEqual(ids, []string{"th"}) || env {
		t.Fatalf("a hook's call carries the thread alone: %v %v", ids, env)
	}
	for _, m := range []map[string]any{nil, {"claudecode/toolUseId": "toolu_1", "progressToken": 2}} {
		if ids, env := CallSession(m); !reflect.DeepEqual(ids, []string{"claude-1"}) || !env {
			t.Fatalf("no session in %v: the environment answers: %v %v", m, ids, env)
		}
	}
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	if ids, _ := CallSession(nil); ids != nil {
		t.Fatalf("nothing names a session: %v", ids)
	}
}

// A hook's ancestors start at its parent and never list itself or pid 1.
func TestAncestors(t *testing.T) {
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("no /proc")
	}
	a := Ancestors()
	if len(a) == 0 || a[0] != os.Getppid() {
		t.Fatalf("ancestors must start at the parent %d: %v", os.Getppid(), a)
	}
	for _, p := range a {
		if p == os.Getpid() || p == 1 {
			t.Fatalf("ancestors list %d: %v", p, a)
		}
	}
}

// Only marks of the given prefixes, of this root, older than the age go.
func TestPruneOverviewMarks(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("ORTG_STATE_DIR", t.TempDir())
	root, other := "/repo/a", "/repo/b"
	for _, k := range []string{"reset-sold", "reset-snew", "sent-sold", "reset", "start"} {
		MarkOverview(root, k, "")
	}
	MarkOverview(other, "reset-sold", "")
	old := time.Now().Add(-10 * 24 * time.Hour)
	for _, f := range []string{overviewMarkFile(root, "reset-sold"), overviewMarkFile(root, "sent-sold"), overviewMarkFile(root, "reset"), overviewMarkFile(root, "start"), overviewMarkFile(other, "reset-sold")} {
		os.Chtimes(f, old, old)
	}
	PruneOverviewMarks(root, []string{"reset-s", "sent-s"}, 7*24*time.Hour)
	gone := func(r, k string) bool { at, _ := OverviewMark(r, k); return at.IsZero() }
	if !gone(root, "reset-sold") || !gone(root, "sent-sold") {
		t.Fatal("old per-session marks of the root must go")
	}
	if gone(root, "reset-snew") || gone(root, "reset") || gone(root, "start") || gone(other, "reset-sold") {
		t.Fatal("recent marks, other kinds and other roots' marks must stay")
	}
}

// Marks live in the state dir, so wiping the temp dir (a tmpfs on reboot)
// keeps them; a mark an older binary left in the temp dir is still read and
// pruned, and the later of the two copies counts.
func TestOverviewMarksSurviveTempDir(t *testing.T) {
	tmp, state := t.TempDir(), t.TempDir()
	t.Setenv("TMPDIR", tmp)
	t.Setenv("ORTG_STATE_DIR", filepath.Join(state, "ortg"))
	root := "/repo/a"
	MarkOverview(root, "sent-sx", "data")
	if _, err := os.Stat(filepath.Join(state, "ortg", overviewMarkName(root, "sent-sx"))); err != nil {
		t.Fatalf("mark not in the state dir: %v", err)
	}
	if fs, _ := filepath.Glob(filepath.Join(tmp, "ortg-overview-*")); len(fs) != 0 {
		t.Fatalf("mark left in the temp dir: %v", fs)
	}
	t.Setenv("TMPDIR", t.TempDir()) // reboot: a fresh, empty temp dir
	if at, d := OverviewMark(root, "sent-sx"); at.IsZero() || d != "data" {
		t.Fatalf("mark lost with the temp dir: %v %q", at, d)
	}

	legacy := filepath.Join(os.Getenv("TMPDIR"), overviewMarkName(root, "reset-sold"))
	os.WriteFile(legacy, []byte("compact"), 0o600)
	if at, d := OverviewMark(root, "reset-sold"); at.IsZero() || d != "compact" {
		t.Fatalf("legacy temp-dir mark not read: %v %q", at, d)
	}
	old := time.Now().Add(-time.Hour)
	os.Chtimes(legacy, old, old)
	MarkOverview(root, "reset-sold", "clear")
	if _, d := OverviewMark(root, "reset-sold"); d != "clear" {
		t.Fatalf("the later copy must count, got %q", d)
	}
	os.Chtimes(legacy, old.Add(-10*24*time.Hour), old.Add(-10*24*time.Hour))
	PruneOverviewMarks(root, []string{"reset-s"}, 7*24*time.Hour)
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatal("stale legacy marks must be pruned too")
	}
}

// A subagent's hook names the parent's session and its own thread apart.
func TestReadHookInputAgent(t *testing.T) {
	in := ReadHookInput(strings.NewReader(`{"hook_event_name":"PostCompact","session_id":"parent","agent_id":"sub/../t2"}`), time.Second)
	if in.SessionID != "parent" || in.AgentID != "t2" {
		t.Fatalf("session %q agent %q", in.SessionID, in.AgentID)
	}
	if in := ReadHookInput(strings.NewReader(`{"session_id":"s"}`), time.Second); in.AgentID != "" {
		t.Fatalf("no agent_id: %q", in.AgentID)
	}
}

// Claude Code names the tool use in _meta; nothing else names one.
func TestToolUseID(t *testing.T) {
	if got := ToolUseID(map[string]any{"claudecode/toolUseId": "toolu_1", "progressToken": 2}); got != "toolu_1" {
		t.Fatalf("got %q", got)
	}
	if got := ToolUseID(map[string]any{"threadId": "th"}); got != "" {
		t.Fatalf("got %q", got)
	}
}

// writeTranscript writes JSONL records as Claude Code does.
func writeTranscript(t *testing.T, path string, recs ...map[string]any) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	var b bytes.Buffer
	for _, r := range recs {
		j, _ := json.Marshal(r)
		b.Write(append(j, '\n'))
	}
	if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func boundary(at time.Time) map[string]any {
	return map[string]any{"type": "system", "subtype": "compact_boundary", "content": "Conversation compacted", "timestamp": at.UTC().Format(time.RFC3339Nano)}
}

// A transcript's last compact_boundary record says when its context last
// compacted; it is read incrementally, a half-written record waits for its
// newline, and a mention of the word in a message is no boundary.
func TestLastCompactAt(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "sess.jsonl")
	if got := SubagentTranscript(main, "a1"); got != filepath.Join(dir, "sess", "subagents", "agent-a1.jsonl") {
		t.Fatalf("subagent transcript: %s", got)
	}
	now := time.Now().Truncate(time.Millisecond)
	chat := map[string]any{"type": "user", "message": map[string]any{"content": "talk about \"compact_boundary\""}, "note": "compact_boundary", "timestamp": now.UTC().Format(time.RFC3339Nano)}
	writeTranscript(t, main, chat)
	if got := LastCompactAt(main); !got.IsZero() {
		t.Fatalf("no boundary yet: %v", got)
	}
	appendLine := func(s string) {
		f, _ := os.OpenFile(main, os.O_APPEND|os.O_WRONLY, 0o644)
		f.WriteString(s)
		f.Close()
	}
	b1, _ := json.Marshal(boundary(now.Add(-time.Minute)))
	appendLine(string(b1) + "\n")
	if got := LastCompactAt(main); !got.Equal(now.Add(-time.Minute)) {
		t.Fatalf("appended boundary: %v", got)
	}
	b2, _ := json.Marshal(boundary(now))
	appendLine(string(b2[:20]))
	if got := LastCompactAt(main); !got.Equal(now.Add(-time.Minute)) {
		t.Fatalf("a half-written record is not read yet: %v", got)
	}
	appendLine(string(b2[20:]) + "\n")
	if got := LastCompactAt(main); !got.Equal(now) {
		t.Fatalf("the finished record is read: %v", got)
	}
	writeTranscript(t, main, chat)
	if got := LastCompactAt(main); !got.IsZero() {
		t.Fatalf("a rewritten, shorter transcript is read again: %v", got)
	}
	if got := LastCompactAt(filepath.Join(dir, "missing.jsonl")); !got.IsZero() {
		t.Fatalf("no transcript: %v", got)
	}
	in := ReadHookInput(strings.NewReader(`{"hook_event_name":"PostCompact","session_id":"s","transcript_path":"/x/s.jsonl"}`), time.Second)
	if in.TranscriptPath != "/x/s.jsonl" {
		t.Fatalf("transcript_path not read: %q", in.TranscriptPath)
	}
}
