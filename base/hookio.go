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
	ToolUseID string // the host's operation identity, shared by its pre/post payloads
	TurnID    string // Codex additionally scopes operations to a turn
	// AgentID is set when a subagent's thread fired the hook. Codex then
	// still gives the parent's session_id: agent_id is the subagent's own
	// thread, the id its MCP calls carry.
	AgentID string
	// TranscriptPath is the session's transcript, where a Claude Code
	// compaction leaves its compact_boundary record (LastCompactAt).
	TranscriptPath string
	Model          string // only Claude Code's interactive SessionStart carries it
	// ToolOutput is what a PostToolUse payload says the tool printed, with
	// terminal color codes removed and at most its last toolOutputMax bytes.
	// A host may already have cut a long output (Claude Code keeps its head
	// when it spills the rest to a file), so a summary line can be missing.
	ToolOutput string
	// ToolExitCode is nil when the host did not provide a recognizable status.
	ToolExitCode *int
	// ToolResultComplete is true only for a recognized terminal result with
	// an exit status and no known interruption, background or output loss.
	// It describes the host result, not whether the command proved a contract.
	ToolResultComplete bool
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

// codexHome also checks the nearest ancestor's CODEX_HOME when Codex filters
// that variable from the MCP environment; an explicit value here wins.
func codexHome() string {
	home := os.Getenv("CODEX_HOME")
	if home == "" {
		for _, pid := range Ancestors() {
			b, _ := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ")
			for _, entry := range bytes.Split(b, []byte{0}) {
				if v, ok := bytes.CutPrefix(entry, []byte("CODEX_HOME=")); ok && len(v) > 0 {
					home = string(v)
					break
				}
			}
			if home != "" {
				break
			}
		}
	}
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		home = filepath.Join(h, ".codex")
	}
	return home
}

// CodexToolOutputLimit returns only an explicitly configured positive limit;
// absent or invalid host configuration stays unknown (0).
func CodexToolOutputLimit() int {
	home := codexHome()
	if home == "" {
		return 0
	}
	_, _, output := codexConfig(filepath.Join(home, "config.toml"))
	return output
}

// CodexContextWindow reads model_context_window or the model's cached window,
// capped at max_context_window and scaled by effective_context_window_percent.
// It returns 0 when configuration cannot be read.
func CodexContextWindow() int {
	home := codexHome()
	if home == "" {
		return 0
	}
	model, window, _ := codexConfig(filepath.Join(home, "config.toml"))
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

// codexConfig reads the top-level model and budget keys of a
// Codex config.toml: the lines before its first [table].
func codexConfig(path string) (model string, window, output int) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", 0, 0
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
		case "tool_output_token_limit":
			var err error
			output, err = strconv.Atoi(strings.ReplaceAll(v, "_", ""))
			if err != nil {
				output = 0
			}
			output = max(0, output)
		}
	}
	return model, window, output
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
// stdout and stderr (Claude Code's Bash) or its output field. Only a host's
// structured exit_code supplies status, before truncation. Codex 0.159.3 sends
// raw command output as a string: even a complete-looking status header there
// is program text, not evidence of the process's exit status.
func toolOutput(raw json.RawMessage) (string, *int, bool) {
	if len(raw) == 0 {
		return "", nil, false
	}
	var out string
	var code *int
	terminal := true
	if json.Unmarshal(raw, &out) != nil {
		var o struct {
			Stdout      string          `json:"stdout"`
			Stderr      string          `json:"stderr"`
			Output      string          `json:"output"`
			ExitCode    json.RawMessage `json:"exit_code"`
			Interrupted json.RawMessage `json:"interrupted"`
			IsImage     json.RawMessage `json:"isImage"`
		}
		if json.Unmarshal(raw, &o) != nil {
			return "", nil, false
		}
		for _, flag := range []json.RawMessage{o.Interrupted, o.IsImage} {
			if len(flag) > 0 && string(flag) != "false" {
				terminal = false
			}
		}
		var parsed *int
		if json.Unmarshal(o.ExitCode, &parsed) == nil {
			code = parsed
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
	complete := terminal && code != nil && len(out) <= toolOutputMax && !incompleteToolOutput.MatchString(out)
	if len(out) > toolOutputMax {
		out = out[len(out)-toolOutputMax:]
	}
	return out, code, complete
}

// These are conservative vetoes only. Program text never supplies positive
// evidence of completion or an exit status.
var incompleteToolOutput = regexp.MustCompile(`(?i)output truncated|tokens truncated|characters truncated|truncated output|output omitted|process running with session id|script running with cell id|moved to the background|command timed out`)

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
		Event      string          `json:"hook_event_name"`
		ToolName   string          `json:"tool_name"`
		Cwd        string          `json:"cwd"`
		Source     string          `json:"source"`
		SessionID  string          `json:"session_id"`
		ToolUseID  string          `json:"tool_use_id"`
		TurnID     string          `json:"turn_id"`
		AgentID    string          `json:"agent_id"`
		Transcript string          `json:"transcript_path"`
		Model      string          `json:"model"`
		Response   json.RawMessage `json:"tool_response"`
		StopHook   bool            `json:"stop_hook_active"`
		Input      struct {
			FilePath     string          `json:"file_path"`
			NotebookPath string          `json:"notebook_path"`
			Command      json.RawMessage `json:"command"`
			Background   bool            `json:"run_in_background"`
		} `json:"tool_input"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(string(raw), "\uFEFF")), &m); err != nil {
		return in
	}
	in.Event, in.ToolName, in.Cwd, in.Source = m.Event, m.ToolName, m.Cwd, m.Source
	if m.SessionID != "" {
		in.SessionID = filepath.Base(m.SessionID)
	}
	in.ToolUseID, in.TurnID = m.ToolUseID, m.TurnID
	in.Model = m.Model
	if m.AgentID != "" {
		in.AgentID = filepath.Base(m.AgentID)
	}
	in.TranscriptPath = m.Transcript
	in.ToolOutput, in.ToolExitCode, in.ToolResultComplete = toolOutput(m.Response)
	if m.Event == "PostToolUseFailure" || m.Input.Background {
		in.ToolResultComplete = false
	}
	in.FilePath = m.Input.FilePath
	if in.FilePath == "" {
		in.FilePath = m.Input.NotebookPath
	}
	in.Command = commandText(m.Input.Command)
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

// commandText is a tool's command as one shell line: the string itself, or
// an argv array joined with each word that needs it single-quoted, so the
// shell parser sees the same words. Anything else reads as no command.
func commandText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var argv []string
	if json.Unmarshal(raw, &argv) != nil {
		return ""
	}
	for i, a := range argv {
		if a == "" || strings.ContainsAny(a, " \t\n\"'\\$`|&;<>()*?[]{}~#") {
			argv[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
	}
	return strings.Join(argv, " ")
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
// which outlives compactions and clears; the kind names what happened and,
// for per-session or per-process marks, whose (the caller builds the kind).
// The mtime is the moment; the content is optional data. They live in a
// state directory that survives a reboot — the temp dir is often a tmpfs, and
// losing the marks there would make a resumed session look like it never got
// its outline: ORTG_STATE_DIR, else the user cache dir. Marks older binaries
// left in the temp dir are still read, so an upgrade resends nothing.
func overviewMarkFile(root, kind string) string {
	return filepath.Join(stateDir(), overviewMarkName(root, kind))
}

func overviewMarkName(root, kind string) string {
	return fmt.Sprintf("ortg-overview-%s-%x", kind, sha1.Sum([]byte(root)))
}

func stateDir() string {
	if d := os.Getenv("ORTG_STATE_DIR"); d != "" {
		return d
	}
	if d, err := os.UserCacheDir(); err == nil {
		return filepath.Join(d, "ortg")
	}
	return os.TempDir()
}

// MarkOverview writes root's mark of the given kind, stamped now; into the
// temp dir when the state dir cannot be written.
func MarkOverview(root, kind, data string) {
	f := overviewMarkFile(root, kind)
	err := os.MkdirAll(filepath.Dir(f), 0o700)
	if err == nil {
		err = os.WriteFile(f, []byte(data), 0o600)
	}
	if err != nil {
		Debugf("overview mark %s %s: %v", kind, root, err)
		f = filepath.Join(os.TempDir(), overviewMarkName(root, kind))
		if err := os.WriteFile(f, []byte(data), 0o600); err != nil {
			Debugf("overview mark %s %s: %v", kind, root, err)
			return
		}
	}
	now := time.Now()
	_ = os.Chtimes(f, now, now)
}

var overviewMarkMu sync.Mutex

// MutateOverviewMark serializes a read/modify/write at the canonical state
// location. It neither changes lock domains nor reclaims an existing lock;
// an interrupted owner must be repaired rather than risking stale reapers.
// A callback error or identical text leaves the mark untouched. The callback
// may use Index.Batch, but must not recursively mutate another overview mark.
func MutateOverviewMark(root, kind string, fn func(string) (string, error)) error {
	overviewMarkMu.Lock()
	defer overviewMarkMu.Unlock()
	p := overviewMarkFile(root, kind)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	unlock, err := lock(p, false)
	if err != nil {
		return err
	}
	defer unlock()
	b, err := os.ReadFile(p)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	next, err := fn(string(b))
	if err != nil || next == string(b) {
		return err
	}
	return AtomicWrite(p, []byte(next))
}

// OverviewMark returns when root's mark of the given kind was written and its
// data; the zero time when never. Of the state dir's and the temp dir's
// copies the later one counts.
func OverviewMark(root, kind string) (time.Time, string) {
	var at time.Time
	var data string
	for _, f := range []string{overviewMarkFile(root, kind), filepath.Join(os.TempDir(), overviewMarkName(root, kind))} {
		fi, err := os.Stat(f)
		if err != nil || !fi.ModTime().After(at) {
			continue
		}
		b, _ := os.ReadFile(f)
		at, data = fi.ModTime(), string(b)
	}
	return at, data
}

// PruneOverviewMarks removes root's marks whose kind starts with one of
// prefixes and that were last written longer ago than age, in the state dir
// and the temp dir: per-session and per-process marks would otherwise pile up.
func PruneOverviewMarks(root string, prefixes []string, age time.Duration) {
	cut := time.Now().Add(-age)
	for _, dir := range []string{stateDir(), os.TempDir()} {
		for _, pre := range prefixes {
			fs, _ := filepath.Glob(filepath.Join(dir, overviewMarkName(root, pre+"*")))
			for _, f := range fs {
				if fi, err := os.Stat(f); err == nil && fi.ModTime().Before(cut) {
					_ = os.Remove(f)
				}
			}
		}
	}
}

// CallSession names the context an MCP tool call belongs to. Codex puts the
// thread in every call's _meta — the model's calls and those its mcp_tool
// hooks make alike — and one Codex process may serve many threads through a
// single MCP server, so the call is the only place to learn it. The thread
// owns the context: a subagent's calls carry its own thread id, the
// agent_id of its hooks, while their session id is the parent's, so the
// session id stands in only when no thread is named. Claude Code's _meta
// names no session, but it starts an MCP server per session with
// CLAUDE_CODE_SESSION_ID in the environment: fromEnv reports that id, which
// a /clear may leave stale (a new session in the same process). nil when
// neither says.
func CallSession(meta map[string]any) (ids []string, fromEnv bool) {
	turn, _ := meta["x-codex-turn-metadata"].(map[string]any)
	for _, v := range []any{meta["threadId"], turn["thread_id"], meta["sessionId"], turn["session_id"]} {
		if s, _ := v.(string); s != "" {
			return []string{s}, false
		}
	}
	if s := os.Getenv("CLAUDE_CODE_SESSION_ID"); s != "" {
		return []string{s}, true
	}
	return nil, false
}

// ToolUseID is the tool use an MCP call answers, as Claude Code names it in
// the call's _meta; the PreToolUse hook that ran before the call names the
// same id as tool_use_id. "" when the host names none.
func ToolUseID(meta map[string]any) string {
	s, _ := meta["claudecode/toolUseId"].(string)
	return s
}

// SubagentTranscript is where Claude Code keeps a subagent's transcript,
// beside the session's: <transcript without .jsonl>/subagents/agent-<id>.jsonl,
// <id> being the agent_id its hooks and tool calls carry.
func SubagentTranscript(transcript, agent string) string {
	return filepath.Join(strings.TrimSuffix(transcript, ".jsonl"), "subagents", "agent-"+agent+".jsonl")
}

// LastCompactAt is when the context this transcript belongs to last
// compacted: the time of its last compact_boundary record (type system),
// zero when none. A compaction hook cannot say which context compacted —
// Claude Code reports a subagent's with the session's id and transcript, and
// may not have written the record yet when the hook runs — but by the
// context's next tool call its transcript has it. The file is read once and
// then only what was appended since (a transcript only grows; a shorter one
// is read again), so a call costs a stat.
func LastCompactAt(path string) time.Time {
	fi, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	compactScan.Lock()
	defer compactScan.Unlock()
	st := compactScan.m[path]
	if fi.Size() < st.size {
		st = compactState{}
	}
	if fi.Size() == st.size {
		return st.last
	}
	f, err := os.Open(path)
	if err != nil {
		return st.last
	}
	defer f.Close()
	from := st.size
	if from == 0 && fi.Size() > compactTail {
		from = fi.Size() - compactTail
	}
	b := make([]byte, fi.Size()-from)
	n, _ := f.ReadAt(b, from)
	b = b[:n]
	// Only whole lines: a record still being written is read next time.
	end := bytes.LastIndexByte(b, '\n') + 1
	for _, line := range bytes.Split(b[:end], []byte("\n")) {
		if !bytes.Contains(line, []byte(`"compact_boundary"`)) {
			continue
		}
		var r struct {
			Type, Subtype string
			Timestamp     time.Time
		}
		if json.Unmarshal(line, &r) == nil && r.Type == "system" && r.Subtype == "compact_boundary" && r.Timestamp.After(st.last) {
			st.last = r.Timestamp
		}
	}
	st.size = from + int64(end)
	compactScan.m[path] = st
	return st.last
}

// compactTail bounds a transcript's first read: a compaction further back
// than this is older than anything a running server has sent.
const compactTail = 4 << 20

type compactState struct {
	size int64
	last time.Time
}

var compactScan = struct {
	sync.Mutex
	m map[string]compactState
}{m: map[string]compactState{}}

// Ancestors lists the processes above this one, nearest first, leaving out
// itself and pid 1; nil without /proc. A hook's ancestors include the host
// process, which is also the parent of the MCP server it started.
func Ancestors() []int {
	if runtime.GOOS != "linux" {
		return nil
	}
	var out []int
	seen := map[string]bool{}
	for pid := procParent(strconv.Itoa(os.Getpid())); pid != "" && pid != "0" && pid != "1" && !seen[pid]; pid = procParent(pid) {
		seen[pid] = true
		if n, err := strconv.Atoi(pid); err == nil {
			out = append(out, n)
		}
	}
	return out
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
