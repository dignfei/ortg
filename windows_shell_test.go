package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ortg/base"
)

// Codex on Windows runs PowerShell (and cmd through it): its writes, searches
// and directory changes must count like their POSIX twins. Paths here use
// forward slashes, which PowerShell accepts; backslash paths are checked at
// the tokenizer, since a Linux test cannot resolve them.
func TestPowerShellShellTargets(t *testing.T) {
	useTestMCPSession(t)
	t.Setenv("TMPDIR", t.TempDir())
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, "nested"), 0o755)
	os.WriteFile(filepath.Join(root, "nested", "b.go"), []byte("package b"), 0o644)
	os.WriteFile(filepath.Join(root, "a.go"), []byte("package a"), 0o644)
	t.Chdir(root)
	if _, err := scanWithHeader(); err != nil {
		t.Fatal(err)
	}
	x, err := base.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	old := backslashPaths
	backslashPaths = true
	t.Cleanup(func() { backslashPaths = old })

	writes := []struct {
		command string
		want    []string
	}{
		{`Set-Content -Path a.go -Value "nested/b.go"`, []string{"a.go"}},
		{`Set-Content a.go 'package c'`, []string{"a.go"}},
		{`"x" | Out-File -FilePath a.go -Encoding utf8`, []string{"a.go"}},
		{`Add-Content -LiteralPath:a.go -Value x`, []string{"a.go"}},
		{`Remove-Item -Force -LiteralPath a.go`, []string{"a.go"}},
		{`Move-Item a.go nested`, []string{"a.go", "nested/a.go"}},
		{`Rename-Item -Path a.go -NewName c.go`, []string{"a.go", "c.go"}},
		{`Copy-Item -Path a.go -Destination d.go`, []string{"d.go"}},
		{`New-Item -ItemType File -Path nested -Name e.go`, []string{"nested/e.go"}},
		{`New-Item -ItemType Directory -Path newdir`, nil},
		{`pwsh -NoProfile -Command "Set-Content a.go x"`, []string{"a.go"}},
		{`powershell.exe -ExecutionPolicy Bypass -Command Remove-Item a.go`, []string{"a.go"}},
		{`cmd /c del /q a.go`, []string{"a.go"}},
		{`Set-Location nested; Set-Content b.go x`, []string{"nested/b.go"}},
		{`Push-Location nested; Pop-Location; Set-Content a.go x`, []string{"a.go"}},
		{`Get-Content a.go > nested/b.go`, []string{"nested/b.go"}},
		{`Get-Content -Raw a.go`, nil},
	}
	for _, tc := range writes {
		_, got := gateTargets(x, "Bash", "", "", tc.command)
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s: writes %v, want %v", tc.command, got, tc.want)
		}
	}

	reads := []struct {
		command string
		want    string
		touched bool
	}{
		{`Get-Content -Raw nested/b.go`, "nested/b.go", true},
		{`& python.exe -c "print(1)"; Get-Content nested/b.go`, "nested/b.go", true},
		{`Get-ChildItem -Recurse -Filter *.go | Select-String -Pattern Package`, ".", true},
		{`Select-String Package a.go`, ".", true},
		{`sls -Path nested/b.go -Pattern x`, ".", true},
		{`git status | Select-String modified`, ".", false},
		{`findstr /s /i package *.go`, ".", true},
		{`findstr package`, ".", false},
	}
	for _, tc := range reads {
		got, _ := gateTargets(x, "Bash", "", "", tc.command)
		if slices.Contains(got, tc.want) != tc.touched {
			t.Errorf("%s: touched %v, want %q=%v", tc.command, got, tc.want, tc.touched)
		}
	}

	// The backslash: a separator in a Windows path, an escape elsewhere;
	// in double quotes it escapes only " \ $ and `.
	words := func(cmd string) []string {
		var w []string
		for _, s := range shellSegments(cmd) {
			w = append(w, s.words...)
		}
		return w
	}
	if got := words(`Get-Content src\base\fs.go "C:\repo\x.go" a\ b`); !slices.Equal(got, []string{"Get-Content", `src\base\fs.go`, `C:\repo\x.go`, "a b"}) {
		t.Errorf("windows words %q", got)
	}
	backslashPaths = false
	if got := words(`cat src\main.go "a\"b" "c\d"`); !slices.Equal(got, []string{"cat", "srcmain.go", `a"b`, `c\d`}) {
		t.Errorf("posix words %q", got)
	}
	backslashPaths = true

	// & where a command starts calls it; after a group it still backgrounds.
	if detachedCommand(`Set-Location sub; & .\venv\Scripts\python.exe -m pytest`) {
		t.Error("PowerShell call operator read as a background job")
	}
	if !detachedCommand(`(sleep 5) &`) || !detachedCommand(`sleep 5 &`) {
		t.Error("background job lost")
	}
	for in, want := range map[string]string{"python.EXE": "python", "pytest.exe": "pytest", "Set-Location": "cd", "push-location": "pushd", "Pop-Location": "popd", "bash": "bash"} {
		if got := shellVerb(in); got != want {
			t.Errorf("shellVerb(%q) = %q, want %q", in, got, want)
		}
	}
	if broad, _ := testRunKind(`pwsh -Command "python.exe -m pytest"`, root); !broad {
		t.Error("wrapped PowerShell test run not seen")
	}

	// A PowerShell write is a write to the gate: outside an unchecked plan it
	// is refused like sed -i or a redirection.
	if _, err := Update("a.go", "a.go[5 L ST T]: Role:fixture | Uses:- | API:- | Constraints:-"); err != nil {
		t.Fatal(err)
	}
	if _, err := Target("a.go", "=", false, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := Overview("", false); err != nil {
		t.Fatal(err)
	}
	finishRules(t)
	for _, cmd := range []string{`Set-Content -Path a.go -Value x`, `pwsh -Command "Remove-Item a.go"`} {
		if out, _ := Gate("Bash", "", "", cmd, ""); !strings.Contains(out, deniedMark) || !strings.Contains(out, "ortg_review") {
			t.Errorf("unchecked plan write allowed for %q: %s", cmd, out)
		}
	}
}
