package base

import (
	"bytes"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// HookInput is the host-independent view of a hook payload.
type HookInput struct {
	Event     string
	ToolName  string
	FilePath  string
	FilePaths []string
	Command   string
	Cwd       string
	Source    string
	SessionID string
	Model     string // only Claude Code's interactive SessionStart carries it
	// ToolOutput is what a PostToolUse payload says the tool printed, with
	// terminal color codes removed and at most its last toolOutputMax bytes.
	// A host may already have cut a long output (Claude Code keeps its head
	// when it spills the rest to a file), so a summary line can be missing.
	ToolOutput string
	// Background is a Bash call's run_in_background (Claude Code): the
	// command keeps running after the call returns.
	Background bool
	// StopHookActive is a Stop payload's stop_hook_active: the turn already
	// went on once because a Stop hook blocked it.
	StopHookActive bool
}

// toolOutputMax bounds HookInput.ToolOutput.
const toolOutputMax = 64 << 10

// ansiRe matches terminal control sequences (colors): test runners forced to
// color their output (pytest --color=yes) wrap every summary word in them.
var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)

func injectDir(session string) string {
	return filepath.Join(os.TempDir(), "ortg-hook-"+session)
}

// InjectOnce records that key was injected in this session and reports
// whether this is the first time. An empty session never deduplicates.
func InjectOnce(session, key string) bool {
	if session == "" {
		return true
	}
	dir := injectDir(session)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return true
	}
	f, err := os.OpenFile(filepath.Join(dir, fmt.Sprintf("%x", sha1.Sum([]byte(key)))), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return false
	}
	f.Close()
	return true
}

// SaveRelations keeps the call-graph answer a pre-edit hook got for rel, so
// the post-edit hook can use it as the graph from before the edit instead of
// asking CBM again. A nil (failed) answer is not kept.
func SaveRelations(session, rel string, rs []Relation) {
	if session == "" || rs == nil {
		return
	}
	dir := injectDir(session)
	if data, err := json.Marshal(rs); err == nil && os.MkdirAll(dir, 0o700) == nil {
		_ = os.WriteFile(filepath.Join(dir, fmt.Sprintf("rel-%x", sha1.Sum([]byte(rel)))), data, 0o600)
	}
}

// TakeRelations returns and forgets what SaveRelations kept for rel: one
// answer serves one edit, a later edit of the same file asks CBM itself.
func TakeRelations(session, rel string) ([]Relation, bool) {
	if session == "" {
		return nil, false
	}
	p := filepath.Join(injectDir(session), fmt.Sprintf("rel-%x", sha1.Sum([]byte(rel))))
	data, err := os.ReadFile(p)
	os.Remove(p)
	var rs []Relation
	if err != nil || json.Unmarshal(data, &rs) != nil {
		return nil, false
	}
	return rs, true
}

// ResetInjectedKey forgets one injection of the session, leaving the rest —
// a resume keeps the file-level markers but must show the pending decision again.
func ResetInjectedKey(session, key string) {
	if session != "" {
		os.Remove(filepath.Join(injectDir(session), fmt.Sprintf("%x", sha1.Sum([]byte(key)))))
	}
}

// ResetInjected forgets every injection of the session.
func ResetInjected(session string) {
	if session != "" {
		os.RemoveAll(injectDir(session))
	}
}

// Host identifies the agent host: ORTG_AGENT overrides, then plugin env vars.
func Host() string {
	if v := os.Getenv("ORTG_AGENT"); v != "" {
		return v
	}
	// Codex exports its native PLUGIN_* variables and also the
	// CLAUDE_PLUGIN_* compatibility ones (measured on codex-cli 0.154: a hook
	// sees PLUGIN_ROOT, PLUGIN_DATA, CLAUDE_PLUGIN_ROOT and CLAUDE_PLUGIN_DATA
	// together), while Claude Code sets only CLAUDE_PLUGIN_*. The native
	// variables are therefore the positive signal; a compatibility variable
	// must not turn a Codex process back into Claude.
	if os.Getenv("PLUGIN_ROOT") != "" || os.Getenv("PLUGIN_DATA") != "" {
		return "codex"
	}
	return "claude"
}

// CodexContextWindow is the context window, in tokens, of the model Codex is
// configured with: config.toml's model_context_window (Codex caps it at the
// model's max_context_window) or else the model's context_window from
// models_cache.json, times the share Codex really uses
// (effective_context_window_percent). 0 when it cannot be read: Codex does not
// tell the MCP server its window.
func CodexContextWindow() int {
	home := os.Getenv("CODEX_HOME")
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return 0
		}
		home = filepath.Join(h, ".codex")
	}
	model, window := codexConfig(filepath.Join(home, "config.toml"))
	b, err := os.ReadFile(filepath.Join(home, "models_cache.json"))
	if err != nil || model == "" {
		return window
	}
	var cache struct {
		Models []struct {
			Slug    string `json:"slug"`
			Context int    `json:"context_window"`
			Max     int    `json:"max_context_window"`
			Percent int    `json:"effective_context_window_percent"`
		} `json:"models"`
	}
	if json.Unmarshal(b, &cache) != nil {
		return window
	}
	for _, m := range cache.Models {
		if m.Slug != model {
			continue
		}
		if window == 0 {
			window = m.Context
		} else if m.Max > 0 && window > m.Max {
			window = m.Max
		}
		if m.Percent > 0 && m.Percent < 100 {
			window = window * m.Percent / 100
		}
	}
	return window
}

// codexConfig reads the top-level model and model_context_window keys of a
// Codex config.toml: the lines before its first [table].
func codexConfig(path string) (model string, window int) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", 0
	}
	for _, l := range strings.Split(string(b), "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "[") {
			break
		}
		k, v, ok := strings.Cut(l, "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		if i := strings.Index(v, " #"); i >= 0 {
			v = strings.TrimSpace(v[:i])
		}
		switch strings.TrimSpace(k) {
		case "model":
			model = strings.Trim(v, `"'`)
		case "model_context_window":
			fmt.Sscan(v, &window)
		}
	}
	return model, window
}

// ApplyPatchPaths extracts only apply_patch file-header lines.  Paths are
// treated as opaque strings: no shell parsing, expansion, or unquoting is
// performed here.  HookReply later cleans each path and rejects anything
// outside the repository before inspecting a file.
func ApplyPatchPaths(command string) []string {
	prefixes := [...]string{
		"*** Update File:",
		"*** Add File:",
		"*** Delete File:",
	}
	seen := make(map[string]bool)
	var paths []string
	for _, line := range strings.Split(command, "\n") {
		line = strings.TrimSuffix(line, "\r")
		for _, prefix := range prefixes {
			if !strings.HasPrefix(line, prefix) {
				continue
			}
			p := strings.TrimSpace(strings.TrimPrefix(line, prefix))
			if p == "" || p == "." || strings.ContainsRune(p, '\x00') || seen[p] {
				break
			}
			seen[p] = true
			paths = append(paths, p)
			break
		}
	}
	return paths
}

func isApplyPatchTool(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.NewReplacer("_", "", "-", "").Replace(name)
	return name == "applypatch"
}

// toolOutput reads a tool_response: a string as is (Codex), or an object's
// stdout and stderr (Claude Code's Bash) or its output field; colors are
// removed and only the tail is kept.
func toolOutput(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var out string
	if json.Unmarshal(raw, &out) != nil {
		var o struct {
			Stdout string `json:"stdout"`
			Stderr string `json:"stderr"`
			Output string `json:"output"`
		}
		if json.Unmarshal(raw, &o) != nil {
			return ""
		}
		out = o.Stdout
		if o.Stderr != "" {
			out += "\n" + o.Stderr
		}
		if out == "" {
			out = o.Output
		}
	}
	out = ansiRe.ReplaceAllString(out, "")
	if len(out) > toolOutputMax {
		out = out[len(out)-toolOutputMax:]
	}
	return out
}

// ReadHookInput reads the JSON payload with a timeout: after timeout without
// EOF whatever arrived is parsed. Missing fields yield empty values.
func ReadHookInput(r io.Reader, timeout time.Duration) HookInput {
	var mu sync.Mutex
	var buf bytes.Buffer
	done := make(chan struct{})
	go func() {
		chunk := make([]byte, 4096)
		for {
			n, err := r.Read(chunk)
			if n > 0 {
				mu.Lock()
				buf.Write(chunk[:n])
				mu.Unlock()
			}
			if err != nil {
				break
			}
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
	}
	mu.Lock()
	raw := append([]byte(nil), buf.Bytes()...)
	mu.Unlock()
	var in HookInput
	var m struct {
		Event     string          `json:"hook_event_name"`
		ToolName  string          `json:"tool_name"`
		Cwd       string          `json:"cwd"`
		Source    string          `json:"source"`
		SessionID string          `json:"session_id"`
		Model     string          `json:"model"`
		Response  json.RawMessage `json:"tool_response"`
		StopHook  bool            `json:"stop_hook_active"`
		Input     struct {
			FilePath     string `json:"file_path"`
			NotebookPath string `json:"notebook_path"`
			Command      string `json:"command"`
			Background   bool   `json:"run_in_background"`
		} `json:"tool_input"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(string(raw), "\uFEFF")), &m); err != nil {
		return in
	}
	in.Event, in.ToolName, in.Cwd, in.Source, in.SessionID = m.Event, m.ToolName, m.Cwd, m.Source, filepath.Base(m.SessionID)
	in.Model = m.Model
	in.ToolOutput = toolOutput(m.Response)
	in.FilePath = m.Input.FilePath
	if in.FilePath == "" {
		in.FilePath = m.Input.NotebookPath
	}
	in.Command = m.Input.Command
	in.Background, in.StopHookActive = m.Input.Background, m.StopHook
	if in.FilePath != "" {
		in.FilePaths = append(in.FilePaths, in.FilePath)
	}
	if isApplyPatchTool(in.ToolName) {
		for _, p := range ApplyPatchPaths(in.Command) {
			duplicate := false
			for _, have := range in.FilePaths {
				if have == p {
					duplicate = true
					break
				}
			}
			if !duplicate {
				in.FilePaths = append(in.FilePaths, p)
			}
		}
	}
	// Keep FilePath as the backwards-compatible primary path for callers that
	// have not been taught about a multi-file apply_patch payload yet.
	if in.FilePath == "" && len(in.FilePaths) > 0 {
		in.FilePath = in.FilePaths[0]
	}
	return in
}

// WriteHookOutput writes text in the envelope the host expects for the event.
// Claude's SessionStart takes raw stdout; other events need the JSON form;
// Codex additionally wants a systemMessage. Empty text writes nothing.
func WriteHookOutput(w io.Writer, host, event, text, userMsg string) {
	if event == "Stop" {
		// Both hosts read a Stop reply the same way: block with a reason
		// keeps the turn going and the reason is the model's next prompt.
		// Codex refuses an empty reply on exit 0, so "no objection" is {}.
		if text == "" {
			w.Write([]byte("{}"))
			return
		}
		b, _ := json.Marshal(map[string]any{"decision": "block", "reason": text})
		w.Write(b)
		return
	}
	if text == "" && userMsg == "" {
		return
	}
	// One envelope for every event: additionalContext reaches the model,
	// systemMessage is shown to the human. The reconcile report is useless to
	// the person at the keyboard if only the model ever sees it.
	out := map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": event, "additionalContext": text}}
	switch {
	case userMsg != "":
		out["systemMessage"] = userMsg
	case host == "codex":
		out["systemMessage"] = "ORTG"
	}
	b, _ := json.Marshal(out)
	w.Write(b)
}

// PreToolDecision renders the reply of a PreToolUse hook the host runs as an
// MCP tool call: deny refuses the call and text is the reason the model reads;
// otherwise text is added to the model's context. No text, no reply.
func PreToolDecision(text string, deny bool) string {
	if text == "" {
		return ""
	}
	o := map[string]any{"hookEventName": "PreToolUse"}
	if deny {
		o["permissionDecision"] = "deny"
		o["permissionDecisionReason"] = text
	} else {
		o["additionalContext"] = text
	}
	b, _ := json.Marshal(map[string]any{"hookSpecificOutput": o})
	return string(b)
}

// Overview marks are per-repository files the hooks leave for the MCP server,
// which outlives compactions and clears and is never told the session id.
// The only kind so far, "reset", is the moment the context lost what was sent
// (compact or clear). The mtime is the moment; the content is optional data.
func overviewMarkFile(root, kind string) string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("ortg-overview-%s-%x", kind, sha1.Sum([]byte(root))))
}

// MarkOverview writes root's mark of the given kind, stamped now.
func MarkOverview(root, kind, data string) {
	f := overviewMarkFile(root, kind)
	if err := os.WriteFile(f, []byte(data), 0o600); err != nil {
		Debugf("overview mark %s %s: %v", kind, root, err)
		return
	}
	now := time.Now()
	_ = os.Chtimes(f, now, now)
}

// OverviewMark returns when root's mark of the given kind was written and its
// data; the zero time when never.
func OverviewMark(root, kind string) (time.Time, string) {
	f := overviewMarkFile(root, kind)
	fi, err := os.Stat(f)
	if err != nil {
		return time.Time{}, ""
	}
	b, _ := os.ReadFile(f)
	return fi.ModTime(), string(b)
}

// ProcessAlive reports whether a process runs words — a command (argv[k]'s
// base name equal to words[0], the next arguments equal to the rest), or a
// shell running a -c script that contains the words — with its working
// directory inside root, started no earlier than since (less a little
// slack), and not this process or one of its ancestors (the host agent
// whose own arguments may quote the command). It reads /proc and is always
// false where there is none.
func ProcessAlive(root string, words []string, since time.Time) bool {
	if len(words) == 0 || runtime.GOOS != "linux" {
		return false
	}
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return false
	}
	skip := map[string]bool{}
	for pid := strconv.Itoa(os.Getpid()); pid != "" && pid != "0" && !skip[pid]; pid = procParent(pid) {
		skip[pid] = true
	}
	boot := procBootTime()
	joined := strings.Join(words, " ")
	for _, e := range ents {
		pid := e.Name()
		if skip[pid] || pid[0] < '0' || pid[0] > '9' {
			continue
		}
		b, err := os.ReadFile("/proc/" + pid + "/cmdline")
		if err != nil || len(b) == 0 || !argvRuns(strings.Split(strings.TrimRight(string(b), "\x00"), "\x00"), words, joined) {
			continue
		}
		cwd, err := os.Readlink("/proc/" + pid + "/cwd")
		if err != nil {
			continue
		}
		if _, inside := Rel(root, cwd); !inside {
			continue
		}
		if start, ok := procStart(pid, boot); ok && start.Before(since.Add(-10*time.Second)) {
			continue
		}
		return true
	}
	return false
}

// procShells run the script given after -c.
var procShells = map[string]bool{"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true}

// argvRuns reports whether a process's arguments run words: as a command
// at some position, or as a shell whose -c script contains them.
func argvRuns(argv, words []string, joined string) bool {
	if len(argv) > 2 && procShells[filepath.Base(argv[0])] {
		for _, a := range argv[1:] {
			if strings.Contains(a, joined) {
				return true
			}
		}
	}
	for k := 0; k+len(words) <= len(argv); k++ {
		if filepath.Base(argv[k]) != filepath.Base(words[0]) {
			continue
		}
		same := true
		for j := 1; j < len(words); j++ {
			if argv[k+j] != words[j] {
				same = false
				break
			}
		}
		if same {
			return true
		}
	}
	return false
}

// procStatFields returns /proc/<pid>/stat's fields after the command name
// (which may hold spaces and parentheses): field 3 (state) comes first.
func procStatFields(pid string) []string {
	b, err := os.ReadFile("/proc/" + pid + "/stat")
	if err != nil {
		return nil
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return nil
	}
	return strings.Fields(s[i+1:])
}

func procParent(pid string) string {
	if f := procStatFields(pid); len(f) > 1 {
		return f[1]
	}
	return ""
}

func procBootTime() time.Time {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return time.Time{}
	}
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "btime ") {
			if n, err := strconv.ParseInt(strings.TrimSpace(l[6:]), 10, 64); err == nil {
				return time.Unix(n, 0)
			}
		}
	}
	return time.Time{}
}

// procStart is when a process started: boot time plus its start time in
// clock ticks (USER_HZ, 100 on Linux).
func procStart(pid string, boot time.Time) (time.Time, bool) {
	f := procStatFields(pid)
	if boot.IsZero() || len(f) < 20 {
		return time.Time{}, false
	}
	ticks, err := strconv.ParseInt(f[19], 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	return boot.Add(time.Duration(ticks) * time.Second / 100), true
}
