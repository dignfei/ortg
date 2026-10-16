package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ortg/base"
)

func TestShellLiteralAccess(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, "nested"), 0o755)
	os.WriteFile(filepath.Join(root, "nested", "empty"), []byte("public fixture"), 0o644)
	os.WriteFile(filepath.Join(root, "a.go"), []byte("package a"), 0o644)
	t.Chdir(root)
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	x, err := base.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, command string
		want          bool
	}{
		{"print heredoc", "python - <<'PY'\nprint('semantic target fields empty')\nPY", false},
		{"print inline", `python -c "print('semantic target fields empty')"`, false},
		{"printed open", `python -c "print(\"open('nested/empty')\")"`, false},
		{"comment", "python - <<'PY'\n# open('nested/empty')\nprint('ok')\nPY", false},
		{"docstring", "python - <<'PY'\n'''open('nested/empty')'''\nPY", false},
		{"dynamic", "python - <<'PY'\np='nested/empty'\nprint(open(p).read())\nPY", false},
		{"dynamic suffix", `python -c "suffix='.data'; open('nested/empty'+suffix).read()"`, false},
		{"path construction", `python -c "from pathlib import Path; print(Path('nested/empty'))"`, false},
		{"cat", "cat nested/empty", true},
		{"bare after cd", "cd nested && cat empty", true},
		{"sed", "sed -n 1p nested/empty", true},
		{"open", `python -c "print(open('nested/empty').read())"`, true},
		{"path", "python - <<'PY'\nfrom pathlib import Path\nprint(Path('nested/empty').read_text())\nPY", true},
		{"path bytes", `python -c "import pathlib; pathlib.Path('nested/empty').read_bytes()"`, true},
		{"module config", "python -m pytest -c nested/empty", true},
		{"path unbuffered", "python -u <<'PY'\nfrom pathlib import Path\nPath('nested/empty').read_text()\nPY", true},
		{"data heredoc", "cat <<'PY'\nopen('nested/empty')\nPY", false},
		{"wrapped print", `bash -lc 'python -c "print(\"empty\")"'`, false},
		{"wrapped open", `bash -lc 'python -c "open(\"nested/empty\").read()"'`, true},
		{"wrapped long option", `bash --norc -c 'python -c "open(\"nested/empty\").read()"'`, true},
		{"heredoc pipe", "python - <<'PY' | cat\nprint(open('nested/empty').read())\nPY", true},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if slices.Contains([]string{"print heredoc", "print inline", "cat", "open", "path"}, tc.name) {
				if out, err := exec.Command("bash", "-c", tc.command).CombinedOutput(); err != nil {
					t.Fatalf("public command: %v: %s", err, out)
				}
			}
			paths, _ := gateTargets(x, "Bash", "", "", tc.command)
			if got := slices.Contains(paths, "nested/empty"); got != tc.want {
				t.Errorf("pre paths %v, want read=%v", paths, tc.want)
			}
			out := HookReply("post", base.HookInput{Cwd: root, SessionID: fmt.Sprintf("literal-%d", i), ToolName: "Bash", Command: tc.command})
			got := strings.Contains(out, "nested/empty") && strings.Contains(out, "尚无纲要")
			if got != tc.want {
				t.Errorf("post read=%v, want %v: %s", got, tc.want, out)
			}
		})
	}
	if _, err := Update("a.go", "a.go[5 L ST T]: Role:fixture | Uses:- | API:- | Constraints:-"); err != nil {
		t.Fatal(err)
	}
	if _, err := Target("a.go", "=", false, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := Overview("", false); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range []string{"sed -i s/a/b/ a.go", "echo hi > a.go", `python -c "from pathlib import Path; Path('a.go').write_text('package b')"`, `python -c "open('a.go', encoding='utf-8', mode='w').write('package b')"`, `bash -c 'printf x' > a.go`} {
		_, writes := gateTargets(x, "Bash", "", "", cmd)
		if !slices.Contains(writes, "a.go") {
			t.Errorf("write lost for %q: %v", cmd, writes)
		}
		if out, _ := Gate("Bash", "", "", cmd, ""); !strings.Contains(out, deniedMark) || !strings.Contains(out, "ortg_review") {
			t.Errorf("unchecked plan write allowed for %q: %s", cmd, out)
		}
	}
}

func TestShellPathIdentity(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	root, other := t.TempDir(), t.TempDir()
	for _, dir := range []string{root, other} {
		if err := os.Mkdir(filepath.Join(dir, "nested"), 0o755); err != nil {
			t.Fatal(err)
		}
		for _, p := range []string{"nested/server.go", "shared.go", "two words.go"} {
			if err := os.WriteFile(filepath.Join(dir, p), []byte("package fixture\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Chdir(root)
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	x, err := base.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, command, want string
		missing, wrapper    bool
	}{
		{name: "missing relative", command: "cat server.go", missing: true},
		{name: "external nested", command: "cat " + strconv.Quote(filepath.Join(other, "nested/server.go"))},
		{name: "external root", command: "cat " + strconv.Quote(filepath.Join(other, "shared.go"))},
		{name: "external cd", command: "cd " + strconv.Quote(other) + " && cat shared.go"},
		{name: "root", command: "cat shared.go", want: "shared.go"},
		{name: "nested", command: "cat nested/server.go", want: "nested/server.go"},
		{name: "nested cd", command: "cd nested && cat server.go", want: "nested/server.go"},
		{name: "parent", command: "cd nested && cat ../shared.go", want: "shared.go"},
		{name: "internal absolute", command: "cat " + strconv.Quote(filepath.Join(root, "nested/server.go")), want: "nested/server.go"},
		{name: "attached input", command: "cat <nested/server.go", want: "nested/server.go"},
		{name: "unspaced input", command: "cat<nested/server.go", want: "nested/server.go"},
		{name: "input fd", command: "cat 0<nested/server.go", want: "nested/server.go"},
		{name: "external input", command: "cat<" + strconv.Quote(filepath.Join(other, "nested/server.go"))},
		{name: "quoted space", command: `cat "two words.go"`, want: "two words.go"},
		{name: "escaped space", command: `cat two\ words.go`, want: "two words.go"},
		{name: "unknown wrapper nested", command: `const r = await tools.exec_command({cmd:"cat server.go",workdir:` + strconv.Quote(other) + `});text(r.output);`, wrapper: true},
		{name: "unknown wrapper root", command: `const r = await tools.exec_command({cmd:"cat shared.go",workdir:` + strconv.Quote(other) + `});text(r.output);`, wrapper: true},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !tc.wrapper {
				out, err := exec.Command("bash", "-c", tc.command).CombinedOutput()
				if (err != nil) != tc.missing {
					t.Fatalf("public command: missing=%v, err=%v: %s", tc.missing, err, out)
				}
			}
			paths, _ := gateTargets(x, "Bash", "", "", tc.command)
			if got := strings.Join(paths, ","); got != tc.want {
				t.Errorf("pre paths %q, want %q", got, tc.want)
			}
			out := HookReply("post", base.HookInput{Cwd: root, SessionID: fmt.Sprintf("path-identity-%d", i), ToolName: "Bash", Command: tc.command})
			if tc.want == "" {
				if strings.Contains(out, "尚无纲要") {
					t.Errorf("unread repository file prompted: %s", out)
				}
			} else if !strings.Contains(out, tc.want+" 尚无纲要") {
				t.Errorf("missing exact file reminder for %s: %s", tc.want, out)
			}
		})
	}
}

func TestPairedWriteAlignment(t *testing.T) {
	cases := []struct {
		name, command string
		wantPending   bool
		planned       bool
		observed      bool
		noID          bool
		wrongPost     bool
		wrongSession  bool
		exitFailure   bool
		preexisting   bool
		changedInput  bool
	}{
		{name: "write", command: "printf changed > a.go", wantPending: true},
		{name: "planned", command: "printf changed > a.go", wantPending: true, planned: true},
		{name: "same bytes", command: "printf content > a.go"},
		{name: "failed before write", command: "false && printf changed > a.go", exitFailure: true},
		{name: "independent drift then failed write", command: "false && printf changed > a.go", exitFailure: true, preexisting: true},
		{name: "changed despite failure", command: "printf changed > a.go; false", wantPending: true, exitFailure: true},
		{name: "missing identity", command: "printf changed > a.go", noID: true},
		{name: "wrong operation", command: "printf changed > a.go", wrongPost: true},
		{name: "wrong context", command: "printf changed > a.go", wrongSession: true},
		{name: "changed input", command: "printf changed > a.go", changedInput: true},
		{name: "read", command: "cat a.go"},
		{name: "observed", command: "printf changed > a.go", observed: true},
		{name: "added", command: "printf changed > new.go", wantPending: true},
		{name: "removed", command: "rm a.go"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ORTG_STATE_DIR", t.TempDir())
			t.Setenv("ORTG_AGENT", "codex")
			root := updateRepo(t, "a.go", "b.go")
			if _, err := scanWithHeader(); err != nil {
				t.Fatal(err)
			}
			if _, err := Header("", nil, nil, nil, nil, nil, nil, "task"); err != nil {
				t.Fatal(err)
			}
			for _, p := range []string{"a.go", "b.go"} {
				if _, err := Update(p, updateLine(p, "public fixture")); err != nil {
					t.Fatal(err)
				}
			}
			if tc.observed {
				if _, err := Header("", nil, nil, nil, nil, []string{"a.go"}, nil); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := Overview("", false); err != nil {
				t.Fatal(err)
			}
			if tc.planned {
				if _, err := Target("a.go", "=", false, ""); err != nil {
					t.Fatal(err)
				}
				if out, err := Review(); err != nil || !strings.Contains(out, "已收敛") {
					t.Fatalf("planned write: %v %s", err, out)
				}
			}
			if tc.preexisting {
				writeFile(t, filepath.Join(root, "a.go"), "independent drift")
			}
			if out, err := Gate("Bash", "", "", tc.command, ""); err != nil || strings.Contains(out, deniedMark) {
				t.Fatalf("fixture write was not allowed: %v %s", err, out)
			}
			in := base.HookInput{Cwd: root, SessionID: "writer", TurnID: "turn", ToolUseID: "operation", ToolName: "Bash", Command: tc.command}
			if tc.noID {
				in.ToolUseID = ""
			}
			if out := HookReply("pre", in); out != "" {
				t.Fatalf("shell sampling must be silent: %s", out)
			}
			cmd := exec.Command("sh", "-c", tc.command)
			if out, err := cmd.CombinedOutput(); (err != nil) != tc.exitFailure {
				t.Fatalf("actual shell result: %v %s", err, out)
			}
			post := in
			if tc.wrongPost {
				post.ToolUseID = "other operation"
			}
			if tc.wrongSession {
				post.SessionID = "other context"
			}
			if tc.changedInput {
				post.Command += " # modified by another pre hook"
			}
			HookReply("post", post)
			path := "a.go"
			if tc.name == "added" {
				path = "new.go"
			}
			before, err := os.ReadFile(filepath.Join(root, base.TableName))
			if err != nil {
				t.Fatal(err)
			}
			_, proofBefore := base.OverviewMark(root, "writes")
			_, err = Align("paths", []string{path})
			unfinished := tc.wrongPost || tc.wrongSession
			if (err != nil) != (tc.wantPending || unfinished) {
				t.Fatalf("pending=%v, Align error=%v", tc.wantPending, err)
			}
			if unfinished {
				if !strings.Contains(err.Error(), "尚未完成配对") || strings.Contains(err.Error(), "已记录尚未提交") {
					t.Fatalf("an unmatched completion is only a temporary deferral: %v", err)
				}
				if err := withWriteProofs(root, func(s *writeProofs) (bool, error) {
					if len(s.Pending) != 0 {
						t.Fatal("an unmatched completion created permanent evidence")
					}
					return false, nil
				}); err != nil {
					t.Fatal(err)
				}
				return
			}
			if !tc.wantPending {
				return
			}
			if !strings.Contains(err.Error(), "ortg_update") {
				t.Fatalf("refusal must name the existing maintenance route: %v", err)
			}
			after, _ := os.ReadFile(filepath.Join(root, base.TableName))
			_, proofAfter := base.OverviewMark(root, "writes")
			if string(before) != string(after) || proofBefore != proofAfter {
				t.Fatal("a refused Align changed the table or its evidence")
			}
			if tc.name == "write" {
				HookReply("compact", base.HookInput{Cwd: root, SessionID: "writer"})
				r := nativeMCP(t, root, map[string]any{"method": "tools/call", "params": map[string]any{
					"name": "ortg_align", "arguments": map[string]any{"mode": "all"}, "_meta": map[string]any{"threadId": "another context"},
				}})[0]
				if !r.Result.IsError || !strings.Contains(r.Result.Content[0].Text, "ortg_update") {
					t.Fatalf("another MCP process/context after compaction bypassed the obligation: %+v", r)
				}
				if !testing.Short() {
					bin := filepath.Join(t.TempDir(), "ortg")
					build := exec.Command("go", "build", "-o", bin, ".")
					build.Dir = worktreeDir(t)
					build.Env = append(os.Environ(), "CGO_ENABLED=0")
					if out, err := build.CombinedOutput(); err != nil {
						t.Fatalf("current CLI build: %v %s", err, out)
					}
					cli := exec.Command(bin, "align", "a.go")
					cli.Dir = root
					if out, err := cli.CombinedOutput(); err == nil || !strings.Contains(string(out), "ortg_update") {
						t.Fatalf("real CLI bypassed the obligation: %v %s", err, out)
					}
				}
			}
			if _, err := Update(path, updateLine(path, "understood final fixture")); err != nil {
				t.Fatal(err)
			}
			HookReply("post", post) // duplicate completion cannot revive the obligation
			if _, err := Align("paths", []string{path}); err != nil {
				t.Fatalf("successful update did not clear the obligation: %v", err)
			}
		})
	}
}

func TestInFlightWriteAlignment(t *testing.T) {
	if testing.Short() {
		t.Skip("real CLI subprocess build")
	}
	bin := filepath.Join(t.TempDir(), "ortg")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Dir = worktreeDir(t)
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("current CLI build: %v %s", err, out)
	}
	for _, tc := range []struct {
		name, source, script string
		deferAlignment       bool
	}{
		{"changed before inner align", "content", "printf changed > a.go", true},
		{"same content", "content", "printf content > a.go", false},
		{"old drift without a write", "independent drift", "false && printf changed > a.go", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ORTG_STATE_DIR", t.TempDir())
			t.Setenv("ORTG_CBM", "off")
			root := updateRepo(t, "a.go")
			if _, err := scanWithHeader(); err != nil {
				t.Fatal(err)
			}
			if _, err := Update("a.go", updateLine("a.go", "original")); err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(root, "a.go"), tc.source)
			command := tc.script + "; " + strconv.Quote(bin) + " align a.go"
			in := base.HookInput{Cwd: root, SessionID: "writer", ToolUseID: "compound", ToolName: "Bash", Command: command}
			HookReply("pre", in)
			tableBefore, _ := os.ReadFile(filepath.Join(root, base.TableName))
			shell := exec.Command("sh", "-c", command)
			shell.Dir = root
			out, err := shell.CombinedOutput()
			if (err != nil) != tc.deferAlignment {
				t.Fatalf("inner Align deferred=%v: %v %s", tc.deferAlignment, err, out)
			}
			if tc.deferAlignment {
				if !strings.Contains(string(out), "尚未完成配对") || strings.Contains(string(out), "已记录尚未提交") {
					t.Fatalf("in-flight refusal must differ from confirmed Post evidence: %s", out)
				}
				tableAfter, _ := os.ReadFile(filepath.Join(root, base.TableName))
				if string(tableBefore) != string(tableAfter) {
					t.Fatal("inner Align advanced fingerprints before completion")
				}
				if err := withWriteProofs(root, func(s *writeProofs) (bool, error) {
					if len(s.Pending) != 0 {
						t.Fatal("an unfinished operation became permanent evidence")
					}
					return false, nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			HookReply("post", in)
			if tc.deferAlignment {
				if status, err := Status(); err != nil || !strings.Contains(status, "过期 1") {
					t.Fatalf("post must retain stale content after a refused inner Align: %v %s", err, status)
				}
				if _, err := Align("paths", []string{"a.go"}); err == nil || !strings.Contains(err.Error(), "已记录尚未提交") {
					t.Fatalf("completed paired write must then require Update: %v", err)
				}
			} else if _, err := Align("paths", []string{"a.go"}); err != nil {
				t.Fatalf("no actual operation change acquired an obligation: %v", err)
			}
		})
	}
}
