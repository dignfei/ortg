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
