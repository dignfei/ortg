package main

// logic.go holds every product decision; it only talks to base.Index and
// base hook I/O and never touches files, protocol or storage format.

import (
	"crypto/sha1"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"ortg/base"
	"ortg/migrate"
)

const version = "0.5.30"

var (
	excludeDirs  = []string{".git", "build"}
	excludeFiles = []string{"ortg.tsv", "ortg.tsv.*", "overview*.txt", "overview.txt.bak*"}
	localExclude = []string{"ortg.tsv.*", "overview.txt.bak*"}
)

const maxFileSize = 4 << 20

var reconcileOpts = base.ReconcileOptions{ExcludeDirs: excludeDirs, ExcludeFiles: excludeFiles, MaxSize: maxFileSize}

var errNoTable = errors.New("仓库没有 ortg.tsv，请先调用 ortg_overview 建表")

// ---- states ----

type state int

const (
	stAligned state = iota
	stSkeleton
	stStale
	stPending
	stPlanned
	stTombstone
	stIgnored
	stObserved
	stObserveDrift
)

var stateNames = map[state]string{stAligned: "对齐", stSkeleton: "无纲要", stStale: "过期", stPending: "待执行", stPlanned: "计划新建", stTombstone: "已删除待确认", stIgnored: "忽略", stObserved: "观察", stObserveDrift: "观察变动"}

// classify is the state table (docs/rules.md); order: 无纲要 > 过期 > 待执行 > 对齐.
func classify(f base.Fact) state {
	switch {
	case f.Ignored:
		return stIgnored
	case f.Observed && f.Exists:
		// The middle role: fingerprinted so a change still surfaces, never
		// asked for an outline. A changed fingerprint is the only thing worth
		// reporting, and acknowledging it is ortg_align, not a gate.
		if f.FingerprintChanged {
			return stObserveDrift
		}
		return stObserved
	case f.IsMissing:
		if f.NeverSeen && f.HasTarget {
			return stPlanned
		}
		return stTombstone
	case f.IsNew || f.OutlineEmpty:
		return stSkeleton
	case f.FingerprintChanged && f.OutlineOlderThanFile:
		return stStale
	case f.HasTarget || f.TargetDelete:
		return stPending
	default:
		return stAligned
	}
}

type summary struct {
	byState map[state][]string
	purged  int
}

// apply performs the writes each fact implies; must run inside Batch.
func apply(x *base.Index, facts []base.Fact) (summary, error) {
	s := summary{byState: map[state][]string{}}
	for _, f := range facts {
		st := classify(f)
		s.byState[st] = append(s.byState[st], f.Path)
		if err := x.SetIgnored(f.Path, f.Ignored); err != nil {
			return s, err
		}
		var err error
		switch {
		case st == stIgnored:
		case st == stObserved || st == stObserveDrift:
			// Record the fingerprint so drift can be seen next time; the row is
			// never turned into a skeleton, which would demand an outline.
			if f.IsNew {
				err = x.AddSkeleton(f.Path)
			}
		case st == stSkeleton:
			err = x.AddSkeleton(f.Path)
		case f.Exists && f.FingerprintChanged && !f.OutlineOlderThanFile:
			err = x.Align([]string{f.Path})
		}
		if err == nil && st != stIgnored && st != stObserved && st != stObserveDrift {
			err = x.SetDeleted(f.Path, st == stTombstone)
		}
		if err != nil {
			return s, err
		}
	}
	gone, err := x.Purge()
	s.purged = len(gone)
	for _, p := range gone {
		s.byState[stTombstone] = without(s.byState[stTombstone], p)
	}
	return s, err
}

func without(list []string, p string) []string {
	out := list[:0]
	for _, v := range list {
		if v != p {
			out = append(out, v)
		}
	}
	return out
}

// normRel turns a caller path (relative or absolute) into a repo-relative slash path.
func normRel(root, rel string) (string, error) {
	if filepath.IsAbs(rel) {
		r, ok := base.Rel(root, rel)
		if !ok {
			return "", fmt.Errorf("%s 不在仓库 %s 内", rel, root)
		}
		return r, nil
	}
	return strings.TrimPrefix(filepath.ToSlash(filepath.Clean(rel)), "./"), nil
}

// stripBodyMarker drops the render-only body marker so re-importing a
// rendered document does not accumulate it in the header. Older document
// formats are the migrate package's business.
func stripBodyMarker(doc string) string {
	lines := strings.Split(doc, "\n")
	out := lines[:0]
	for _, l := range lines {
		if strings.TrimSpace(l) != bodyMarker {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

// oneLine is the report squeezed into the line a human sees at session start.
func (s summary) oneLine() string {
	var parts []string
	for _, st := range []state{stSkeleton, stStale, stPending, stPlanned, stTombstone, stObserveDrift} {
		if n := len(s.byState[st]); n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", stateNames[st], n))
		}
	}
	parts = append(parts, fmt.Sprintf("对齐 %d", len(s.byState[stAligned])))
	if n := len(s.byState[stObserved]) + len(s.byState[stObserveDrift]); n > 0 {
		parts = append(parts, fmt.Sprintf("观察 %d", n))
	}
	parts = append(parts, fmt.Sprintf("忽略 %d", len(s.byState[stIgnored])))
	return strings.Join(parts, " · ")
}

// needsDecision reports whether anything is waiting on the user's call.
func (s summary) needsDecision() bool {
	return len(s.byState[stSkeleton])+len(s.byState[stStale])+len(s.byState[stPending])+len(s.byState[stPlanned]) > 0
}

func (s summary) report() string {
	var b strings.Builder
	b.WriteString("## 对账报告\n")
	for _, st := range []state{stSkeleton, stStale, stPending, stPlanned, stTombstone, stObserveDrift} {
		list := s.byState[st]
		if len(list) == 0 {
			continue
		}
		sort.Strings(list)
		fmt.Fprintf(&b, "- %s %d: %s\n", stateNames[st], len(list), strings.Join(list, ", "))
	}
	fmt.Fprintf(&b, "- 对齐 %d, 观察 %d, 忽略 %d, 已清除 %d\n", len(s.byState[stAligned]), len(s.byState[stObserved])+len(s.byState[stObserveDrift]), len(s.byState[stIgnored]), s.purged)
	if s.needsDecision() {
		b.WriteString("\n" + decisionText + "\n")
	} else {
		b.WriteString("\n纲要与代码已对齐。\n")
	}
	return b.String()
}

// ---- repository access ----

func repoRoot() string {
	wd, _ := os.Getwd()
	return base.FindRepoRoot(wd)
}

// openTable opens root's table; one an older ORTG wrote is first upgraded in
// place by migrate. Only entry points that may write call it: the read-only
// hooks and the gate open the table directly and stay silent on an old one.
func openTable(root string) (*base.Index, error) {
	x, err := base.Open(root)
	if errors.Is(err, base.ErrFormat) {
		if up, merr := migrate.Table(filepath.Join(root, base.TableName)); merr == nil && up {
			x, err = base.Open(root)
		}
	}
	return x, err
}

func openIndex(root string) (*base.Index, error) {
	x, err := openTable(root)
	if errors.Is(err, base.ErrNoTable) {
		return nil, errNoTable
	}
	if err != nil {
		return nil, err
	}
	return x, nil
}

// backupNote and backupName render the backup for the success and the failure
// note. The legacy document is moved aside so no stale copy of the outline
// stays where a model would read or grep it.
func backupNote(backup string) string {
	if backup == "" {
		return ""
	}
	return fmt.Sprintf("；旧 overview.txt 已重命名为 %s，纲要从此只在 ortg.tsv，用 ortg_overview 读", backup)
}

func backupName(backup string) string {
	if backup == "" {
		return "(重命名失败，仍是 overview.txt)"
	}
	return backup
}

// spawnGraph brings the CBM daemon up and indexes this root's graph, detached,
// since a cold daemon alone takes longer than the host gives a hook.
func spawnGraph(x *base.Index) {
	if x.GraphEnabled() {
		if err := x.Spawn("graph"); err != nil {
			base.Debugf("spawn graph: %v", err)
		}
	}
}

// openOrBootstrap opens the table, or creates it: from the outline documents
// an older ORTG left at the root (migrate.Import; every entry becomes a target
// outline), else from the template.
func openOrBootstrap(root string) (*base.Index, string, error) {
	x, err := openTable(root)
	if err == nil {
		return x, "", nil
	}
	if !errors.Is(err, base.ErrNoTable) {
		return nil, "", err
	}
	// The import's save backs up the fresh template as ortg.tsv.bak, which
	// would clobber the backup a user rebuilding a lost table may restore
	// from; keep what was there, and drop the template copy otherwise.
	bak := filepath.Join(root, base.TableName+".bak")
	prevBak, bakErr := os.ReadFile(bak)
	defer func() {
		if bakErr == nil {
			_ = os.WriteFile(bak, prevBak, 0o644)
		} else {
			_ = os.Remove(bak)
		}
	}()
	x, err = base.Create(root, headerTemplate)
	if err != nil {
		return nil, "", err
	}
	r := migrate.Import(root, headerTemplate, func(doc string) (base.ImportResult, error) {
		var res base.ImportResult
		err := x.Batch(func() error { // rolls back on error: the table stays empty but valid
			var e error
			res, e = x.ImportOutlineWith(stripBodyMarker(doc), base.ImportOptions{
				Excluded: func(rel string) bool { return x.Excluded(rel, reconcileOpts) },
			})
			return e
		})
		return res, err
	})
	note := emptyTableNote
	switch {
	case !r.Found:
	case r.Err != nil:
		note = fmt.Sprintf(migrateHint, r.Err, backupName(r.Backup))
	case r.FromV1:
		note = fmt.Sprintf("已从 ORTG v1 分卷索引升级建表：%d 个目录段、%d 条纲要", r.Res.Sections, r.Res.Entries)
		note += missingNote(r.Res.Missing) + "（模块卷本轮未迁移）" + backupNote(r.Backup)
	default:
		note = fmt.Sprintf("已从 overview.txt 建表：%d 个目录段、%d 条纲要", r.Res.Sections, r.Res.Entries)
		note += missingNote(r.Res.Missing) + backupNote(r.Backup)
	}
	_ = base.ExcludeLocal(root, localExclude)
	// A table made mid-session missed the session hook's graph build; without
	// it the first edit has no pre-edit graph to find broken callers in.
	spawnGraph(x)
	return x, note, nil
}

// renderOpts renders every non-ignored row, or only one module when given.
func renderOpts(x *base.Index, module string) base.RenderOptions {
	return base.RenderOptions{
		Include: func(r base.Row) bool {
			return !r.Ignored && (isDirRow(r.Outline) || !x.Observed(r.Path, reconcileOpts)) &&
				(module == "" || module == "*" || strings.EqualFold(moduleKey(r), module))
		},
		DirTitle: x.Config().Dirs,
		Prefix:   base.Prefixes{Deleted: prefixDeleted, Planned: prefixPlanned, Target: prefixTarget, TargetDelete: prefixTargetDelete, TargetSame: prefixTargetSame, Body: bodyMarker},
	}
}

// isDirRow reports a folder outline: an ordinary table row whose outline names
// a directory ("vendored/[标签]: ..."), written for a directory kept out of
// per-file outlines (third-party code) and tagged with the module that uses
// it, so it travels with that module like any file.
func isDirRow(outline string) bool {
	i := strings.IndexByte(outline, '[')
	return i > 0 && outline[i-1] == '/'
}

// reconcileAndApply runs one full reconciliation inside a batch.
func reconcileAndApply(x *base.Index) (summary, error) {
	var s summary
	err := x.Batch(func() error {
		facts, err := x.Reconcile(reconcileOpts)
		if err != nil {
			return err
		}
		s, err = apply(x, facts)
		return err
	})
	return s, err
}

// ---- tools ----

// moduleKey is the module a row is listed and fetched under: its B tag, or
// "-" for a row without one (a skeleton, or an entry whose tag is broken).
func moduleKey(r base.Row) string {
	if r.Module == "" {
		return "-"
	}
	return r.Module
}

// Overview reconciles and sends the outline the context lacks: a bare call
// (module "") the whole of it within the budget (see limits), else the header and
// the module list; module "*" all of it regardless; any other one module.
func Overview(module string, force bool) (string, error) {
	root := repoRoot()
	x, note, err := openOrBootstrap(root)
	if err != nil {
		return "", err
	}
	s, err := reconcileAndApply(x)
	if err != nil {
		return "", err
	}
	overviewMu.Lock()
	defer overviewMu.Unlock()
	c := sentFor(x.Root())
	defer saveSent(x.Root(), c)
	first := "" // a fresh table: the docs come before any outline
	if headerUnwritten(x.Header()) {
		first = headerFirstText(x) + "\n\n"
	}
	opts := renderOpts(x, module)
	bare, whole := module == "", module == "" || module == "*"
	scope := "整份纲要"
	if !whole {
		scope = "模块 " + module + " "
	}
	var cand, fresh []base.Row
	for _, r := range x.Rows() {
		if !opts.Include(r) {
			continue
		}
		cand = append(cand, r)
		if force || c.rows[r.Path] != rowPrint(r) {
			fresh = append(fresh, r)
		}
	}
	header := x.Header()
	headerFresh := force || c.header != header
	var b strings.Builder
	b.WriteString(first)
	if note != "" {
		b.WriteString("# " + note + "\n\n")
	}
	lim := limits()
	rendered := x.Render(opts)
	size, tokens := outlineSize(rendered), estTokens(rendered)
	over := ""
	if size > lim.whole {
		over = fmt.Sprintf(overviewBudgetCap, lim.whole, lim.unit)
	} else if half := contextWindow(root) * wholeShareOfWindow / 100; half > 0 && tokens > half {
		over = fmt.Sprintf(overviewBudgetWindow, half*100/wholeShareOfWindow)
	}
	if bare && over != "" && len(fresh) > 0 {
		// Too large to hand over whole: the map, not the territory — the
		// header and the module list; the model then fetches the modules its
		// question needs. Below the budget a bare call sends everything.
		if headerFresh {
			b.WriteString(header + "\n\n")
			c.header = header
		}
		sized := fmt.Sprintf(overviewSizeBoth, size, lim.unit, tokens)
		if base.Host() == "codex" {
			sized = fmt.Sprintf(overviewSizeEst, tokens)
		}
		fmt.Fprintf(&b, overviewModulesHead, sized, len(x.Rows()), over)
		b.WriteString(moduleList(x, opts.Include, c))
		if headerFresh {
			b.WriteString(gateOverview(x))
		}
		b.WriteString("\n" + s.report() + "\n" + overviewModulesFooter)
		return b.String(), nil
	}
	if len(fresh) == 0 && !headerFresh {
		return first + fmt.Sprintf(overviewAlreadyRead, scope, len(cand)) + "\n" + s.report() + "\n" + overviewShortFooter, nil
	}
	sub := opts
	set := make(map[string]bool, len(fresh))
	for _, r := range fresh {
		set[r.Path] = true
	}
	sub.Include = func(r base.Row) bool { return set[r.Path] }
	sub.OmitHeader = !headerFresh
	body := x.Render(sub)
	if n := outlineSize(body); n > lim.max-lim.margin {
		// Sent anyway: even a truncated part lets the model work out how to
		// split the module. The warning leads so a preview still carries it,
		// and nothing is recorded as sent, since the host may have cut it.
		b.WriteString(fmt.Sprintf(overviewTooLarge, scope, n, lim.unit, lim.max, lim.unit) + "\n\n")
	} else {
		for _, r := range fresh {
			c.rows[r.Path] = rowPrint(r)
		}
	}
	c.header = header
	b.WriteString(body)
	if headerFresh {
		b.WriteString(gateOverview(x))
	}
	b.WriteString("\n" + s.report())
	if skipped := len(cand) - len(fresh); skipped > 0 {
		b.WriteString("\n" + fmt.Sprintf(overviewIncrementNote, len(fresh), skipped))
	}
	if whole {
		scope = "整份" // "以上是整份的纲要"
	}
	b.WriteString("\n" + fmt.Sprintf(overviewPartFooter, scope))
	return b.String(), nil
}

// gateOverview is the version-gate note that goes out with the header, ""
// when the repository has no gate in force.
func gateOverview(x *base.Index) string {
	var live []versionGate
	for _, g := range requiredGates(x) {
		if !g.Lifted {
			live = append(live, g)
		}
	}
	if len(live) == 0 {
		return ""
	}
	return "\n" + fmt.Sprintf(versionGateOverview, gateLines(live)) + "\n"
}

// The outline is the largest thing ORTG sends, and whatever it sends stays in
// the model's context until a compaction or a clear. overviewSent remembers,
// per repository root, which entries this MCP process has handed over and in
// which version, so every fetch sends only what the context lacks or what has
// changed since. The cache is dropped when the session hook's reset mark
// (compact/clear) is newer than the cache. It is also saved under the temp
// dir on every change: a host that resumes the conversation in a new process
// (claude -p --resume) starts a new MCP server whose context still holds what
// the old one sent, so the new one loads the saved cache — unless a start mark
// (a session that did not resume) or a reset mark is newer. The gate (ortg_gate) reads the same cache: it runs
// in this process too, as a hook the host calls over the MCP connection, so
// what was sent here is what this conversation holds — no session id needed.
var (
	overviewMu   sync.Mutex
	overviewSent = map[string]*sentCache{}
)

type sentCache struct {
	born   time.Time         // compared with the reset mark
	header string            // header last sent; "" = nothing loaded yet
	rows   map[string]string // path -> rowPrint at send time
	denied bool              // the gate has refused a call once
	nagged map[string]bool   // file or "module:X" the gate reminded about
	// The write gate: each ortg_review records the plan it saw; two in a
	// row seeing the same plan unlock its files for the rest of the context.
	reviews    int
	lastReview string
	planned    map[string]bool
	// solo is the files written outside any plan whose outline has not been
	// submitted yet: one such file is a single-file change and passes; a
	// second one makes it a multi-file change, which needs a plan.
	solo map[string]bool
	// What the reviews of the current plan already said, so a later review
	// of the same plan sends only what changed: each file's target as last
	// shown, the contracts and fallback entries quoted in full, the affected
	// files named, and whether the conflict text and the checklist went out.
	plan planSeen
}

// planSeen is what the reviews of one plan have shown; the zero value is a
// plan not reviewed yet.
type planSeen struct {
	Targets   map[string]string // path → target as shown
	Quoted    map[string]bool   // contract keys, and "rel:"+line fallback entries
	Affected  map[string]bool
	Conflict  bool
	Checklist bool
}

// sentFor returns root's cache, starting over when the reset mark is newer:
// the context lost what was sent, and the gate re-arms with it. Callers hold
// overviewMu.
func sentFor(root string) *sentCache {
	c := overviewSent[root]
	resetAt, _ := base.OverviewMark(root, "reset")
	if c == nil {
		c = loadSent(root, resetAt)
	}
	if c == nil || !c.born.After(resetAt) {
		c = &sentCache{born: time.Now(), rows: map[string]string{}, nagged: map[string]bool{}, planned: map[string]bool{}, solo: map[string]bool{}}
	}
	overviewSent[root] = c
	return c
}

// sentFile is sentCache as saved between processes.
type sentFile struct {
	Born       time.Time
	Header     string
	Rows       map[string]string
	Denied     bool
	Nagged     map[string]bool
	Reviews    int
	LastReview string
	Planned    map[string]bool
	Solo       map[string]bool
	Plan       planSeen
}

// saveSent writes root's cache to its "sent" mark. Callers hold overviewMu.
func saveSent(root string, c *sentCache) {
	b, err := json.Marshal(sentFile{c.born, c.header, c.rows, c.denied, c.nagged, c.reviews, c.lastReview, c.planned, c.solo, c.plan})
	if err != nil {
		return
	}
	base.MarkOverview(root, "sent", string(b))
}

// loadSent returns the saved cache when it still describes this context: it
// was born after the last reset and after the last session start that did
// not resume; nil otherwise.
func loadSent(root string, resetAt time.Time) *sentCache {
	_, data := base.OverviewMark(root, "sent")
	if data == "" {
		return nil
	}
	var f sentFile
	if json.Unmarshal([]byte(data), &f) != nil {
		return nil
	}
	startAt, _ := base.OverviewMark(root, "start")
	if !f.Born.After(resetAt) || !f.Born.After(startAt) {
		return nil
	}
	c := &sentCache{born: f.Born, header: f.Header, rows: f.Rows, denied: f.Denied, nagged: f.Nagged,
		reviews: f.Reviews, lastReview: f.LastReview, planned: f.Planned, solo: f.Solo, plan: f.Plan}
	if c.rows == nil {
		c.rows = map[string]string{}
	}
	if c.nagged == nil {
		c.nagged = map[string]bool{}
	}
	if c.planned == nil {
		c.planned = map[string]bool{}
	}
	if c.solo == nil {
		c.solo = map[string]bool{}
	}
	return c
}

// Gate answers the PreToolUse hook the host runs as a call to the hidden MCP
// tool ortg_gate, before the model reads, searches or writes code. Until the
// context has loaded the outline it refuses the first such call, then adds a
// reminder once per file: a refusal makes the model re-plan, where a plain
// reminder is as easy to skip as the session hook's. Once loaded it stays
// silent, except once per module whose entries the context still lacks (a
// large outline is fetched module by module). Writes are gated harder: a file
// may be written only once it belongs to a plan — its target outline written
// with ortg_target and checked by ortg_review until two checks in a row saw
// the same plan — unless the change touches a single file: a file outside
// any plan may be written while no other unplanned file waits for its
// outline, and a second one is refused until the plan lists them all.
// Observe-tier files hold no outline and are never gated. Subagents (agent
// set) are left alone: their context is not the session's, and this cache is.
func Gate(tool, path, dir, command, agent string) (string, error) {
	if agent != "" {
		// A subagent's broad test run is still a fact about the work tree.
		if tool == "Bash" {
			if broad, _ := testRunKind(command, repoRoot()); broad {
				if x, err := base.Open(repoRoot()); err == nil {
					recordBroadRun(x, command)
				}
			}
		}
		return "", nil
	}
	root := repoRoot()
	x, err := base.Open(root)
	if err != nil {
		return "", nil // no table here, or unreadable: ORTG is off
	}
	broad, commit := false, false
	if tool == "Bash" {
		broad, commit = testRunKind(command, x.Root())
		if commit && !broad {
			if s := verifyCommit(x, stagesChanges(command, x.Root())); s != "" {
				return s, nil
			}
		}
	}
	out := gateCall(x, tool, path, dir, command)
	// A run counts once the command is let through: a refused one never ran.
	if broad && !strings.Contains(out, deniedMark) {
		recordBroadRun(x, command)
	}
	// Staging run on its own after a refusal: the next commit need not stage.
	if tool == "Bash" && !commit && !strings.Contains(out, deniedMark) && stagesChanges(command, x.Root()) {
		clearRestage(x)
	}
	return out, nil
}

// deniedMark is how a refusal from base.PreToolDecision reads.
const deniedMark = `"permissionDecision":"deny"`

// gateCall is Gate for the session's own calls once commits are checked: the
// write gate and the read gate.
func gateCall(x *base.Index, tool, path, dir, command string) string {
	targets, writes := gateTargets(x, tool, path, dir, command)
	if len(targets) == 0 && len(writes) == 0 {
		return ""
	}
	overviewMu.Lock()
	defer overviewMu.Unlock()
	c := sentFor(x.Root())
	n0 := len(c.solo) + len(c.nagged)
	d0 := c.denied
	defer func() {
		if len(c.solo)+len(c.nagged) != n0 || c.denied != d0 {
			saveSent(x.Root(), c)
		}
	}()
	if len(writes) > 0 {
		// Writing is gated every time, not once: code changes only after
		// the plan for them has been written and checked to a fixpoint.
		if c.header == "" {
			return base.PreToolDecision(gateWriteUnloaded, true)
		}
		_, targeted := planSnapshot(x)
		var unplanned []string
		for _, w := range writes {
			if c.planned[w] || x.Observed(w, reconcileOpts) || isGateFile(x, w) {
				continue // lifting a version gate for a run is test plumbing
			}
			if targeted[w] {
				return base.PreToolDecision(fmt.Sprintf(gatePlanUnreviewed, w), true)
			}
			unplanned = append(unplanned, w)
		}
		pending := map[string]bool{}
		for p := range c.solo {
			pending[p] = true
		}
		for _, w := range unplanned {
			pending[w] = true
		}
		if len(pending) > 1 {
			names := make([]string, 0, len(pending))
			for p := range pending {
				names = append(names, p)
			}
			sort.Strings(names)
			return base.PreToolDecision(fmt.Sprintf(gateMultiFile, strings.Join(names, "、")), true)
		}
		for _, w := range unplanned {
			if !c.solo[w] {
				c.solo[w] = true
				return base.PreToolDecision(fmt.Sprintf(gateSoloEdit, w), false)
			}
		}
		for _, w := range writes {
			if isTestPath(w) && !c.nagged["test:"+w] {
				c.nagged["test:"+w] = true
				return base.PreToolDecision(fmt.Sprintf(gateTestEdit, w), false)
			}
		}
		return ""
	}
	if c.header == "" {
		if !c.denied {
			c.denied = true
			return base.PreToolDecision(gateDeny, true)
		}
		if c.nagged[targets[0]] {
			return ""
		}
		c.nagged[targets[0]] = true
		what := targets[0]
		if what == "." {
			what = "本仓库代码" // a search over the whole repository
		}
		return base.PreToolDecision(fmt.Sprintf(gateRemind, what), false)
	}
	for _, p := range targets {
		row, ok := x.Row(p)
		if !ok || row.Outline == "" || c.rows[p] != "" {
			continue
		}
		if row.Module == "" { // a tag without a B part: the entry is broken
			if c.nagged[p] {
				continue
			}
			c.nagged[p] = true
			return base.PreToolDecision(fmt.Sprintf(gateBadOutline, p), false)
		}
		if c.nagged["module:"+row.Module] {
			continue
		}
		c.nagged["module:"+row.Module] = true
		return base.PreToolDecision(fmt.Sprintf(gateModule, p, row.Module), false)
	}
	return ""
}

// verifyCommit keeps a commit from landing code no broad test run has seen.
// Targeted runs (one file, -k, a subdirectory, the collection config
// overridden) miss what a change breaks elsewhere — a name another package's
// tests import, a check the whole suite runs on every component — and a
// version gate in force hides a whole category from every local run. So a
// commit is refused, once per HEAD, while code changed after the last broad
// run, or while a gate in force was never run lifted (nor acknowledged: the
// commit that follows a refusal naming it counts as acknowledging it — the
// condition may not hold here). A command that runs the broad suite and
// commits passes. Silent until the outline is loaded, outside git, and
// without code changes.
//
// A refusal stops the whole command, so its staging (git add …) never ran:
// the refusal says so, and when the commit that follows at the same commit
// point stages nothing — no staging of its own, none run in between — it is
// refused once more, or only what was staged before would land.
func verifyCommit(x *base.Index, stages bool) string {
	gates := requiredGates(x)
	overviewMu.Lock()
	defer overviewMu.Unlock()
	if sentFor(x.Root()).header == "" {
		return ""
	}
	changed, ok := x.ChangedFiles()
	if !ok {
		return ""
	}
	var newest time.Time
	for p, t := range changed {
		if codeFile(p) && !x.Excluded(p, reconcileOpts) && t.After(newest) {
			newest = t
		}
	}
	if newest.IsZero() {
		return "" // nothing of code to commit
	}
	v := loadVerify(x.Root())
	sha, _, _ := x.HeadCommit()
	if v.Denied[sha] {
		switch v.Restage[sha] {
		case restageOwed:
			if !stages {
				v.Restage[sha] = restageWarned
				saveVerify(x.Root(), v)
				return base.PreToolDecision(gateVerifyRestage, true)
			}
			delete(v.Restage, sha)
			saveVerify(x.Root(), v)
		case restageWarned:
			delete(v.Restage, sha)
			saveVerify(x.Root(), v)
		}
		if keys := v.DeniedGates[sha]; len(keys) > 0 {
			for _, k := range keys {
				if v.GateRuns[k].IsZero() {
					v.GateRuns[k] = time.Now() // acknowledged
				}
			}
			delete(v.DeniedGates, sha)
			saveVerify(x.Root(), v)
		}
		return ""
	}
	var pending, inForce []versionGate
	for _, g := range gates {
		if g.Lifted {
			continue
		}
		inForce = append(inForce, g)
		if v.GateRuns[g.key()].IsZero() {
			pending = append(pending, g)
		}
	}
	stale := newest.After(v.BroadRun)
	if !stale && len(pending) == 0 {
		return ""
	}
	v.Denied[sha] = true
	for _, g := range pending {
		v.DeniedGates[sha] = append(v.DeniedGates[sha], g.key())
	}
	if stages {
		v.Restage[sha] = restageOwed
	}
	saveVerify(x.Root(), v)
	deny := func(s string) string {
		if stages {
			s += "\n" + gateVerifyWhole
		}
		return base.PreToolDecision(s, true)
	}
	switch {
	case !stale:
		return deny(fmt.Sprintf(gateVerifyGatesOnly, gateLines(pending)) + gateLiftHowTo)
	case len(pending) > 0:
		return deny(gateVerifyCore + "\n" + fmt.Sprintf(gateVerifyGates, gateLines(pending)) + gateLiftHowTo)
	case len(inForce) > 0:
		return deny(gateVerifyCore + "\n" + fmt.Sprintf(gateVerifyGatesNudge, gateLines(inForce)))
	}
	return deny(gateVerifyCore + gateVerifyVersionHint)
}

// Restage states of a refused commit point: the refused command staged
// (owed), and the reminder for a commit without staging was given (warned).
const (
	restageOwed   = 1
	restageWarned = 2
)

// clearRestage records that staging ran on its own after a refusal: the
// commit that follows need not stage again.
func clearRestage(x *base.Index) {
	sha, _, _ := x.HeadCommit()
	overviewMu.Lock()
	defer overviewMu.Unlock()
	v := loadVerify(x.Root())
	if _, ok := v.Restage[sha]; ok {
		delete(v.Restage, sha)
		saveVerify(x.Root(), v)
	}
}

// verifyState is what verifyCommit remembers per repository: when the last
// broad test run started, the commit points already refused (and the gates
// a refusal named), and per version gate when a broad run last saw it lifted
// or a commit acknowledged it. All are facts about the work tree, not about
// what the context holds, so they live in their own mark and survive
// compaction, clearing and new sessions, which reset sentCache.
type verifyState struct {
	BroadRun    time.Time
	Denied      map[string]bool      // HEAD sha → refused once
	DeniedGates map[string][]string  // HEAD sha → gate keys its refusal named
	GateRuns    map[string]time.Time // versionGate key → lifted run or acknowledgement
	Restage     map[string]int       // HEAD sha → restageOwed or restageWarned
}

func loadVerify(root string) verifyState {
	var v verifyState
	if _, data := base.OverviewMark(root, "verify"); data != "" {
		json.Unmarshal([]byte(data), &v)
	}
	if v.Denied == nil {
		v.Denied = map[string]bool{}
	}
	if v.DeniedGates == nil {
		v.DeniedGates = map[string][]string{}
	}
	if v.GateRuns == nil {
		v.GateRuns = map[string]time.Time{}
	}
	if v.Restage == nil {
		v.Restage = map[string]int{}
	}
	return v
}

func saveVerify(root string, v verifyState) {
	if b, err := json.Marshal(v); err == nil {
		base.MarkOverview(root, "verify", string(b))
	}
}

// recordBroadRun records a broad test run starting now, and for each version
// gate lifted in the work tree right now — or edited in place by the same
// command, before its test run — that this run saw it lifted.
func recordBroadRun(x *base.Index, command string) {
	gates := versionGates(x)
	overviewMu.Lock()
	defer overviewMu.Unlock()
	v := loadVerify(x.Root())
	v.BroadRun = time.Now()
	for _, g := range gates {
		if g.Lifted || editsInPlace(command, g.File) {
			v.GateRuns[g.key()] = v.BroadRun
		}
	}
	saveVerify(x.Root(), v)
}

// bgRun is the last broad test run a session left running (in the
// background or detached): when it started, in which session, and the words
// it runs (wrappers stripped).
type bgRun struct {
	At      time.Time
	Session string
	Words   []string
}

// bgRunMaxAge is how long a recorded background run is worth looking for.
const bgRunMaxAge = 12 * time.Hour

func recordBgRun(root, session string, words []string) {
	if b, err := json.Marshal(bgRun{At: time.Now(), Session: session, Words: words}); err == nil {
		base.MarkOverview(root, "bgrun", string(b))
	}
}

// stopNote is the Stop hook's objection: a broad test run this session left
// running is still going and code changes are uncommitted, so ending the
// turn now ends a non-interactive session before the run reports — the
// waiting turn never resumes. Cheap checks first; the table and git status
// only when the run is alive. Said once per run and session, never while a
// Stop hook already kept the turn going, never without a session.
func stopNote(root string, in base.HookInput) string {
	if in.StopHookActive || in.SessionID == "" {
		return ""
	}
	_, data := base.OverviewMark(root, "bgrun")
	var b bgRun
	if data == "" || json.Unmarshal([]byte(data), &b) != nil || b.Session != in.SessionID || time.Since(b.At) > bgRunMaxAge {
		return ""
	}
	if !base.ProcessAlive(root, b.Words, b.At) {
		base.MarkOverview(root, "bgrun", "") // finished: nothing left to look for
		return ""
	}
	x, err := base.Open(root)
	if err != nil {
		return ""
	}
	changed, ok := x.ChangedFiles()
	if !ok {
		return ""
	}
	pending := false
	for p := range changed {
		if codeFile(p) && !x.Excluded(p, reconcileOpts) {
			pending = true
			break
		}
	}
	if !pending || !base.InjectOnce(in.SessionID, fmt.Sprintf("stopbg:%d", b.At.UnixNano())) {
		return ""
	}
	return fmt.Sprintf(stopBgRunNote, strings.Join(b.Words, " "))
}

// editsInPlace reports whether a shell command edits file in place with
// sed -i or perl -i (a lift and its test run in one command).
func editsInPlace(command, file string) bool {
	for _, seg := range expandedSegments(command, 0) {
		w := unwrapCommand(dropRedirects(seg.words))
		if len(w) == 0 || (filepath.Base(w[0]) != "sed" && filepath.Base(w[0]) != "perl") {
			continue
		}
		inPlace, names := false, false
		for _, a := range w[1:] {
			switch {
			case strings.HasPrefix(a, "-i") || strings.HasPrefix(a, "--in-place") || strings.HasPrefix(a, "-pi"):
				inPlace = true
			case a == file || strings.HasSuffix(filepath.ToSlash(a), "/"+file):
				names = true
			}
		}
		if inPlace && names {
			return true
		}
	}
	return false
}

// versionGate is a branch of the test configuration that skips a whole
// category of tests when a dependency's version compares a certain way: in
// an environment where it holds, those tests never run locally, while CI or
// a configuration that drops the branch runs them — a blind spot no local
// run reveals (ortg cannot tell whether it holds here; the model can).
type versionGate struct {
	File     string // repository-relative conftest path
	Line     int    // line of the condition, in HEAD's version when HEAD has it
	Cond     string // the condition line, trimmed, comment dropped
	Reason   string // the skip reason the block gives, "" when none
	Category string
	Fn       string // the enclosing pytest hook, "" at module level
	Lifted   bool   // HEAD's condition line is gone from the work tree
}

// key identifies a gate by where it acts, not by its text: editing or
// flipping the condition keeps the gate the same one.
func (g versionGate) key() string { return g.File + "\x00" + g.Fn + "\x00" + g.Category }

// versionGateMax bounds the gates one note lists.
const versionGateMax = 8

var (
	pyCompareRe  = regexp.MustCompile(`<=|>=|<|>|==|!=`)
	pyDefLineRe  = regexp.MustCompile(`^(\s*)(?:async\s+)?def\s+(\w+)`)
	pyReasonRe   = regexp.MustCompile(`(?:reason\s*=\s*\(?\s*|SkipTest\(\s*|skip\(\s*)[rRbBuUfF]*["']([^"'\n]{3,})`)
	pySkipActRe  = regexp.MustCompile(`\bskip|SkipTest|collect_ignore`)
	pyDepVerRe   = regexp.MustCompile(`__version__|[A-Za-z0-9]_version\b|Version\(|parse_version\(|version\.parse\(`)
	pyNotDepGate = regexp.MustCompile(`(?i)sys\.version_info|sys\.platform|platform\.|os\.name|32bit|maxsize|\bpy(thon)?_?version`)
	pyPerItemRe  = regexp.MustCompile(`\bitem\.|fspath|nodeid|basename|\.name\s*==`)
	pyDeadCondRe = regexp.MustCompile(`^(?:el)?if\s+(?:False|0)\s+and\b|\band\s+(?:False|0)\s*:$`)
)

// versionGates finds the version gates of the repository's conftest.py
// files: an if/elif comparing a dependency's version (not the interpreter's,
// not the platform, not one test's) whose block skips, inside a pytest hook
// (def pytest_…) or at module level feeding collect_ignore. Each file is
// read at HEAD and in the work tree: a gate whose HEAD condition line is
// gone from the work tree is lifted; a line only in the work tree that sits
// where a lifted gate sits is that gate's edited form, not a new gate.
func versionGates(x *base.Index) []versionGate {
	var out []versionGate
	for _, r := range x.Rows() {
		if r.Deleted || r.Ignored || path.Base(r.Path) != "conftest.py" {
			continue
		}
		cur := ""
		if b, err := os.ReadFile(filepath.Join(x.Root(), filepath.FromSlash(r.Path))); err == nil {
			cur = string(b)
		}
		atHead, lifted := map[string]bool{}, map[string]bool{}
		if head, ok := x.HeadContent(r.Path); ok {
			for _, g := range scanPyGates(head) {
				g.File, g.Lifted = r.Path, !hasLine(cur, g.Cond)
				atHead[g.Cond] = true
				if g.Lifted {
					lifted[g.key()] = true
				}
				out = append(out, g)
			}
		}
		for _, g := range scanPyGates(cur) {
			g.File = r.Path
			if !atHead[g.Cond] && !lifted[g.key()] {
				out = append(out, g)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	return out
}

// requiredGates is versionGates limited to the conftests a broad run loads:
// those under the configured testpaths (all of them when none is set).
func requiredGates(x *base.Index) []versionGate {
	roots := testRoots(x)
	var out []versionGate
	for _, g := range versionGates(x) {
		if underRoots(path.Dir(g.File), roots) {
			out = append(out, g)
		}
	}
	return out
}

// testRoots reads pytest's testpaths from the repository's configuration
// (pytest.ini, pyproject.toml, tox.ini, setup.cfg, in pytest's order); nil
// when none sets it.
func testRoots(x *base.Index) []string {
	sections := []struct{ file, section string }{
		{"pytest.ini", "[pytest]"}, {"pyproject.toml", "[tool.pytest.ini_options]"}, {"tox.ini", "[pytest]"}, {"setup.cfg", "[tool:pytest]"},
	}
	for _, s := range sections {
		b, err := os.ReadFile(filepath.Join(x.Root(), s.file))
		if err != nil {
			continue
		}
		if roots := parseTestpaths(string(b), s.section); len(roots) > 0 {
			return roots
		}
	}
	return nil
}

var testpathsRe = regexp.MustCompile(`^testpaths\s*=\s*(.*)$`)

// parseTestpaths returns the testpaths value of section in an ini or toml
// source: words of the key's line and its continuation lines (indented ini
// lines, or a toml array up to its closing bracket).
func parseTestpaths(src, section string) []string {
	in, collecting, array := false, false, false
	var vals []string
	add := func(s string) {
		s = strings.NewReplacer("[", " ", "]", " ", `"`, " ", "'", " ", ",", " ").Replace(s)
		for _, f := range strings.Fields(s) {
			vals = append(vals, strings.TrimSuffix(strings.TrimPrefix(f, "./"), "/"))
		}
	}
	for _, l := range strings.Split(src, "\n") {
		t := strings.TrimSpace(l)
		if collecting {
			if array || (t != "" && (l[0] == ' ' || l[0] == '\t') && !strings.Contains(t, "=")) {
				add(t)
				if array && strings.Contains(t, "]") {
					return vals
				}
				continue
			}
			return vals
		}
		if strings.HasPrefix(t, "[") && !strings.HasPrefix(t, "[[") {
			in = t == section
			continue
		}
		if m := testpathsRe.FindStringSubmatch(t); in && m != nil {
			collecting = true
			array = strings.Contains(m[1], "[") && !strings.Contains(m[1], "]")
			add(m[1])
		}
	}
	return vals
}

// underRoots reports whether dir is at or below one of roots; the
// repository root, and any dir when roots is empty, always is.
func underRoots(dir string, roots []string) bool {
	if len(roots) == 0 || dir == "." {
		return true
	}
	for _, r := range roots {
		if r == "." || dir == r || strings.HasPrefix(dir, r+"/") {
			return true
		}
	}
	return false
}

// hasLine reports whether src has a line that reads line once trimmed and
// stripped of a comment.
func hasLine(src, line string) bool {
	for _, l := range strings.Split(src, "\n") {
		if strings.TrimSpace(stripPyComment(l)) == line {
			return true
		}
	}
	return false
}

// stripPyComment drops a # comment outside quotes from a line of Python.
func stripPyComment(l string) string {
	var q rune
	for i, r := range l {
		switch {
		case q != 0:
			if r == q {
				q = 0
			}
		case r == '"' || r == '\'':
			q = r
		case r == '#':
			return l[:i]
		}
	}
	return l
}

// scanPyGates finds versionGates' branches in one Python source (File and
// Lifted left to the caller). A condition already made false (if False
// and …) is no gate.
func scanPyGates(src string) []versionGate {
	lines := strings.Split(src, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(stripPyComment(lines[i]), " \t")
	}
	indent := func(l string) int { return len(l) - len(strings.TrimLeft(l, " \t")) }
	var out []versionGate
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if !(strings.HasPrefix(t, "if ") || strings.HasPrefix(t, "elif ")) || !strings.HasSuffix(t, ":") ||
			!pyDepVerRe.MatchString(t) || !pyCompareRe.MatchString(t) || pyNotDepGate.MatchString(t) ||
			pyPerItemRe.MatchString(t) || pyDeadCondRe.MatchString(t) {
			continue
		}
		ind := indent(l)
		var block []string
		for j := i + 1; j < len(lines) && len(block) < 40; j++ {
			if strings.TrimSpace(lines[j]) == "" {
				continue
			}
			if indent(lines[j]) <= ind {
				break
			}
			block = append(block, lines[j])
		}
		body := strings.Join(block, "\n")
		fn, fnText := "", ""
		for j := i - 1; j >= 0; j-- {
			m := pyDefLineRe.FindStringSubmatch(lines[j])
			if m == nil || len(m[1]) >= ind {
				continue
			}
			k := j + 1
			for k < len(lines) && (strings.TrimSpace(lines[k]) == "" || indent(lines[k]) > len(m[1])) {
				k++
			}
			if i < k { // the condition is inside this def
				fn, fnText = m[2], strings.Join(lines[j:k], "\n")
			}
			break
		}
		g := versionGate{Line: i + 1, Cond: t, Fn: fn}
		switch {
		case fn == "pytest_ignore_collect" && strings.Contains(body, "return True"):
			g.Category = gateCatIgnored
		case !pySkipActRe.MatchString(body):
			continue
		case fn == "" && strings.Contains(body, "collect_ignore"):
			g.Category = gateCatIgnored
		case strings.HasPrefix(fn, "pytest_"):
			g.Category = gateCatHook
		default:
			continue // a helper for one test or file, not a whole category
		}
		if strings.Contains(strings.ToLower(fnText+body), "doctest") {
			g.Category = gateCatDoctest
		}
		if m := pyReasonRe.FindStringSubmatch(body); m != nil {
			g.Reason = m[1]
		}
		out = append(out, g)
	}
	return out
}

// gateLines renders at most versionGateMax gates for the model, one a line.
func gateLines(gates []versionGate) string {
	var b strings.Builder
	for i, g := range gates {
		if i == versionGateMax {
			fmt.Fprintf(&b, versionGateMore, len(gates)-i)
			break
		}
		reason := ""
		if g.Reason != "" {
			reason = fmt.Sprintf(versionGateReason, g.Reason)
		}
		fmt.Fprintf(&b, versionGateLine, g.File, g.Line, g.Cond, g.Category, reason)
		if g.Lifted {
			b.WriteString(versionGateLifted)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// isGateFile reports whether rel is a conftest holding a version gate at
// HEAD: lifting and restoring its gate line is test plumbing, not a change
// the write gate plans.
func isGateFile(x *base.Index, rel string) bool {
	if path.Base(rel) != "conftest.py" {
		return false
	}
	head, ok := x.HeadContent(rel)
	return ok && len(scanPyGates(head)) > 0
}

// codeExts and codeNames are the files whose change calls for a test run:
// source and build configuration, not documents, data or test artifacts.
var (
	codeExts = map[string]bool{
		".py": true, ".pyx": true, ".pxd": true, ".pxi": true, ".go": true, ".rs": true,
		".js": true, ".jsx": true, ".ts": true, ".tsx": true, ".mjs": true, ".cjs": true, ".mts": true, ".cts": true,
		".vue": true, ".svelte": true, ".java": true, ".kt": true, ".kts": true, ".scala": true, ".groovy": true,
		".c": true, ".h": true, ".cc": true, ".cpp": true, ".cxx": true, ".hpp": true, ".hh": true,
		".cs": true, ".rb": true, ".php": true, ".swift": true, ".m": true, ".mm": true,
		".ex": true, ".exs": true, ".erl": true, ".hs": true, ".ml": true, ".lua": true, ".dart": true,
		".toml": true, ".cfg": true, ".ini": true, ".gradle": true,
	}
	codeNames = map[string]bool{"package.json": true, "go.mod": true, "pom.xml": true, "Makefile": true, "CMakeLists.txt": true, "meson.build": true}
)

func codeFile(rel string) bool {
	return codeExts[strings.ToLower(path.Ext(rel))] || codeNames[path.Base(rel)]
}

// dataFile recognises manifests, configuration and data: their outline's API
// names keys, not code names.
func dataFile(rel string) bool {
	switch strings.ToLower(path.Ext(rel)) {
	case ".json", ".yaml", ".yml", ".toml", ".lock", ".xml", ".ini", ".cfg", ".csv", ".tsv", ".properties":
		return true
	}
	return false
}

// testRunKind reads a shell command for verifyGate: broad when one of its
// commands, run at the repository root, runs the repository's own test suite
// with its own configuration and narrows nothing; commit when one commits in
// this repository. cd and git -C are followed; a directory the text cannot
// resolve counts as elsewhere.
func testRunKind(command, root string) (broad, commit bool) {
	words, commit := scanTestRun(command, root)
	return words != nil, commit
}

// scanTestRun is testRunKind returning the words of the first broad test run
// (wrappers stripped), nil when there is none.
func scanTestRun(command, root string) (broadWords []string, commit bool) {
	return scanTestRunIn(command, root, root, 0)
}

// scanTestRunIn scans command run from dir. A shell's -c script is scanned
// as its own commands, from a copy of dir: its cd does not move the caller.
func scanTestRunIn(command, root, dir string, depth int) (broadWords []string, commit bool) {
	atRoot := func(d string) bool {
		rel, in := base.Rel(root, d)
		return d != "" && in && rel == "."
	}
	for _, seg := range shellSegments(command) {
		w := unwrapCommand(dropRedirects(seg.words))
		if len(w) == 0 {
			continue
		}
		if depth < 3 && shellInterpreters[filepath.Base(w[0])] {
			if script, ok := shellScriptArg(w[1:]); ok {
				bw, c := scanTestRunIn(script, root, dir, depth+1)
				if broadWords == nil {
					broadWords = bw
				}
				commit = commit || c
				continue
			}
		}
		switch verb := filepath.Base(w[0]); verb {
		case "cd", "pushd":
			dir = cdTarget(dir, w[1:])
		case "git":
			if gitCommits(w[1:], dir, root) {
				commit = true
			}
		default:
			if broadWords == nil && atRoot(dir) && broadTestRun(verb, w[1:], root) {
				broadWords = w
			}
		}
	}
	return broadWords, commit
}

// shellInterpreters run the script given after -c.
var shellInterpreters = map[string]bool{"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true}

// expandedSegments is shellSegments with each "sh -c <script>" (any shell
// above, options before -c allowed) replaced by the segments of its script,
// so a run wrapped for detaching (setsid nohup bash -c '…' &) reads as the
// commands it runs. Nesting is followed a few levels deep.
func expandedSegments(cmd string, depth int) []shellSegment {
	var out []shellSegment
	for _, seg := range shellSegments(cmd) {
		w := unwrapCommand(dropRedirects(seg.words))
		if depth < 3 && len(w) > 1 && shellInterpreters[filepath.Base(w[0])] {
			if script, ok := shellScriptArg(w[1:]); ok {
				out = append(out, expandedSegments(script, depth+1)...)
				continue
			}
		}
		out = append(out, seg)
	}
	return out
}

// shellScriptArg finds the script of a shell's -c among its arguments: once
// an option holding c is seen (-c, -lc, -ec …), the first argument that is
// no option. -o/+o/-O/+O, --rcfile and --init-file take a value.
func shellScriptArg(args []string) (string, bool) {
	sawC := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-o" || a == "+o" || a == "-O" || a == "+O" || a == "--rcfile" || a == "--init-file":
			i++
		case strings.HasPrefix(a, "--"):
		case strings.HasPrefix(a, "-") || strings.HasPrefix(a, "+"):
			if a[0] == '-' && strings.Contains(a[1:], "c") {
				sawC = true
			}
		default:
			return a, sawC // without -c it is a script file
		}
	}
	return "", false
}

// detachedCommand reports whether a shell command leaves something running
// after it returns: a command ended by a lone & with no wait after it, or
// setsid/nohup/disown.
func detachedCommand(cmd string) bool {
	// The outer commands first: expanding a sh -c strips the setsid around it.
	segs := append(shellSegments(cmd), expandedSegments(cmd, 0)...)
	for i, seg := range segs {
		if seg.bg {
			waited := false
			for _, later := range segs[i+1:] {
				if w := unwrapCommand(later.words); len(w) > 0 && w[0] == "wait" {
					waited = true
				}
			}
			if !waited {
				return true
			}
		}
		for _, w := range seg.words {
			if envAssignRe.MatchString(w) {
				continue
			}
			if b := filepath.Base(w); b == "setsid" || b == "nohup" || b == "disown" {
				return true
			}
			break
		}
	}
	return false
}

// dropRedirects removes redirections and their targets from a simple
// command's words (2>&1, > out.log, <in, <<EOF), which are no arguments of
// the program.
func dropRedirects(words []string) []string {
	var out []string
	for i := 0; i < len(words); i++ {
		w := words[i]
		t := strings.TrimLeft(w, "0123456789&")
		switch {
		case strings.HasPrefix(t, ">"):
			if t == ">" || t == ">>" || t == ">|" || t == ">&" {
				i++ // the target is the next word
			}
		case strings.HasPrefix(w, "<"):
			if w == "<" || w == "<<" || w == "<<-" || w == "<<<" {
				i++
			}
		default:
			out = append(out, w)
		}
	}
	return out
}

var (
	pythonRe    = regexp.MustCompile(`^python(\d+(\.\d+)*)?$`)
	envAssignRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
)

// unwrapCommand strips what runs a command without being it: environment
// assignments, shell keywords, sudo/env/time/nice/timeout and their options,
// tool runners (uv run, poetry run, npx …) and python -m, so the words start
// with the program that does the work.
func unwrapCommand(w []string) []string {
	skipOpts := func(valued map[string]bool) {
		for len(w) > 0 && strings.HasPrefix(w[0], "-") {
			opt := w[0]
			w = w[1:]
			if valued[opt] && len(w) > 0 {
				w = w[1:]
			}
		}
	}
	for len(w) > 0 {
		a := w[0]
		switch {
		case envAssignRe.MatchString(a):
			w = w[1:]
		case a == "do" || a == "then" || a == "else" || a == "elif" || a == "if" || a == "while" || a == "until" || a == "!" || a == "{":
			w = w[1:]
		case a == "sudo" || a == "env" || a == "time" || a == "command" || a == "exec" || a == "nohup" || a == "setsid" || a == "nice" || a == "stdbuf":
			w = w[1:]
			skipOpts(map[string]bool{"-u": true, "-n": true, "-g": true, "-C": true})
		case a == "timeout":
			w = w[1:]
			skipOpts(map[string]bool{"-s": true, "-k": true, "--signal": true, "--kill-after": true})
			if len(w) > 0 {
				w = w[1:] // the duration
			}
		case (a == "uv" || a == "poetry" || a == "pipenv" || a == "pdm" || a == "hatch" || a == "rye") && len(w) > 1 && w[1] == "run":
			w = w[2:]
			skipOpts(map[string]bool{"--with": true, "--python": true, "-p": true, "--env": true, "-e": true})
		case a == "npx" || a == "bunx" || (a == "pnpm" || a == "yarn") && len(w) > 1 && (w[1] == "exec" || w[1] == "dlx"):
			if a == "pnpm" || a == "yarn" {
				w = w[1:]
			}
			w = w[1:]
			skipOpts(nil)
		case pythonRe.MatchString(filepath.Base(a)) || a == "py":
			for i := 1; i < len(w); i++ {
				switch o := w[i]; {
				case o == "-m" && i+1 < len(w):
					return w[i+1:]
				case o == "-X" || o == "-W":
					i++
				case !strings.HasPrefix(o, "-"):
					return w // a script, not a module
				}
			}
			return w
		default:
			return w
		}
	}
	return w
}

// gitSubcommand returns the subcommand git's arguments run in the repository
// at root, run from dir (git -C moves it; options before the subcommand are
// skipped), and its arguments; "" when it runs elsewhere or runs none.
func gitSubcommand(args []string, dir, root string) (string, []string) {
	for len(args) > 0 {
		a := args[0]
		switch {
		case a == "-C" && len(args) > 1:
			dir = cdTarget(dir, args[1:2])
			args = args[2:]
		case (a == "-c" || a == "--git-dir" || a == "--work-tree" || a == "--namespace") && len(args) > 1:
			args = args[2:]
		case strings.HasPrefix(a, "-"):
			args = args[1:]
		default:
			if dir == "" {
				return "", nil
			}
			if _, in := base.Rel(root, dir); !in {
				return "", nil
			}
			return a, args[1:]
		}
	}
	return "", nil
}

// gitCommits reports whether git's arguments commit in the repository at root.
func gitCommits(args []string, dir, root string) bool {
	sub, _ := gitSubcommand(args, dir, root)
	return sub == "commit"
}

// gitStages reports whether git's arguments stage changes in the repository
// at root: add, rm, mv, stage, or a commit that stages tracked changes itself
// (-a, --all).
func gitStages(args []string, dir, root string) bool {
	sub, rest := gitSubcommand(args, dir, root)
	switch sub {
	case "add", "rm", "mv", "stage":
		return true
	case "commit":
		for _, a := range rest {
			if a == "--all" || len(a) > 1 && a[0] == '-' && a[1] != '-' && strings.ContainsRune(a[1:], 'a') {
				return true
			}
		}
	}
	return false
}

// stagesChanges reports whether a shell command stages changes in the
// repository at root (cd, git -C and sh -c scripts followed as for
// testRunKind).
func stagesChanges(command, root string) bool {
	return stagesIn(command, root, root, 0)
}

func stagesIn(command, root, dir string, depth int) bool {
	for _, seg := range shellSegments(command) {
		w := unwrapCommand(dropRedirects(seg.words))
		if len(w) == 0 {
			continue
		}
		if depth < 3 && shellInterpreters[filepath.Base(w[0])] {
			if script, ok := shellScriptArg(w[1:]); ok {
				if stagesIn(script, root, dir, depth+1) {
					return true
				}
				continue
			}
		}
		switch filepath.Base(w[0]) {
		case "cd", "pushd":
			dir = cdTarget(dir, w[1:])
		case "git":
			if gitStages(w[1:], dir, root) {
				return true
			}
		}
	}
	return false
}

// broadTestRun reports whether verb with args runs the whole test suite the
// way the repository configures it: no test selection (a name filter, a
// file, a subdirectory, a package) and no override of the collection
// configuration. Unknown programs are no test run.
func broadTestRun(verb string, args []string, root string) bool {
	switch verb {
	case "pytest", "py.test":
		return pytestBroad(args, root)
	case "unittest":
		return len(args) == 0 || args[0] == "discover" && !slices.Contains(args, "-k") && !slices.Contains(args, "-p")
	case "tox", "nox":
		if i := slices.Index(args, "--"); i >= 0 {
			return pytestBroad(args[i+1:], root)
		}
		return true
	case "make":
		return !slices.Contains(args, "-C") && (slices.Contains(args, "test") || slices.Contains(args, "tests") || slices.Contains(args, "check"))
	case "go":
		return goTestBroad(args)
	case "cargo":
		return cargoTestBroad(args)
	case "npm", "pnpm", "yarn", "bun":
		rest := args
		if len(rest) > 0 && rest[0] == "run" {
			rest = rest[1:]
		}
		if len(rest) == 0 || rest[0] != "test" && rest[0] != "t" {
			return false
		}
		return jsTestBroad(rest[1:])
	case "jest", "mocha":
		return jsTestBroad(args)
	case "vitest":
		if len(args) > 0 && (args[0] == "run" || args[0] == "watch") {
			args = args[1:]
		}
		return jsTestBroad(args)
	case "mvn", "mvnw":
		phase := false
		for _, a := range args {
			name, _, _ := strings.Cut(a, "=")
			switch {
			case name == "-Dtest" || name == "-Dit.test" || name == "-DskipTests" || name == "-Dmaven.test.skip" || strings.HasPrefix(a, "-pl") || name == "--projects":
				return false
			case a == "test" || a == "verify" || a == "install" || a == "package" || a == "integration-test":
				phase = true
			}
		}
		return phase
	case "gradle", "gradlew":
		task := false
		for _, a := range args {
			switch {
			case a == "--tests" || strings.HasPrefix(a, "--tests="):
				return false
			case a == "test" || a == "check" || a == "build":
				task = true
			case strings.HasPrefix(a, ":") && strings.Count(a, ":") > 1:
				return false // a subproject's task
			}
		}
		return task
	case "ctest":
		for _, a := range args {
			if a == "-R" || a == "-L" || a == "-I" || a == "-E" || strings.HasPrefix(a, "--tests-regex") || strings.HasPrefix(a, "--label-regex") {
				return false
			}
		}
		return true
	}
	return false
}

// pytestValued are the pytest options whose value may follow as the next word.
var pytestValued = map[string]bool{
	"-n": true, "-W": true, "-r": true, "--tb": true, "--durations": true, "--maxfail": true, "--timeout": true,
	"--junitxml": true, "--junit-xml": true, "--basetemp": true, "--log-level": true, "--log-cli-level": true,
	"--cov": true, "--cov-report": true, "--dist": true, "--numprocesses": true, "--capture": true,
	"--color": true, "--import-mode": true, "--randomly-seed": true,
}

var dottedRe = regexp.MustCompile(`^[A-Za-z_]\w*(\.[A-Za-z_]\w*)+$`)

// pytestBroad: no -k/-m selection, no last-failed or deselection, no
// override of the configuration (-o, -c, --rootdir, --noconftest, -p
// no:doctest…), and no path narrower than a top-level directory.
func pytestBroad(args []string, root string) bool {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--" || a == "-":
			continue
		case !strings.HasPrefix(a, "-"):
			if narrowsPath(a, root) {
				return false
			}
		case strings.HasPrefix(a, "--"):
			name, _, eq := strings.Cut(a, "=")
			switch name {
			case "--lf", "--last-failed", "--sw", "--stepwise", "--deselect", "--ignore", "--ignore-glob",
				"--override-ini", "--config-file", "--rootdir", "--noconftest", "--confcutdir", "--keyword",
				"--collect-only", "--co", "--version", "--help", "--fixtures", "--markers", "--setup-plan":
				return false // a selection, an override, or no run at all
			}
			if !eq && pytestValued[name] {
				i++
			}
		default:
			// A group of short flags (-qx, -vk expr): letters until one that
			// takes a value, which is the rest of the group or the next word.
		group:
			for j := 1; j < len(a); j++ {
				switch a[j] {
				case 'k', 'm', 'o', 'c', 'h':
					return false
				case 'p', 'n', 'W', 'r':
					v := a[j+1:]
					if v == "" && i+1 < len(args) {
						i++
						v = args[i]
					}
					if a[j] == 'p' && strings.HasPrefix(v, "no:doctest") {
						return false
					}
					break group
				}
			}
		}
	}
	return true
}

// narrowsPath reports whether a positional test argument selects less than
// the suite: a node id, a file, a directory below the top level, a dotted
// module, or a missing path that looks like one. The root and a top-level
// directory (the package itself) do not; neither does a word that is no path.
func narrowsPath(a, root string) bool {
	if strings.Contains(a, "::") || strings.ContainsAny(a, "$`") {
		return true // a node id, or a selection the text cannot tell
	}
	p := strings.TrimSuffix(strings.TrimPrefix(filepath.ToSlash(a), "./"), "/")
	if p == "" || p == "." {
		return false
	}
	abs := p
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(root, filepath.FromSlash(p))
	}
	rel, in := base.Rel(root, abs)
	fi, err := os.Stat(abs)
	switch {
	case err != nil:
		return strings.Contains(p, "/") || path.Ext(p) == ".py" || dottedRe.MatchString(p)
	case !in:
		return true
	case rel == ".":
		return false
	}
	return !fi.IsDir() || strings.Contains(rel, "/")
}

// goValued are the go test flags whose value may follow as the next word.
var goValued = map[string]bool{
	"-count": true, "-timeout": true, "-p": true, "-tags": true, "-parallel": true, "-cpu": true,
	"-coverprofile": true, "-bench": true, "-benchtime": true, "-o": true, "-exec": true, "-covermode": true,
	"-coverpkg": true, "-mod": true, "-vet": true, "-ldflags": true, "-gcflags": true, "-shuffle": true,
}

// goTestBroad: go test ./... with no -run/-skip/-short and no other package list.
func goTestBroad(args []string) bool {
	if len(args) == 0 || args[0] != "test" {
		return false
	}
	all := false
	for i := 1; i < len(args); i++ {
		a := args[i]
		name, _, eq := strings.Cut(strings.Replace(a, "--", "-", 1), "=")
		switch {
		case a == "-args":
			return all
		case name == "-run" || name == "-skip" || name == "-short" || name == "-list":
			return false
		case a == "./...":
			all = true
		case !strings.HasPrefix(a, "-"):
			return false // a package list narrower than ./...
		case !eq && goValued[name]:
			i++
		}
	}
	return all
}

// cargoValued are the cargo test options whose value may follow as the next word.
var cargoValued = map[string]bool{
	"--features": true, "-F": true, "--target": true, "--profile": true, "-j": true, "--jobs": true,
	"--target-dir": true, "--color": true, "--message-format": true, "-Z": true, "--config": true, "--exclude": true,
	"--test-threads": true, "--format": true,
}

// cargoTestBroad: cargo test (or nextest run) with no package, target or
// name filter, here or after --.
func cargoTestBroad(args []string) bool {
	for len(args) > 0 && strings.HasPrefix(args[0], "+") {
		args = args[1:] // a toolchain
	}
	switch {
	case len(args) > 0 && (args[0] == "test" || args[0] == "t"):
		args = args[1:]
	case len(args) > 1 && args[0] == "nextest" && args[1] == "run":
		args = args[2:]
	default:
		return false
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		name, _, eq := strings.Cut(a, "=")
		switch {
		case a == "--":
			continue
		case name == "-p" || name == "--package" || name == "--test" || name == "--bench" || name == "--example" ||
			name == "--bin" || a == "--lib" || a == "--doc" || name == "--manifest-path" || name == "--skip" ||
			a == "--ignored" || a == "-E" || name == "--filter-expr" || a == "--no-run":
			return false
		case !strings.HasPrefix(a, "-"):
			return false // a test name filter
		case !eq && cargoValued[name]:
			i++
		}
	}
	return true
}

// jsTestBroad: no file or pattern argument and no name, path or project filter.
func jsTestBroad(args []string) bool {
	valued := map[string]bool{"-w": true, "--maxWorkers": true, "--reporter": true, "--timeout": true, "--testTimeout": true,
		"--pool": true, "--environment": true, "--env": true, "--outputFile": true}
	for i := 0; i < len(args); i++ {
		a := args[i]
		name, _, eq := strings.Cut(a, "=")
		switch {
		case a == "--":
			continue
		case name == "-t" || name == "--testNamePattern" || name == "--testPathPattern" || name == "--testPathPatterns" ||
			name == "-g" || name == "--grep" || name == "--project" || name == "--shard" || name == "--selectProjects" ||
			name == "--filter" || a == "--onlyChanged" || a == "-o" || a == "--changed" || name == "--config" || name == "-c" ||
			a == "--listTests" || a == "--list":
			return false
		case !strings.HasPrefix(a, "-"):
			return false // a file or pattern
		case !eq && valued[name]:
			i++
		}
	}
	return true
}

// isTestPath recognises a test file by the usual layouts: a test or spec
// directory anywhere on the path, or a file named the way Go, Rust, Python and
// JS/TS test runners pick up (x_test.go, test_x.py, x.test.ts, x.spec.js).
func isTestPath(rel string) bool {
	parts := strings.Split(rel, "/")
	for _, d := range parts[:len(parts)-1] {
		switch strings.ToLower(d) {
		case "test", "tests", "__tests__", "spec", "specs", "testdata", "testing":
			return true
		}
	}
	name := strings.ToLower(parts[len(parts)-1])
	base := strings.TrimSuffix(name, path.Ext(name))
	return strings.HasSuffix(base, "_test") || strings.HasPrefix(base, "test_") ||
		strings.HasSuffix(base, ".test") || strings.HasSuffix(base, ".spec") || base == "tests"
}

// codeSearch reports whether a shell command, run at a repository root,
// searches files of that repository rather than filtering a stream; see
// shellTargets.
func codeSearch(command string) bool {
	const root = "/r"
	inRepo := func(p string) (string, bool) { return base.Rel(root, p) }
	touched, _ := shellTargets(command, root, inRepo, func(string) []string { return nil })
	for _, t := range touched {
		if t == "." {
			return true
		}
	}
	return false
}

// shellTargets walks a command line's simple commands in order, following
// cd and pushd from the repository root, and names what they touch here: the
// table files a command run inside the repository names, and the repository
// itself (".") when a grep-family command searches a path inside it — its
// file or directory operands, or, with none, the directory it runs in when
// it searches by default (rg/ag/ack, grep -r) and is not reading a pipe; git
// grep and xargs grep search where they run; popd returns to where the
// matching pushd left. A cd the text cannot resolve
// ($VAR, backquotes, cd -) leaves the repository: what follows is not judged,
// so "cd $R && grep x ortg.tsv" in another repository touches nothing here.
func shellTargets(command, root string, inRepo func(string) (string, bool), mentioned func(string) []string) (touched, writes []string) {
	dir := root
	var stack []string // pushd saves where popd returns
	seen, wseen := map[string]bool{}, map[string]bool{}
	add := func(t string) {
		if !seen[t] {
			seen[t] = true
			touched = append(touched, t)
		}
	}
	resolve := func(p string) (string, bool) {
		switch {
		case p == "" || strings.ContainsAny(p, "$`"):
			return "", false
		case strings.HasPrefix(p, "~/"):
			home, _ := os.UserHomeDir()
			return filepath.Join(home, p[2:]), true
		case filepath.IsAbs(p):
			return p, true
		case dir == "":
			return "", false
		}
		return filepath.Join(dir, p), true
	}
	here := func(p string) bool {
		a, ok := resolve(p)
		if !ok {
			return false
		}
		_, in := inRepo(a)
		return in
	}
	// write records a path the command writes, creates or removes; with
	// mustExist it counts only an existing file, so a sed script or an
	// option value is never taken for one.
	write := func(p string, mustExist bool) {
		a, ok := resolve(p)
		if !ok {
			return
		}
		if _, err := os.Stat(a); mustExist && err != nil {
			return
		}
		if rel, in := inRepo(a); in && rel != "." && !wseen[rel] {
			wseen[rel] = true
			writes = append(writes, rel)
		}
	}
	for _, seg := range shellSegments(command) {
		w := seg.words
		for len(w) > 0 && (w[0] == "sudo" || w[0] == "time" || w[0] == "command") {
			w = w[1:]
		}
		if len(w) == 0 {
			continue
		}
		verb := filepath.Base(w[0])
		switch {
		case verb == "cd" || verb == "pushd":
			if verb == "pushd" {
				stack = append(stack, dir)
			}
			dir = cdTarget(dir, w[1:])
			continue
		case verb == "popd":
			if len(stack) > 0 {
				dir, stack = stack[len(stack)-1], stack[:len(stack)-1]
			}
			continue
		}
		shellWrites(verb, w[1:], write, resolve)
		if !here(".") {
			continue // this command runs outside the repository
		}
		for _, p := range mentioned(strings.Join(w, " ")) {
			add(p)
		}
		switch verb {
		case "git", "xargs":
			if verb == "git" && len(w) > 1 && w[1] == "grep" || verb == "xargs" && slicesContainsSearch(w[1:]) {
				add(".")
			}
		case "grep", "egrep", "fgrep", "rg", "ag", "ack":
			paths, recursive := searchArgs(w[1:])
			if len(paths) == 0 && !seg.piped && (verb == "rg" || verb == "ag" || verb == "ack" || recursive) {
				paths = []string{"."}
			}
			for _, p := range paths {
				if here(p) {
					add(".")
					break
				}
			}
		}
	}
	return touched, writes
}

// shellWrites reports, through write, the paths one simple command writes:
// output redirections anywhere in it, sed/perl -i on existing files,
// tee/touch/rm/unlink/truncate operands, and cp/install/ln/mv targets (a
// directory target takes each source's name; mv also removes its sources).
func shellWrites(verb string, args []string, write func(string, bool), resolve func(string) (string, bool)) {
	var plain []string // operands: not options, not redirections
	inPlace := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		t := strings.TrimLeft(a, "0123456789&")
		switch {
		case t == ">" || t == ">>" || t == ">|":
			if i+1 < len(args) {
				write(args[i+1], false)
				i++
			}
		case strings.HasPrefix(t, ">") && !strings.HasPrefix(t, ">&"):
			write(strings.TrimLeft(t, ">|"), false)
		case a == "<" || a == "<<" || a == "<<-" || a == "<<<":
			i++
		case strings.HasPrefix(a, "<"):
		case a == "--in-place" || strings.HasPrefix(a, "--in-place="):
			inPlace = true
		case strings.HasPrefix(a, "--"):
		case len(a) > 1 && a[0] == '-':
			if strings.ContainsRune(a[1:], 'i') {
				inPlace = true // sed -i, sed -i.bak, perl -pi
			}
		default:
			plain = append(plain, a)
		}
	}
	isDir := func(p string) bool {
		a, ok := resolve(p)
		if !ok {
			return false
		}
		fi, err := os.Stat(a)
		return err == nil && fi.IsDir()
	}
	switch verb {
	case "sed", "perl":
		if inPlace {
			for _, p := range plain {
				write(p, true)
			}
		}
	case "tee", "touch":
		for _, p := range plain {
			write(p, false)
		}
	case "rm", "unlink", "truncate":
		for _, p := range plain {
			write(p, true)
		}
	case "cp", "install", "ln", "mv":
		if len(plain) < 2 {
			return
		}
		dst, srcs := plain[len(plain)-1], plain[:len(plain)-1]
		for _, s := range srcs {
			if verb == "mv" {
				write(s, true)
			}
			if isDir(dst) {
				write(filepath.Join(dst, filepath.Base(s)), false)
			}
		}
		if !isDir(dst) {
			write(dst, false)
		}
	}
}

// cdTarget is the directory a cd or pushd with args leads to from dir, or ""
// when the text alone cannot tell (a variable, backquotes, "cd -", or dir
// already unknown and the target relative).
func cdTarget(dir string, args []string) string {
	for len(args) > 0 && strings.HasPrefix(args[0], "-") && args[0] != "-" {
		args = args[1:] // cd -P, pushd -n
	}
	if len(args) == 0 || args[0] == "~" || strings.HasPrefix(args[0], "~/") {
		home, _ := os.UserHomeDir()
		if len(args) == 0 || args[0] == "~" {
			return home
		}
		return filepath.Join(home, args[0][2:])
	}
	t := args[0]
	switch {
	case t == "-" || strings.ContainsAny(t, "$`"):
		return ""
	case filepath.IsAbs(t):
		return filepath.Clean(t)
	case dir == "":
		return ""
	}
	return filepath.Join(dir, t)
}

func slicesContainsSearch(w []string) bool {
	for _, a := range w {
		switch filepath.Base(a) {
		case "grep", "egrep", "fgrep", "rg", "ag", "ack":
			return true
		}
	}
	return false
}

// searchArgs names the paths a grep-family command searches — its operands
// after the pattern, which -e/-f supply instead, and a file redirected into
// it — and whether it recurses.
func searchArgs(args []string) (paths []string, recursive bool) {
	pattern := false
	takesValue := map[string]bool{"-m": true, "-A": true, "-B": true, "-C": true, "-d": true, "-D": true,
		"-t": true, "-T": true, "-g": true, "--max-count": true, "--type": true, "--glob": true}
	var operands, redirected []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "<":
			if i+1 < len(args) {
				redirected = append(redirected, args[i+1]) // that file is searched
			}
			i++
		case a == "<<" || a == "<<<" || redirectOut(a):
			if strings.Trim(strings.TrimLeft(a, "0123456789"), "<>&") == "" {
				i++ // bare operator: the next word is its target, not a path
			}
		case strings.HasPrefix(a, "<"):
			if !strings.HasPrefix(a, "<<") {
				redirected = append(redirected, a[1:])
			}
		case a == "--":
			operands = append(operands, args[i+1:]...)
			i = len(args)
		case a == "-e" || a == "-f" || a == "--regexp" || a == "--file":
			pattern = true
			i++
		case strings.HasPrefix(a, "--regexp=") || strings.HasPrefix(a, "--file="):
			pattern = true
		case a == "--recursive" || a == "--dereference-recursive":
			recursive = true
		case takesValue[a]:
			i++
		case strings.HasPrefix(a, "--"):
		case len(a) > 1 && a[0] == '-':
			if strings.ContainsAny(a[1:], "rR") {
				recursive = true
			}
			if k := strings.IndexByte(a, 'e'); k > 0 {
				pattern = true // -e with its pattern glued on (-efoo) or next (-rne foo)
				if k == len(a)-1 {
					i++
				}
			}
		default:
			operands = append(operands, a)
		}
	}
	if !pattern && len(operands) > 0 {
		operands = operands[1:] // the first operand is the pattern
	}
	return append(operands, redirected...), recursive
}

// redirectOut reports an output redirection word: >, >>, 2>, &>, 2>&1,
// >/dev/null and the like.
func redirectOut(a string) bool {
	t := strings.TrimLeft(a, "0123456789&")
	return strings.HasPrefix(t, ">")
}

type shellSegment struct {
	words []string
	piped bool // reads the previous command's output
	bg    bool // ended by a lone & : the shell does not wait for it
}

// shellSegments splits a command line into simple commands at unquoted
// | ; & ( ) and newlines, honouring quotes and backslashes so a quoted
// pattern stays one word, skipping heredoc bodies (<<EOF … EOF), and marks
// the commands fed by a pipe.
func shellSegments(cmd string) []shellSegment {
	var segs []shellSegment
	cur := shellSegment{}
	var word strings.Builder
	inWord := false
	var heredocs []string // delimiters whose bodies start at the next newline
	expectDelim := false
	flushWord := func() {
		if !inWord {
			return
		}
		w := word.String()
		cur.words = append(cur.words, w)
		word.Reset()
		inWord = false
		switch {
		case expectDelim:
			heredocs, expectDelim = append(heredocs, w), false
		case w == "<<" || w == "<<-":
			expectDelim = true
		case strings.HasPrefix(w, "<<") && !strings.HasPrefix(w, "<<<"):
			if d := strings.TrimPrefix(w[2:], "-"); d != "" {
				heredocs = append(heredocs, d)
			}
		}
	}
	flushSeg := func(piped bool) {
		flushWord()
		if len(cur.words) > 0 {
			segs = append(segs, cur)
		}
		cur = shellSegment{piped: piped}
	}
	rs := []rune(cmd)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case r == '\\' && i+1 < len(rs) && rs[i+1] == '\n':
			i++ // a line continuation joins the lines, it is no word
		case r == '\\' && i+1 < len(rs):
			i++
			word.WriteRune(rs[i])
			inWord = true
		case r == '\'' || r == '"':
			j := i + 1
			for j < len(rs) && rs[j] != r {
				if r == '"' && rs[j] == '\\' && j+1 < len(rs) {
					j++
				}
				word.WriteRune(rs[j])
				j++
			}
			i = j
			inWord = true
		case r == '|':
			if i+1 < len(rs) && rs[i+1] == '|' {
				i++
				flushSeg(false)
			} else {
				flushSeg(true)
			}
		case r == '&' && (i > 0 && (rs[i-1] == '>' || rs[i-1] == '<') || i+1 < len(rs) && rs[i+1] == '>'):
			word.WriteRune(r) // part of a redirection: >&1, <&0, &>file
			inWord = true
		case r == '\n':
			flushSeg(false) // flushes the last word, so a trailing <<EOF counts
			if len(heredocs) == 0 {
				continue
			}
			// A heredoc body is data, not commands: skip each body up to the
			// line holding just its delimiter.
			k := i + 1
			for _, d := range heredocs {
				for k < len(rs) {
					e := k
					for e < len(rs) && rs[e] != '\n' {
						e++
					}
					line := strings.TrimSpace(string(rs[k:e]))
					k = e + 1
					if line == d {
						break
					}
				}
			}
			heredocs, expectDelim = nil, false
			i = k - 1
		case r == '&' && (i == 0 || rs[i-1] != '&' && rs[i-1] != '|') && (i+1 == len(rs) || rs[i+1] != '&'):
			flushWord()
			if len(cur.words) == 0 && len(segs) > 0 {
				segs[len(segs)-1].bg = true // "( … ) &": the group just closed
			} else {
				cur.bg = true
			}
			flushSeg(false)
		case r == ';' || r == '&' || r == '(' || r == ')':
			flushSeg(false)
		case unicode.IsSpace(r):
			flushWord()
		default:
			word.WriteRune(r)
			inWord = true
		}
	}
	flushSeg(false)
	return segs
}

// gateTargets names what a tool call touches in this repository: the file a
// file tool reads or writes, the directory a search covers (the root when
// none is given), and for a shell command the table files it names, or the
// root when it is a code search. Paths outside the repository or excluded
// from it touch nothing; so does a command that names no code and searches
// none (ls, git status, go test).
func gateTargets(x *base.Index, tool, path, dir, command string) (targets, writes []string) {
	inRepo := func(p string) (string, bool) {
		if p == "" {
			return ".", true
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(x.Root(), filepath.FromSlash(p))
		}
		rel, inside := base.Rel(x.Root(), p)
		return rel, inside && (rel == "." || !x.Excluded(rel, reconcileOpts))
	}
	switch tool {
	case "Bash":
		t, w := shellTargets(command, x.Root(), inRepo, x.MentionedPaths)
		return t, expandDirs(x, w)
	case "apply_patch", "ApplyPatch":
		// Codex writes files through a patch: the files are its headers.
		var out []string
		for _, p := range base.ApplyPatchPaths(command) {
			if rel, ok := inRepo(p); ok && rel != "." {
				out = append(out, rel)
			}
		}
		return out, out
	case "Grep", "Glob":
		if rel, ok := inRepo(dir); ok {
			return []string{rel}, nil
		}
		return nil, nil
	case "Read":
		if rel, ok := inRepo(path); ok && path != "" && rel != "." {
			return []string{rel}, nil
		}
		return nil, nil
	default: // Edit, Write, MultiEdit: the file they write
		if rel, ok := inRepo(path); ok && path != "" && rel != "." {
			return []string{rel}, []string{rel}
		}
		return nil, nil
	}
}

// expandDirs turns a written directory (rm -r dir, mv dir …) into the table
// files under it, which are what a plan names.
func expandDirs(x *base.Index, paths []string) []string {
	var out []string
	for _, p := range paths {
		if fi, err := os.Stat(filepath.Join(x.Root(), p)); err != nil || !fi.IsDir() {
			out = append(out, p)
			continue
		}
		for _, r := range x.Rows() {
			if !r.Ignored && !r.Deleted && strings.HasPrefix(r.Path, p+"/") {
				out = append(out, r.Path)
			}
		}
	}
	return out
}

// Review checks the plan — every target outline, planned creation and
// planned deletion — against the global outline and says what to verify:
// each file's current → target fields, target lines that fail the format,
// files whose R depends on a planned file but have no target of their own,
// and a checklist. Each call records the plan it saw; when two calls in a
// row see the same plan, its files unlock for the write gate for the rest of
// the context (they stay unlocked after ortg_update clears their targets).
// The check is for the model itself: nothing waits on the user.
func Review(rulings ...string) (string, error) {
	x, err := openIndex(repoRoot())
	if err != nil {
		return "", err
	}
	snap, set := planSnapshot(x)
	overviewMu.Lock()
	defer overviewMu.Unlock()
	c := sentFor(x.Root())
	converged := c.reviews > 0 && c.lastReview == snap
	c.reviews++
	c.lastReview = snap
	defer saveSent(x.Root(), c)
	if len(set) == 0 {
		c.plan = planSeen{}
		return reviewEmpty, nil
	}
	// Later reviews of one plan say only what changed since the last: the
	// rest is still in the context, and repeating a long plan word for word
	// buries the change. A plan sharing no file with the last one reviewed
	// is a new plan and gets everything.
	pl := &c.plan
	same := false
	for p := range set {
		if _, ok := pl.Targets[p]; ok {
			same = true
			break
		}
	}
	if !same || pl.Targets == nil {
		*pl = planSeen{Targets: map[string]string{}, Quoted: map[string]bool{}, Affected: map[string]bool{}}
	}
	for p := range pl.Targets {
		if !set[p] {
			delete(pl.Targets, p)
		}
	}
	n := c.reviews
	paths := make([]string, 0, len(set))
	for p := range set {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var b strings.Builder
	fmt.Fprintf(&b, reviewHead, n, len(paths))
	affected := map[string][]string{}
	for _, p := range paths {
		r, _ := x.Row(p)
		as := fmt.Sprintf("%s\x00%t\x00%t\x00%s", r.Target, r.TargetDelete, r.Exists, r.Outline)
		if prev, ok := pl.Targets[p]; ok && prev == as {
			fmt.Fprintf(&b, reviewUnchanged, p)
		} else {
			pl.Targets[p] = as
			switch {
			case r.TargetDelete:
				fmt.Fprintf(&b, "- %s：计划删除\n", p)
			case !r.Exists:
				fmt.Fprintf(&b, "- %s：计划新建\n  目标: %s\n", p, r.Target)
			case r.Target == r.Outline:
				fmt.Fprintf(&b, reviewSame, p)
			default:
				fmt.Fprintf(&b, "- %s：%s\n  现在: %s\n  目标: %s\n", p, changedFields(r.Outline, r.Target), r.Outline, r.Target)
			}
			if r.Target != "" {
				if chk := x.CheckEntry(p, r.Target); len(chk.Errors) > 0 {
					fmt.Fprintf(&b, "  目标纲要不合格：%s\n", strings.Join(chk.Errors, "；"))
				}
			}
		}
		for _, d := range x.Dependents(p) {
			if !set[d] {
				affected[d] = append(affected[d], p)
			}
		}
	}
	if len(affected) > 0 {
		names := make([]string, 0, len(affected))
		for d := range affected {
			names = append(names, d)
		}
		sort.Strings(names)
		var fresh []string
		for _, d := range names {
			if !pl.Affected[d] {
				pl.Affected[d] = true
				fresh = append(fresh, d)
			}
		}
		if len(fresh) > 0 {
			b.WriteString(reviewAffected)
			for _, d := range fresh {
				fmt.Fprintf(&b, "- %s（Uses 依赖 %s）\n", d, strings.Join(affected[d], "、"))
			}
		}
		if k := len(names) - len(fresh); k > 0 {
			fmt.Fprintf(&b, reviewAffectedSeen, k)
		}
	}
	kc, shown, bound := keyContracts(x, set, pl.Quoted)
	if len(kc) > 0 {
		b.WriteString(reviewKeys)
		for _, l := range kc {
			b.WriteString(l + "\n")
		}
	}
	for k := range shown {
		pl.Quoted[k] = true
	}
	if ts := guardTests(x, set, shown); len(ts) > 0 {
		b.WriteString(fmt.Sprintf(reviewGuardTests, strings.Join(ts, "、")))
	}
	cs := relatedContracts(x, set, shown)
	var freshCs []string
	for _, l := range cs {
		if !pl.Quoted["rel:"+l] {
			pl.Quoted["rel:"+l] = true
			freshCs = append(freshCs, l)
		}
	}
	if len(freshCs) > 0 {
		b.WriteString(reviewContracts)
		for _, l := range freshCs {
			b.WriteString(l + "\n")
		}
	}
	if k := len(cs) - len(freshCs); k > 0 {
		fmt.Fprintf(&b, reviewContractsSeen, k)
	}
	if moves := takenOverKeys(x, set); len(moves) > 0 {
		b.WriteString(fmt.Sprintf(reviewMigration, strings.Join(moves, "；")))
	}
	if (len(kc) > 0 || len(cs) > 0) && !pl.Conflict {
		pl.Conflict = true
		b.WriteString(reviewConflict)
	}
	if !pl.Checklist {
		pl.Checklist = true
		b.WriteString(reviewChecklist)
	}
	given, missing := matchRulings(bound, rulings)
	if converged && len(missing) > 0 {
		converged = false
		fmt.Fprintf(&b, reviewRulingsMissing, strings.Join(missing, "、"))
	}
	if converged && len(given) > 0 {
		if err := recordRulings(x, given); err != nil {
			return "", err
		}
	}
	if converged {
		for _, p := range paths {
			c.planned[p] = true
			delete(c.solo, p)
		}
		c.plan = planSeen{} // the next plan is reviewed from scratch
		fmt.Fprintf(&b, reviewConverged, len(paths))
	} else {
		b.WriteString(reviewAgain)
	}
	return b.String(), nil
}

// matchRulings pairs each contract quoted in full with its ruling, "键：裁决"
// (a key may hold ASCII colons, so a ruling matches by the key it starts
// with); it returns the rulings found and the keys still without one, sorted.
func matchRulings(shown map[string]bool, rulings []string) (map[string]string, []string) {
	given := map[string]string{}
	var missing []string
	for k := range shown {
		found := false
		for _, r := range rulings {
			r = strings.TrimSpace(r)
			rest, ok := strings.CutPrefix(r, k)
			if !ok {
				continue
			}
			rest = strings.TrimLeft(rest, " ：:")
			if rest != "" {
				given[k] = strings.Join(strings.Fields(rest), " ")
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, k)
		}
	}
	sort.Strings(missing)
	return given, missing
}

// rulingMark opens the ruling a contract's text ends with.
const rulingMark = "【裁决】"

// recordRulings writes each ruling at the end of its contract's line in the
// header, replacing the one recorded before, so later plans meet the same
// decision.
func recordRulings(x *base.Index, given map[string]string) error {
	lines := strings.Split(x.Header(), "\n")
	changed := false
	for i, l := range lines {
		k, text, ok := strings.Cut(strings.TrimPrefix(l, "#【契约】"), "：")
		if !ok || !strings.HasPrefix(l, "#【契约】") {
			continue
		}
		r, has := given[strings.TrimSpace(k)]
		if !has {
			continue
		}
		if j := strings.Index(text, rulingMark); j >= 0 {
			text = strings.TrimRight(text[:j], "；; ")
		}
		lines[i] = "#【契约】" + k + "：" + text + "；" + rulingMark + r
		changed = true
	}
	if !changed {
		return nil
	}
	return x.Batch(func() error { return x.SetHeader(strings.Join(lines, "\n")) })
}

// contractLimit caps the contracts a review quotes; contractRunes cuts each.
const (
	contractLimit = 15
	contractRunes = 300
)

var identRe = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)

// identsOf returns the identifiers of six characters or more in text.
func identsOf(text string) map[string]bool {
	out := map[string]bool{}
	for _, w := range identRe.FindAllString(text, -1) {
		if len(w) >= 6 && w != "Constraints" {
			out[w] = true
		}
	}
	return out
}

// sameIdent reports whether two identifiers name the same concept: equal, or
// the shorter, of nine characters or more, inside the longer.
func sameIdent(a, b string) bool {
	if a == b {
		return true
	}
	if len(a) > len(b) {
		a, b = b, a
	}
	return len(a) >= 9 && strings.Contains(b, a)
}

// newTerms returns the identifiers the plan's target outlines bring in —
// absent from the file's current outline, outside the parentheses that cite
// tests — that carry a concept (in at most
// a tenth of the outlines, at least eight), with the document frequency of
// every identifier.
func newTerms(x *base.Index, set map[string]bool) (map[string]bool, map[string]int) {
	rows := x.Rows()
	df := map[string]int{}
	live := 0
	for _, r := range rows {
		if r.Outline == "" || r.Deleted || r.Ignored {
			continue
		}
		live++
		for w := range identsOf(r.Outline) {
			df[w]++
		}
	}
	limit := max(8, live/10)
	terms := map[string]bool{}
	for _, r := range rows {
		if !set[r.Path] || r.Target == "" || r.Target == r.Outline {
			continue
		}
		had := identsOf(r.Outline)
		for w := range identsOf(outsideParens(r.Target)) {
			if !had[w] && df[w] <= limit {
				terms[w] = true
			}
		}
	}
	return terms, df
}

// outsideParens drops parenthesized text: outlines cite guard tests there
// ("必须保持 X(测试名)"), and a test's name is no concept the plan brings in.
func outsideParens(s string) string {
	var b strings.Builder
	depth := 0
	for _, c := range s {
		switch {
		case c == '(' || c == '（':
			depth++
		case c == ')' || c == '）':
			if depth > 0 {
				depth--
			}
		case depth == 0:
			b.WriteRune(c)
		}
	}
	return b.String()
}

// relatedContracts finds the guarded behaviour a plan may break without any
// file of it depending on the guard: the "必须保持" clauses, in outlines
// outside the plan and in the header, that name an identifier a planned
// file's target outline brings in (absent from its current outline). A
// concept moved into a module (a limit a lower layer now enforces) links it by
// name, not by Uses, to the module that already guards it (the layer that
// enforced it before). Identifiers in more than a tenth of the outlines (at
// least eight) carry no concept; two match when equal, or when the shorter,
// of nine characters or more, is inside the longer. Clauses are ranked by the
// number, then the rarity, of what they share, each quoted with the
// identifiers it shares.
func relatedContracts(x *base.Index, set, shown map[string]bool) []string {
	terms, df := newTerms(x, set)
	if len(terms) == 0 {
		return nil
	}
	idents, same := identsOf, sameIdent
	rows := x.Rows()
	type hit struct {
		where, clause string
		names         []string
		score         float64
	}
	var hits []hit
	scan := func(where, text string) {
		for _, cl := range strings.FieldsFunc(text, func(c rune) bool { return c == '；' || c == ';' || c == '。' }) {
			if !strings.Contains(cl, "必须保持") {
				continue
			}
			var names []string
			score := 0.0
			for t := range terms {
				for w := range idents(cl) {
					if same(w, t) {
						names = append(names, t)
						score += 1 / float64(max(1, df[t]))
						break
					}
				}
			}
			if len(names) == 0 {
				continue
			}
			sort.Strings(names)
			cl = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(cl), "| Constraints:"))
			if r := []rune(cl); len(r) > contractRunes {
				cl = string(r[:contractRunes]) + "…"
			}
			hits = append(hits, hit{where, cl, names, score})
		}
	}
	for _, r := range rows {
		if set[r.Path] || r.Outline == "" || r.Deleted || r.Ignored {
			continue
		}
		f := outlineFields(r.Outline)
		scan(r.Path, f[len(f)-1])
	}
	for _, l := range strings.Split(x.Header(), "\n") {
		// a registered contract the key section already quoted is not repeated
		if k, _, ok := strings.Cut(strings.TrimPrefix(l, "#【契约】"), "："); ok && strings.HasPrefix(l, "#【契约】") && shown[strings.TrimSpace(k)] {
			continue
		}
		scan("纲要头", l)
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if len(hits[i].names) != len(hits[j].names) {
			return len(hits[i].names) > len(hits[j].names)
		}
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return hits[i].where < hits[j].where
	})
	var out []string
	for i, h := range hits {
		if i == contractLimit {
			out = append(out, fmt.Sprintf("（另有 %d 条未列出）", len(hits)-i))
			break
		}
		out = append(out, fmt.Sprintf("- %s：%s（同名概念：%s）", h.where, h.clause, strings.Join(h.names, "、")))
	}
	return out
}

// outlineFields cuts an outline line at its field labels, in order and at
// the first match each, the way the parser reads it: a field whose text holds
// " | " stays one field instead of shifting every field after it.
func outlineFields(line string) []string {
	var out []string
	for _, label := range []string{" | Uses:", " | API:", " | Constraints:"} {
		i := strings.Index(line, label)
		if i < 0 {
			break
		}
		out, line = append(out, line[:i]), line[i:]
	}
	return append(out, line)
}

// changedFields names the fields a target outline changes against the
// current one: the file name with its tag and Role, then Uses, API and
// Constraints.
func changedFields(cur, target string) string {
	names := []string{"标签/Role", "Uses", "API", "Constraints"}
	c, t := outlineFields(cur), outlineFields(target)
	var out []string
	for i, name := range names {
		var a, b string
		if i < len(c) {
			a = c[i]
		}
		if i < len(t) {
			b = t[i]
		}
		if a != b {
			out = append(out, name)
		}
	}
	if len(out) == 0 {
		return "目标与现在的纲要一样"
	}
	return "改 " + strings.Join(out, "、")
}

// planSnapshot is the plan as the table holds it: every file with a target
// outline, a delete mark or a planned creation, and the text that pins it,
// so two ortg_review calls can tell whether the plan moved between them.
func planSnapshot(x *base.Index) (string, map[string]bool) {
	var b strings.Builder
	set := map[string]bool{}
	for _, r := range x.Rows() {
		if r.Ignored || (r.Target == "" && !r.TargetDelete) {
			continue
		}
		set[r.Path] = true
		fmt.Fprintf(&b, "%s\x00%s\x00%t\n", r.Path, r.Target, r.TargetDelete)
	}
	return b.String(), set
}

// hostLimits are the ortg_overview size limits in the unit the host meters a
// tool result by (see outlineSize): max is the most one reply may carry,
// margin the room kept for the report and the closing lines, whole the size
// up to which a bare call sends the whole outline.
type hostLimits struct {
	max, margin, whole int
	unit               string
}

// Claude Code keeps a result up to the anthropic/maxResultSizeChars main.go
// declares, at most 500000, and saves anything larger to a file. Codex keeps
// at most tool_output_token_limit of a result in its history (the README sets
// 500000 with a context window raised by model_context_window; without them
// about 12000) whatever @exec's max_output_tokens allows, cutting the middle.
// The two hosts share the numbers in their own units. whole is only an extra
// cap the tests shrink; limits() derives the real one. Vars so the tests can
// shrink them.
var (
	claudeLimits = hostLimits{max: 500000, margin: 20000, whole: 500000, unit: "字"}
	codexLimits  = hostLimits{max: 500000, margin: 20000, whole: 500000, unit: "token"}
)

// limits gives the host's per-reply cap in its own unit — Claude Code in
// UTF-16 units, Codex in bytes/4 tokens — and the size a bare ortg_overview
// may send whole as far as that cap goes (500000 - 20000, about 480000). A
// bare call must also keep within half the context window, in estimated model
// tokens (see estTokens, contextWindow): the outline stays in the context and
// comes back after each compaction, so more than half the window leaves too
// little room to work.
func limits() hostLimits {
	l := claudeLimits
	if base.Host() == "codex" {
		l = codexLimits
	}
	l.whole = min(l.whole, l.max-l.margin)
	return l
}

// wholeShareOfWindow is the share of the context window a bare call may fill.
const wholeShareOfWindow = 50 // percent

// contextWindow is the model's context window in tokens; 0 when unknown.
func contextWindow(root string) int {
	if base.Host() == "codex" {
		return base.CodexContextWindow()
	}
	return claudeWindow(root)
}

// claudeWindow is the context window Claude Code compacts at: the auto-compact
// window it was given, else 1M for a [1m] model — named by ANTHROPIC_MODEL or
// by the model the session-start hook recorded, since Claude Code tells the
// MCP server neither — else 200K, its default without the long context.
func claudeWindow(root string) int {
	if n, err := strconv.Atoi(os.Getenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW")); err == nil && n > 0 {
		return n
	}
	model := os.Getenv("ANTHROPIC_MODEL")
	if model == "" {
		_, model = base.OverviewMark(root, "model")
	}
	if strings.Contains(model, "[1m]") {
		return 1000000
	}
	return 200000
}

// estTokens estimates what text costs the host's model, fitted on real
// outlines: Claude counted from the usage reported around ortg_overview
// results, Codex by the o200k tokenizer. CJK text runs about a token a
// character, code and English two to four characters a token, so a fixed
// characters-per-token ratio misses by a third either way.
func estTokens(s string) int {
	var cjk, other int
	for _, r := range s {
		if r >= 0x2E80 && r <= 0x9FFF || r >= 0xF900 && r <= 0xFAFF || r >= 0xFF00 && r <= 0xFFEF {
			cjk++
		} else {
			other++
		}
	}
	if base.Host() == "codex" {
		return (908*cjk + 253*other) / 1000
	}
	return (1079*cjk + 530*other) / 1000
}

// outlineSize measures text the way the host meters a tool result: Codex
// estimates tokens as UTF-8 bytes / 4 rounded up; Claude Code takes the JS
// string length, UTF-16 code units, so a character beyond the BMP counts twice.
func outlineSize(s string) int {
	if base.Host() == "codex" {
		return (len(s) + 3) / 4
	}
	n := 0
	for _, r := range s {
		n++
		if r > 0xFFFF {
			n++
		}
	}
	return n
}

// rowPrint is the version of an entry as the model saw it: a changed outline
// or target outline is sent again.
func rowPrint(r base.Row) string {
	return fmt.Sprintf("%x", sha1.Sum([]byte(fmt.Sprintf("%s\x00%s\x00%t\x00%t", r.Outline, r.Target, r.TargetDelete, r.Deleted))))
}

// moduleList lists every module (the B tag) with its size and how much
// of it this context already holds, so the model can fetch by module.
func moduleList(x *base.Index, include func(base.Row) bool, c *sentCache) string {
	type agg struct{ files, size, sent int }
	mods := map[string]*agg{}
	measure := outlineSize // Codex shows the estimate, as the list head does
	if base.Host() == "codex" {
		measure = estTokens
	}
	for _, r := range x.Rows() {
		if !include(r) {
			continue
		}
		m := moduleKey(r)
		a := mods[m]
		if a == nil {
			a = &agg{}
			mods[m] = a
		}
		a.files++
		a.size += measure(r.Outline)
		if c.rows[r.Path] == rowPrint(r) {
			a.sent++
		}
	}
	names := make([]string, 0, len(mods))
	for m := range mods {
		names = append(names, m)
	}
	sort.Slice(names, func(i, j int) bool { return mods[names[i]].size > mods[names[j]].size })
	unit := limits().unit
	var b strings.Builder
	for _, m := range names {
		a := mods[m]
		fmt.Fprintf(&b, "\n- %s  %d 个文件  约 %d %s  已在上下文 %d", m, a.files, a.size, unit, a.sent)
	}
	return b.String()
}

// Scan is the CLI reconciliation; it creates the table like Overview.
func Scan() (string, error) {
	x, note, err := openOrBootstrap(repoRoot())
	if err != nil {
		return "", err
	}
	s, err := reconcileAndApply(x)
	if err != nil {
		return "", err
	}
	if note != "" {
		note += "\n"
	}
	return note + s.report(), nil
}

// Graph starts CBM's permanent daemon and refreshes this repository's call
// graph; the session hook runs it detached. Silent without CBM.
func Graph() (string, error) {
	x, err := openIndex(repoRoot())
	if err != nil {
		return "", err
	}
	if err := x.WarmGraph(); err != nil {
		return "", err
	}
	return "ortg: CBM 常驻进程已就绪，调用图已刷新(未装 CBM 或 ORTG_CBM=off 时什么都不做)", nil
}

// Status reconciles read-only and reports.
func Status() (string, error) {
	x, err := openIndex(repoRoot())
	if err != nil {
		return "", err
	}
	facts, err := x.Reconcile(reconcileOpts)
	if err != nil {
		return "", err
	}
	s := summary{byState: map[state][]string{}}
	for _, f := range facts {
		st := classify(f)
		s.byState[st] = append(s.byState[st], f.Path)
	}
	return s.report(), nil
}

// sQuota is the S-field rune budget per C importance level, and the only place
// the numbers live: checkOrError enforces them and quotaLine renders the same
// table into the model-facing texts in prompt.go, so raising a budget here
// reaches the checker and the prompts together. A repo whose ortg.tsv header was
// written by an earlier build still carries its own copy — resync that with
// ortg_header; headerTemplate only governs tables created from now on.
var sQuota = map[int]int{9: 2000, 8: 1200, 7: 500, 6: 500, 5: 500, 4: 500, 3: 100, 2: 100, 1: 100}

// contractQuota is the budget of the test contract clauses beside the other
// constraints' q: half of it, at least 100 runes, so a file with a small
// quota can still state what its tests lock.
func contractQuota(q int) int { return max(q/2, 100) }

// quotaLine renders sQuota as "C9≤2000字 C8≤1200字 C7-4≤500字 C3-1≤100字", collapsing
// adjacent levels that share a budget into one range.
func quotaLine() string {
	var parts []string
	for hi := 9; hi >= 1; {
		q, ok := sQuota[hi]
		if !ok {
			hi--
			continue
		}
		lo := hi
		for lo > 1 && sQuota[lo-1] == q {
			lo--
		}
		label := fmt.Sprintf("C%d", hi)
		if lo != hi {
			label = fmt.Sprintf("C%d-%d", hi, lo)
		}
		parts = append(parts, fmt.Sprintf("%s≤%d字", label, q))
		hi = lo - 1
	}
	return strings.Join(parts, " ")
}

func checkOrError(x *base.Index, rel, line string) (string, error) {
	c := x.CheckEntry(rel, line)
	if len(c.Errors) > 0 {
		return "", fmt.Errorf("纲要行不合法，请修正后重提：%s", strings.Join(c.Errors, "；"))
	}
	var warn []string
	warn = append(warn, c.TagIssues...)
	if strings.Contains(line, "单测入口") {
		warn = append(warn, testOnlyWording)
	}
	if isDocFile(rel) {
		if c.ConsLen > docQuota {
			warn = append(warn, fmt.Sprintf("Constraints 字段 %d 字超过文档配额 %d", c.ConsLen, docQuota))
		}
	} else if q, ok := sQuota[c.C]; ok {
		if other := c.ConsLen - c.ContractLen; other > q {
			warn = append(warn, fmt.Sprintf("Constraints 里「必须保持」以外的部分 %d 字超过 C%d 配额 %d", other, c.C, q))
		}
		if cq := contractQuota(q); c.ContractLen > cq {
			warn = append(warn, fmt.Sprintf("Constraints 里「必须保持」子句共 %d 字超过 C%d 的契约配额 %d", c.ContractLen, c.C, cq))
		}
	}
	if len(warn) > 0 {
		return "警告: " + strings.Join(warn, "；"), nil
	}
	return "", nil
}

// checkUses refuses a line whose Uses names something other than a
// repository path — a package, a module, a bare file name: dependents are
// found by path, and a name that resolves to no path drops the edge without a
// word (an outline naming a package hid a dependent's guard from a plan that
// changed the package).
func checkUses(x *base.Index, line string) error {
	var bad []string
	for _, it := range base.UsesItems(line) {
		if !x.ResolvesUses(it) {
			bad = append(bad, it)
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf(usesNotPath, strings.Join(bad, "、"))
	}
	return nil
}

// checkKeys refuses a line declaring a contract key the header's 【契约】
// table does not register: the contract text lives there, once, and a key
// without it links files to nothing anyone can read.
func checkKeys(x *base.Index, line string) error {
	reg := x.Contracts()
	var bad []string
	for _, k := range base.KeysOf(line) {
		if _, ok := reg[k]; !ok {
			bad = append(bad, k)
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf(keysUnregistered, strings.Join(bad, "、"))
	}
	return nil
}

// pubSymbolRes find the names a source file exports, by language: Rust pub
// items (not pub(crate)), Go exported functions, methods and types, JS/TS
// exports. Other languages export by convention only and are not checked.
var pubSymbolRes = map[string][]*regexp.Regexp{
	".rs": {regexp.MustCompile(`(?m)^\s*pub\s+(?:(?:async|const|unsafe|extern\s+"[^"]*")\s+)*(?:fn|struct|enum|trait|type|const|static|mod|union)\s+([A-Za-z_]\w*)`)},
	".go": {regexp.MustCompile(`(?m)^func\s+(?:\([^)]*\)\s*)?([A-Z]\w*)`), regexp.MustCompile(`(?m)^type\s+([A-Z]\w*)`)},
	".ts": {jsExportRe}, ".tsx": {jsExportRe}, ".js": {jsExportRe}, ".jsx": {jsExportRe}, ".mjs": {jsExportRe},
}

var jsExportRe = regexp.MustCompile(`(?m)^export\s+(?:default\s+)?(?:async\s+)?(?:function\*?|class|const|let|var|interface|type|enum)\s+([A-Za-z_$][\w$]*)`)

// pubSymbols returns the names src exports by rel's language; nil when the
// language is not checked.
func pubSymbols(rel, src string) map[string]bool {
	res := pubSymbolRes[strings.ToLower(path.Ext(rel))]
	if res == nil {
		return nil
	}
	out := map[string]bool{}
	for _, re := range res {
		for _, m := range re.FindAllStringSubmatch(src, -1) {
			out[m[1]] = true
		}
	}
	return out
}

// newAPIMax caps the names checkNewAPI reports.
const newAPIMax = 8

// checkNewAPI refuses an outline whose API leaves out a name the file now
// exports and its committed version did not: a public interface added while
// the outline stays as it was ("=") hides the change from every check keyed
// on what the outline says changed. Files new to the repository, tests,
// documents and unchecked languages pass.
func checkNewAPI(x *base.Index, rel, line string) error {
	if isDocFile(rel) || isTestPath(rel) {
		return nil
	}
	b, err := os.ReadFile(filepath.Join(x.Root(), filepath.FromSlash(rel)))
	if err != nil {
		return nil
	}
	now := pubSymbols(rel, string(b))
	if len(now) == 0 {
		return nil
	}
	head, ok := x.HeadContent(rel)
	if !ok {
		return nil
	}
	before := pubSymbols(rel, head)
	f := outlineFields(line)
	if len(f) < 3 {
		return nil
	}
	api := f[2]
	var missing []string
	for name := range now {
		if !before[name] && !strings.Contains(api, name) {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)
	if len(missing) > newAPIMax {
		missing = append(missing[:newAPIMax], "…")
	}
	return fmt.Errorf(newAPINotListed, rel, strings.Join(missing, "、"))
}

// orphanKeys lists the registered contract keys no outline declares, sorted.
func orphanKeys(x *base.Index) []string {
	var out []string
	for k := range x.Contracts() {
		if len(x.KeyHolders(k)) == 0 {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// literalKinds are the contract key kinds whose value is a literal the
// source spells out — a route, a message or event name, a config or
// environment key — so a file using it can be found without a declaration.
var literalKinds = map[string]bool{"route": true, "ws": true, "msg": true, "event": true, "topic": true, "cfg": true, "env": true}

// keyLiteral returns the literal a key's value stands for in the source, ""
// for a key of another kind: "route:POST /api/v1/order" → "/api/v1/order".
func keyLiteral(key string) string {
	kind, val, ok := strings.Cut(key, ":")
	if !ok || !literalKinds[kind] {
		return ""
	}
	f := strings.Fields(val)
	if len(f) == 0 {
		return ""
	}
	return f[len(f)-1]
}

// takenOverKeys names the contracts a planned file takes part in after the
// change but not before — its target declares a key its outline does not: a
// duty moving into this file, whose old holders' guards it must now meet on
// its own. Each item reads "file 承担 key(原参与者 a、b)".
func takenOverKeys(x *base.Index, set map[string]bool) []string {
	paths := make([]string, 0, len(set))
	for p := range set {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var out []string
	for _, p := range paths {
		r, ok := x.Row(p)
		if !ok || r.Target == "" {
			continue
		}
		had := map[string]bool{}
		for _, k := range base.KeysOf(r.Outline) {
			had[k] = true
		}
		for _, k := range base.KeysOf(r.Target) {
			if had[k] {
				continue
			}
			var others []string
			for _, h := range x.KeyHolders(k) {
				if h != p {
					others = append(others, h)
				}
			}
			out = append(out, fmt.Sprintf("%s 承担 %s(原参与者 %s)", p, k, strings.Join(others, "、")))
		}
	}
	return out
}

// guardTestMax caps the tests a review or an impact note names.
const guardTestMax = 40

// guardTests lists the tests guarding the files in set: those the "必须保持"
// clauses of their current and target outlines cite, then those of the
// contracts keys names (the ones a review quotes in full), in order, once
// each, at most guardTestMax with a count of the rest.
func guardTests(x *base.Index, set, keys map[string]bool) []string {
	paths := make([]string, 0, len(set))
	for p := range set {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var texts []string
	for _, p := range paths {
		if r, ok := x.Row(p); ok {
			texts = append(texts, r.Outline, r.Target)
		}
	}
	reg := x.Contracts()
	names := make([]string, 0, len(keys))
	for k := range keys {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		texts = append(texts, reg[k])
	}
	var out []string
	seen := map[string]bool{}
	for _, t := range texts {
		for _, n := range base.GuardTests(t) {
			if !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	if len(out) > guardTestMax {
		out = append(out[:guardTestMax], fmt.Sprintf("…另 %d 个", len(out)-guardTestMax))
	}
	return out
}

// keyFullMax is how many contracts a review quotes in full at least;
// keyFullCap at most; hubKeyMax is how many keys a file may declare before it
// counts as a hub (the file turning flags into settings takes part in most
// contracts, so its keys alone say little about this plan).
const (
	keyFullMax = 6
	keyFullCap = 15
	hubKeyMax  = 8
)

// keyContracts lists the contracts the plan's files take part in, by the
// keys they declare now or in their target. Quoted in full: every key of a
// planned file that is no hub and every key whose text names an identifier
// the plan brings in — what the plan is bound to, whether or not its targets
// say what changes ("=" says nothing); keys only hubs share say little, so
// they do not count — topped up to keyFullMax by rank and capped at
// keyFullCap, bound keys first; the rest are named. Rank: identifiers the plan's targets bring
// in that the contract text names, then files whose outline changes that
// declare the key, then planned files that do. A full entry carries its text,
// every party and the identifiers that matched; a literal key also names the
// files using its literal undeclared. It returns the lines, the keys quoted
// in full and, of those, the ones the plan is bound to (all but the top-up):
// each of these needs a ruling before the plan converges. A key in quoted was
// quoted in full by an earlier review of this plan: it counts as quoted but
// gets one short line.
func keyContracts(x *base.Index, set, quoted map[string]bool) ([]string, map[string]bool, map[string]bool) {
	type cand struct {
		key            string
		hits           []string
		changed, plans int
		core           bool
	}
	byKey := map[string]*cand{}
	for p := range set {
		r, _ := x.Row(p)
		changed := r.Target != "" && r.Target != r.Outline
		seen := map[string]bool{}
		for _, k := range append(base.KeysOf(r.Outline), base.KeysOf(r.Target)...) {
			seen[k] = true
		}
		hub := len(seen) > hubKeyMax
		for k := range seen {
			c := byKey[k]
			if c == nil {
				c = &cand{key: k}
				byKey[k] = c
			}
			c.plans++
			if changed {
				c.changed++
			}
			if !hub {
				c.core = true
			}
		}
	}
	if len(byKey) == 0 {
		return nil, nil, nil
	}
	reg := x.Contracts()
	terms, _ := newTerms(x, set)
	cands := make([]*cand, 0, len(byKey))
	for _, c := range byKey {
		text := identsOf(reg[c.key])
		for t := range terms {
			for w := range text {
				if sameIdent(w, t) {
					c.hits = append(c.hits, t)
					break
				}
			}
		}
		sort.Strings(c.hits)
		cands = append(cands, c)
	}
	sort.Slice(cands, func(i, j int) bool {
		a, b := cands[i], cands[j]
		if ab, bb := a.core || len(a.hits) > 0, b.core || len(b.hits) > 0; ab != bb {
			return ab
		}
		if len(a.hits) != len(b.hits) {
			return len(a.hits) > len(b.hits)
		}
		if a.changed != b.changed {
			return a.changed > b.changed
		}
		if a.plans != b.plans {
			return a.plans > b.plans
		}
		return a.key < b.key
	})
	full, bound := map[string]bool{}, map[string]bool{}
	for _, c := range cands {
		if len(full) < keyFullCap && (c.core || len(c.hits) > 0) {
			full[c.key] = true
			bound[c.key] = true
		}
	}
	for _, c := range cands {
		if len(full) >= keyFullMax {
			break
		}
		full[c.key] = true
	}
	var out, rest []string
	shown := map[string]bool{}
	for _, c := range cands {
		holders := x.KeyHolders(c.key)
		if !full[c.key] {
			rest = append(rest, fmt.Sprintf("【%s】(%d)", c.key, len(holders)))
			continue
		}
		shown[c.key] = true
		if quoted[c.key] {
			out = append(out, fmt.Sprintf(reviewKeySeen, c.key))
			continue
		}
		line := fmt.Sprintf("- 【%s】%s\n  参与者：%s", c.key, reg[c.key], strings.Join(holders, "、"))
		if len(c.hits) > 0 {
			line += "\n  计划新引入的同名概念：" + strings.Join(c.hits, "、")
		}
		out = append(out, line)
		if lit := keyLiteral(c.key); lit != "" {
			held := map[string]bool{}
			for _, h := range holders {
				held[h] = true
			}
			var miss []string
			for _, p := range x.FilesContaining(lit) {
				if !held[p] && !x.Observed(p, reconcileOpts) {
					miss = append(miss, p)
				}
			}
			if len(miss) > 0 {
				out = append(out, "  "+fmt.Sprintf(reviewKeyMissing, lit, strings.Join(miss, "、")))
			}
		}
	}
	if len(rest) > 0 {
		out = append(out, fmt.Sprintf(reviewKeysRest, strings.Join(rest, "、")))
	}
	return out, shown, bound
}

// Update stores a submitted outline line and applies any target outline.
func Update(rel, line string) (string, error) {
	x, err := openIndex(repoRoot())
	if err != nil {
		return "", err
	}
	if rel, err = normRel(x.Root(), rel); err != nil {
		return "", err
	}
	// a fresh table takes the documents' outlines first, nothing else
	if headerUnwritten(x.Header()) && !isDocFile(rel) {
		return "", errors.New(headerFirstText(x))
	}
	var warn string
	var depChanged bool  // a new dependency edge may outdate the header
	var distill []string // documents to distill once the last outline lands
	err = x.Batch(func() error {
		if f, _ := x.Inspect(rel); !f.Exists {
			return fmt.Errorf("磁盘上不存在 %s，无法提交纲要", rel)
		}
		dir := isDirRow(line)
		if st, err := os.Stat(filepath.Join(x.Root(), filepath.FromSlash(rel))); err == nil && st.IsDir() != dir {
			return fmt.Errorf(dirRowMismatch, rel)
		}
		// A folder outline stands for a directory kept out of per-file
		// outlines, so the tier of the files under it does not apply to it.
		if !dir && x.Excluded(rel, reconcileOpts) {
			return fmt.Errorf("%s 命中排除或忽略规则，不进纲要", rel)
		}
		if !dir && x.Observed(rel, reconcileOpts) {
			return fmt.Errorf("%s 是观察档(observe_dirs/observe_files)：只记指纹、不写纲要，写了也不会渲染进整份纲要。确实要为它建立认知，把它加进 keep_files", rel)
		}
		w, err := checkOrError(x, rel, line)
		if err != nil {
			return err
		}
		if err := checkUses(x, line); err != nil {
			return err
		}
		if err := checkKeys(x, line); err != nil {
			return err
		}
		if err := checkNewAPI(x, rel, line); err != nil {
			return err
		}
		warn = w
		// A dependency edge that appeared or went may outdate the header's
		// account of the layers; a fresh table has no such account yet, and a
		// first outline while other files still lack one is a bulk build.
		old, _ := x.Row(rel)
		depChanged = depsOf(old.Outline) != depsOf(line) && !headerUnwritten(x.Header()) &&
			(old.Outline != "" || !othersUnoutlined(x, rel))
		if err := x.SetOutline(rel, line); err != nil {
			return err
		}
		// The first outline of the last file without one closes the first
		// build: what the documents say now belongs in the header and the
		// source outlines, and the documents can move to the observe tier.
		if old.Outline == "" && !headerUnwritten(x.Header()) && !othersUnoutlined(x, rel) {
			distill, _ = docs(x)
		}
		return x.ClearTarget(rel)
	})
	if err != nil {
		return "", err
	}
	overviewMu.Lock()
	if c := sentFor(x.Root()); c.solo[rel] {
		delete(c.solo, rel)
		saveSent(x.Root(), c)
	}
	overviewMu.Unlock()
	x.RefreshGraph()
	msg := fmt.Sprintf("已提交 %s 的纲要，指纹已前移，状态：对齐", rel)
	if warn != "" {
		msg += "\n" + warn
	}
	if depChanged {
		msg += "\n" + fmt.Sprintf(headerDepNote, rel)
	}
	if len(distill) > 0 {
		msg += "\n" + fmt.Sprintf(distillNote, docList(distill))
	}
	if note, _ := removedTestedNote(x, rel); note != "" {
		msg += "\n" + note
	}
	return msg, nil
}

// Target writes one target outline, a delete mark, or imports a document.
func Target(rel, line string, del bool, document string) (string, error) {
	x, err := openIndex(repoRoot())
	if err != nil {
		return "", err
	}
	// A whole document carries its own header, so it may write the header a
	// fresh table lacks; a single target may not be planned before one exists.
	if document == "" && headerUnwritten(x.Header()) {
		return "", errors.New(headerFirstText(x))
	}
	var msg string
	err = x.Batch(func() error {
		if document != "" {
			// AllTarget: this tool submits a plan. Without it the bootstrap
			// split would write existing files' lines into Outline and wipe
			// out the current knowledge the plan is supposed to sit next to.
			res, err := x.ImportOutlineWith(stripBodyMarker(document), base.ImportOptions{
				Excluded:  func(rel string) bool { return x.Excluded(rel, reconcileOpts) },
				AllTarget: true,
			})
			if err != nil {
				return err
			}
			if headerUnwritten(x.Header()) { // rolls the import back
				return errors.New(headerFirstText(x))
			}
			msg = fmt.Sprintf("已导入 %d 个目录段、%d 条目标纲要（新建 %d 行）", res.Sections, res.Entries, res.Created)
			return nil
		}
		if rel == "" {
			return errors.New("需要 path，或用 document 整份导入")
		}
		var err error
		if rel, err = normRel(x.Root(), rel); err != nil {
			return err
		}
		if del {
			msg = fmt.Sprintf("已标记 %s 计划删除", rel)
			return x.SetTargetDelete(rel)
		}
		same := strings.TrimSpace(line) == "="
		if same {
			// The code changes, the outline does not: the target is the
			// current outline, written by the program instead of retyped.
			r, ok := x.Row(rel)
			if !ok || !r.Exists || r.Outline == "" {
				return fmt.Errorf("%s 还没有纲要，不能用 = 表示纲要不变；写出完整的目标纲要", rel)
			}
			line = r.Outline
		}
		w, err := checkOrError(x, rel, line)
		if err != nil {
			return err
		}
		if !same {
			if err := checkUses(x, line); err != nil {
				return err
			}
			if err := checkKeys(x, line); err != nil {
				return err
			}
		}
		msg = fmt.Sprintf("已写入 %s 的目标纲要", rel)
		if same {
			msg += "（同现在：只改实现，纲要不变）\n" + sameApiNote
			if r, ok := x.Row(rel); ok {
				if keys := base.KeysOf(r.Outline); len(keys) > 0 {
					msg += "\n" + fmt.Sprintf(sameWithKeysNote, rel, strings.Join(keys, "、"))
				}
			}
		}
		if w != "" {
			msg += "\n" + w
		}
		return x.SetTarget(rel, line)
	})
	return msg, err
}

// TargetItems writes several targets in one call, one item per file:
// "path outline", "path =" (the outline stays) or "path 删除" (planned
// deletion). Each item is reported on its own line; a failed item leaves the
// others written, and the call fails only when every item failed.
func TargetItems(items []string) (string, error) {
	if len(items) == 0 {
		return "", errors.New("items 为空")
	}
	var lines []string
	failed := 0
	for _, it := range items {
		it = strings.TrimSpace(it)
		path, rest := it, ""
		if i := strings.IndexAny(it, " \t"); i >= 0 {
			path, rest = it[:i], strings.TrimSpace(it[i+1:])
		}
		var msg string
		var err error
		switch {
		case path == "" || rest == "":
			err = fmt.Errorf("%q 不是 \"路径 目标纲要行\"", it)
		case rest == "删除":
			msg, err = Target(path, "", true, "")
		default:
			msg, err = Target(path, rest, false, "")
		}
		if err != nil {
			failed++
			msg = fmt.Sprintf("%s：失败：%v", path, err)
		}
		lines = append(lines, msg)
	}
	out := strings.Join(lines, "\n")
	if failed == len(items) {
		return "", errors.New(out)
	}
	return out, nil
}

// depsOf is the dependencies an outline line's Uses field names, cut the way
// the parser reads it; "-" and a missing outline both mean none.
func depsOf(line string) string {
	f := outlineFields(line)
	if len(f) < 2 {
		return ""
	}
	if u := strings.TrimSpace(strings.TrimPrefix(f[1], " | Uses:")); u != "-" {
		return u
	}
	return ""
}

// othersUnoutlined reports whether a live file other than rel, outside the
// observe tier, still lacks an outline.
func othersUnoutlined(x *base.Index, rel string) bool {
	for _, r := range x.Rows() {
		if r.Path != rel && r.Outline == "" && !r.Ignored && !r.Deleted && r.Exists && !x.Observed(r.Path, reconcileOpts) {
			return true
		}
	}
	return false
}

// headerUnwritten reports a header still holding the template's 【部署】 or
// 【系统】 placeholder line: nobody has described the system yet, so neither
// outlines nor plans have the global picture they must fit into.
func headerUnwritten(header string) bool {
	for _, l := range strings.Split(headerTemplate, "\n") {
		if (strings.HasPrefix(l, "#【部署】(") || strings.HasPrefix(l, "#【系统】(")) && strings.Contains(header, l) {
			return true
		}
	}
	return false
}

// docLimit caps the documents headerFirstText names; the overview still
// shows every one as a bare file name.
const docLimit = 20

// docQuota is the Constraints budget of a document's outline whatever its
// importance: the conventions and decisions a document states are what the
// first build distills into the header and the source outlines.
const docQuota = 1200

// isDocFile reports a documentation file by its path alone, the same rule for
// every repository: Markdown, reST, AsciiDoc or text, or anything under a
// docs, doc, guide or guides directory.
func isDocFile(rel string) bool {
	p := strings.ToLower(strings.Trim(rel, "/"))
	for _, d := range []string{"/docs/", "/doc/", "/guide/", "/guides/"} {
		if strings.Contains("/"+p+"/", d) {
			return true
		}
	}
	for _, e := range []string{".md", ".rst", ".adoc", ".txt"} {
		if strings.HasSuffix(p, e) {
			return true
		}
	}
	return false
}

// docs lists the live documents outside the observe tier, READMEs first, and
// those of them that still lack an outline.
func docs(x *base.Index) (all, missing []string) {
	for _, r := range x.Rows() {
		if r.Ignored || r.Deleted || !r.Exists || !isDocFile(r.Path) || x.Observed(r.Path, reconcileOpts) {
			continue
		}
		all = append(all, r.Path)
		if r.Outline == "" {
			missing = append(missing, r.Path)
		}
	}
	readme := func(p string) bool { return strings.HasPrefix(strings.ToLower(path.Base(p)), "readme") }
	for _, l := range [][]string{all, missing} {
		sort.SliceStable(l, func(i, j int) bool { return readme(l[i]) && !readme(l[j]) })
	}
	return all, missing
}

// docList names documents, at most docLimit of them with the total after.
func docList(d []string) string {
	if len(d) <= docLimit {
		return strings.Join(d, "、")
	}
	return strings.Join(d[:docLimit], "、") + fmt.Sprintf(" 等 %d 个", len(d))
}

// headerFirstText is the guidance for a table whose header is unwritten, by
// how far the first build has come: documents to outline, the header to
// write from them, or — with no documents — the header from the code.
func headerFirstText(x *base.Index) string {
	all, missing := docs(x)
	switch {
	case len(missing) > 0:
		return fmt.Sprintf(headerFirstNote, docList(missing), docQuota)
	case len(all) > 0:
		return headerFromDocsNote
	}
	return headerNoDocsNote
}

// Align advances fingerprints after a human confirmed the outlines.
func Align(mode string, paths []string) (string, error) {
	x, err := openIndex(repoRoot())
	if err != nil {
		return "", err
	}
	n := 0
	err = x.Batch(func() error {
		facts, err := x.Reconcile(reconcileOpts)
		if err != nil {
			return err
		}
		if mode == "all" {
			paths = nil
			for _, f := range facts {
				if f.Exists && !f.Ignored && !f.IsNew {
					paths = append(paths, f.Path)
				}
			}
		}
		n = len(paths)
		return x.Align(paths)
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("已对齐 %d 个文件的指纹", n), nil
}

// Header replaces the header and/or the ignore and directory configuration.
func Header(header string, ignoreDirs, ignoreFiles, keepFiles, observeDirs, observeFiles, dirs []string) (string, error) {
	x, err := openIndex(repoRoot())
	if err != nil {
		return "", err
	}
	err = x.Batch(func() error {
		cfg := x.Config()
		if ignoreDirs != nil {
			cfg.IgnoreDirs = ignoreDirs
		}
		if ignoreFiles != nil {
			cfg.IgnoreFiles = ignoreFiles
		}
		if keepFiles != nil {
			cfg.KeepFiles = keepFiles
		}
		if observeDirs != nil {
			cfg.ObserveDirs = observeDirs
		}
		if observeFiles != nil {
			cfg.ObserveFiles = observeFiles
		}
		if dirs != nil {
			cfg.Dirs = map[string]string{}
			for _, d := range dirs {
				k, v, _ := strings.Cut(d, "=")
				cfg.Dirs[strings.Trim(strings.TrimSpace(k), "/")] = strings.TrimSpace(v)
			}
		}
		if err := x.SetConfig(cfg); err != nil {
			return err
		}
		if header == "" {
			return nil
		}
		// The first build writes the documents' outlines before the header
		// (the config above may first move unneeded documents to observe).
		if headerUnwritten(x.Header()) && !headerUnwritten(header) {
			if _, missing := docs(x); len(missing) > 0 {
				return fmt.Errorf(headerDocsMissingNote, docList(missing))
			}
		}
		return x.SetHeader(header)
	})
	if err != nil {
		return "", err
	}
	msg := "纲要头与配置已更新"
	if orphan := orphanKeys(x); len(orphan) > 0 {
		msg += "\n" + fmt.Sprintf(keysOrphan, strings.Join(orphan, "、"))
	}
	return msg, nil
}

// ---- hooks ----

// HookReply is pure: it returns the text for a host event, or "" to stay
// silent. Only the session event writes (reconciliation); nothing creates a
// table.
// decisionKey marks the session as having shown the pending-decision options.
const decisionKey = "decision"

// rulesText is the rules block plus whatever the current host needs on top:
// only Codex truncates an oversized tool result, so only Codex is told how to
// raise the budget. Telling every host would spend context on a directive the
// others do not understand.
func rulesText() string {
	if base.Host() == "codex" {
		return rulesBlock + "\n" + codexBudgetRule
	}
	return rulesBlock
}

// toolDescription is a tool's description for the current host. The Codex
// budget rule rides on ortg_overview too: the session that builds a table
// starts with no table, so no hook injects the rules block there.
func toolDescription(name string) string {
	if name == "ortg_overview" && base.Host() == "codex" {
		return toolDescriptions[name] + "\n" + codexBudgetRule
	}
	return toolDescriptions[name]
}

// HookReply is HookReplyFull without the user-visible line.
func HookReply(event string, in base.HookInput) string {
	c, _ := HookReplyFull(event, in)
	return c
}

// HookReplyFull answers a hook event: the first string is context for the
// model, the second is a one-liner shown to the user (only the session
// reconcile and a corrupt-table stop produce one).
func HookReplyFull(event string, in base.HookInput) (string, string) {
	start := in.Cwd
	if start == "" {
		start, _ = os.Getwd()
	}
	root := base.FindRepoRoot(start)
	if event == "stop" {
		return stopNote(root, in), "" // runs every turn: no table unless a run is alive
	}
	open := base.Open // the other events only read: an old table stays as it is
	if event == "session" {
		open = openTable // the session reconciles and may write: upgrade it
	}
	x, err := open(root)
	if err != nil {
		if event == "session" && !errors.Is(err, base.ErrNoTable) {
			msg := tableBrokenNote
			if errors.Is(err, base.ErrFormat) {
				msg = tableFormatNote
			}
			msg += err.Error()
			return msg, msg
		}
		return "", ""
	}
	switch event {
	case "session":
		spawnGraph(x)
		if in.Model != "" {
			base.MarkOverview(root, "model", in.Model)
		}
		switch in.Source {
		case "compact", "clear":
			// The running MCP server still counts the outline as read; the
			// context that held it is gone, so let the next overview serve it.
			base.MarkOverview(root, "reset", "")
		case "resume":
			// The context is the old one: a new MCP server may load the cache
			// the old server saved.
		default:
			// A new conversation: a saved cache describes another context.
			// Servers already running keep theirs — another session's.
			base.MarkOverview(root, "start", "")
		}
		if in.Source != "resume" { // resume keeps the context; startup/clear/compact lose it
			base.ResetInjected(in.SessionID)
		} else {
			base.ResetInjectedKey(in.SessionID, decisionKey)
		}
		type res struct {
			s   summary
			err error
		}
		ch := make(chan res, 1)
		go func() {
			s, err := reconcileAndApply(x)
			ch <- res{s, err}
		}()
		select {
		case r := <-ch:
			if r.err != nil {
				return rulesText(), ""
			}
			return rulesText() + "\n\n" + r.s.report() + "\n" + sessionFetchNote + sessionReportNote,
				"ortg 对账：" + r.s.oneLine()
		case <-time.After(4 * time.Second):
			return rulesText() + "\n\n" + sessionFetchNote, ""
		}
	case "subagent":
		return rulesText(), ""
	case "compact":
		// PostCompact is the moment the context loses the outline on both
		// hosts. Codex only fires SessionStart(compact) lazily, at the next
		// turn, so the mark is written here too; a second mark is harmless.
		// Silent: Codex rejects context output on PostCompact ("invalid
		// PostCompact hook JSON output"), and the SessionStart(compact) that
		// follows already restates the rules and asks for ortg_overview.
		base.MarkOverview(root, "reset", "")
		return "", ""
	case "prompt":
		// The host does not reliably render a resume-time SessionStart message,
		// but it always renders one on a submitted prompt: the pending decision
		// rides here so that whether the user sees it never depends on the model.
		if !base.InjectOnce(in.SessionID, decisionKey) {
			return "", ""
		}
		s, err := reconcileAndApply(x)
		if err != nil || !s.needsDecision() {
			return "", ""
		}
		return "", decisionPrompt
	}
	paths := hookInputPaths(in)
	if len(paths) == 0 {
		// Bash and friends carry no file path: a file read with cat/sed and an
		// edit made with sed/heredoc are both invisible to the Read and
		// Edit|Write matchers, which is exactly how outlines stay missing or go
		// stale unnoticed. Fall back to the command text and the table itself.
		if event == "post" {
			return shellNote(x, in.SessionID, in.Command, in.ToolOutput, in.Background), ""
		}
		return "", ""
	}
	seen := make(map[string]bool)
	var replies []string
	for _, filePath := range paths {
		if !filepath.IsAbs(filePath) {
			filePath = filepath.Join(start, filepath.FromSlash(filePath))
		}
		rel, inside := base.Rel(root, filePath)
		if !inside || rel == "." || seen[rel] {
			continue
		}
		seen[rel] = true
		reply := fileHookReply(event, in, x, rel)
		if event == "post" && isFileEditTool(in.ToolName) && isDepManifest(rel) {
			if n := depBumpNoteOnce(x, in.SessionID, []string{rel}); n != "" {
				reply = strings.TrimPrefix(reply+"\n"+n, "\n")
			}
		}
		if reply != "" {
			replies = append(replies, reply)
		}
	}
	return strings.Join(replies, "\n"), ""
}

// hookInputPaths preserves the single-file hook API while allowing Codex's
// apply_patch payload to name every file touched by one tool call.
func hookInputPaths(in base.HookInput) []string {
	seen := make(map[string]bool)
	var paths []string
	add := func(p string) {
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		paths = append(paths, p)
	}
	add(in.FilePath)
	for _, p := range in.FilePaths {
		add(p)
	}
	return paths
}

func isFileEditTool(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.NewReplacer("_", "", "-", "").Replace(name)
	switch name {
	case "edit", "write", "multiedit", "applypatch":
		return true
	default:
		return false
	}
}

func fileHookReply(event string, in base.HookInput, x *base.Index, rel string) string {
	// Observed files are watched, not authored: never ask for an outline. An
	// edit under a folder outline still gets that outline, since its
	// description ("read-only, patch upstream") is the constraint that matters.
	if x.Excluded(rel, reconcileOpts) || x.Observed(rel, reconcileOpts) {
		if event == "pre" {
			return folderPreNote(x, in.SessionID, rel)
		}
		return ""
	}
	switch event {
	case "pre":
		if !base.InjectOnce(in.SessionID, rel) {
			return ""
		}
		row, ok := x.Row(rel)
		var s string
		switch {
		case ok && row.Outline != "":
			s = "ortg: 该文件的纲要(改动前请遵循其中约束):\n" + row.Outline
			if row.Target != "" || row.TargetDelete {
				s += "\n" + targetNote(rel, row)
			}
		case ok && (row.Target != "" || row.TargetDelete):
			s = targetNote(rel, row)
		default:
			s = "ortg: " + rel + " 尚无纲要。\n" + skeletonRule
		}
		// The answer is also the graph from before the edit, which the post
		// hook needs; keeping it spares that hook one CBM call.
		rs := x.Related(rel, 2*relatedMax, relatedTimeout)
		base.SaveRelations(in.SessionID, rel, rs)
		return s + relatedNote(x, rs)
	case "post":
		f, ok := x.Inspect(rel)
		if !f.Exists {
			if ok && isFileEditTool(in.ToolName) {
				return fmt.Sprintf("ortg: 已删除 %s。在回复用户之前，先调用 ortg_target，以 path=%q、delete=true 标记删除——这是改动的一部分，不征求同意、不列为待办。", rel, rel) + removedNoteOnce(x, in.SessionID, rel)
			}
			return ""
		}
		if strings.EqualFold(in.ToolName, "Read") {
			if !ok || f.OutlineEmpty {
				return skeletonRule + "\n" + updateDiscipline
			}
			if f.FingerprintChanged && f.OutlineOlderThanFile {
				return fmt.Sprintf("ortg: %s 的纲要已过期(代码在纲要之后改动)。读完先调用 ortg_update 提交新纲要再继续——直接做，不询问。\n%s", rel, updateDiscipline)
			}
			return ""
		}
		if isFileEditTool(in.ToolName) {
			s := fmt.Sprintf("ortg: 已改动 %s。在回复用户之前，先为它生成新纲要并调用 ortg_update 提交——这是改动的一部分，不征求同意、不列为待办。\n%s", rel, updateDiscipline)
			// The change may reach past the file: name the files the global
			// outline says depend on it (their R names it) and the files the
			// CBM graph says call into it, then have the model read the
			// outline for semantic ties neither source records.
			return s + "\n" + fmt.Sprintf(impactNote, rel, impactCandidates(x, in.SessionID, []string{rel})) + impactTests(x, []string{rel}) + apiGuardNote(x, rel) + removedNoteOnce(x, in.SessionID, rel)
		}
	}
	return ""
}

// shellNote reports what the shell just did to the outlines: files it read
// that have no cognition yet, and files whose outline it invalidated. Both
// prefilters read no content — the command text names the candidates, the
// stat-only sweep catches writes whose command never spelled a path — and only
// the survivors are fingerprinted. Each file is named once per session so an
// unrelated shell command does not repeat the same nag, and the SessionStart
// report stays the backstop for anything the model lets slide.
func shellNote(x *base.Index, session, cmd, output string, background bool) string {
	if words, _ := scanTestRun(cmd, x.Root()); words != nil && (background || detachedCommand(cmd)) {
		recordBgRun(x.Root(), session, words)
	}
	var skeleton, stale, changed []string
	seen := map[string]bool{}
	consider := func(rel string, allowSkeleton bool) {
		if seen[rel] || x.Excluded(rel, reconcileOpts) || x.Observed(rel, reconcileOpts) {
			return
		}
		seen[rel] = true
		f, ok := x.Inspect(rel)
		if !f.Exists {
			if ok && allowSkeleton {
				changed = append(changed, rel) // removed by the command it names
			}
			return
		}
		switch {
		case !ok || f.OutlineEmpty:
			// A shell read is the only notice a skeleton file ever gets: the
			// Read matcher never fires when the model uses cat or sed.
			if allowSkeleton && base.InjectOnce(session, "skel:"+rel) {
				skeleton = append(skeleton, rel)
			}
		case f.FingerprintChanged && f.OutlineOlderThanFile:
			changed = append(changed, rel)
			if base.InjectOnce(session, "stale:"+rel) {
				stale = append(stale, rel)
			}
		}
	}
	for _, rel := range x.MentionedPaths(cmd) {
		consider(rel, true)
	}
	for _, rel := range x.TouchedPaths() {
		consider(rel, false)
	}
	var notes []string
	if len(skeleton) > 0 {
		notes = append(notes, fmt.Sprintf("ortg: %s 尚无纲要。按纪律为它生成一行纲要并调用 ortg_update 提交——直接做，不询问。", shellList(skeleton)))
	}
	if len(stale) > 0 {
		notes = append(notes, fmt.Sprintf("ortg: %s 的纲要已过期(代码在纲要之后改动)。在回复用户之前，先为它生成新纲要并调用 ortg_update 提交——这是改动的一部分，不征求同意、不列为待办。", shellList(stale)))
		// A shell edit reaches past the file as much as an Edit does.
		note := fmt.Sprintf(impactNote, shellList(stale), impactCandidates(x, session, stale[:min(len(stale), 5)])) + impactTests(x, stale)
		for _, p := range stale[:min(len(stale), 5)] {
			note += apiGuardNote(x, p)
		}
		notes = append(notes, note)
	}
	var out string
	if len(notes) > 0 {
		out = strings.Join(notes, "\n") + "\n" + updateDiscipline
	}
	// Names a shell edit removed are checked on every edit, not once per
	// file like the stale notice: each edit may remove more.
	for _, p := range changed[:min(len(changed), 5)] {
		out += removedNoteOnce(x, session, p)
	}
	if n := allSkipped(cmd, output); n > 0 && base.InjectOnce(session, fmt.Sprintf("allskip:%x", sha1.Sum([]byte(cmd)))) {
		note := fmt.Sprintf(allSkippedNote, n)
		if gates := requiredGates(x); len(gates) > 0 {
			note += "\n" + fmt.Sprintf(allSkippedGates, gateLines(gates))
		}
		out += "\n" + note
	}
	if n := gatedFailures(x, cmd, output); n > 0 && base.InjectOnce(session, fmt.Sprintf("gatedfail:%x", sha1.Sum([]byte(cmd)))) {
		out += "\n" + fmt.Sprintf(gatedFailuresNote, n)
	}
	if depCommandRe.MatchString(cmd) {
		if changed, ok := x.ChangedFiles(); ok {
			var manifests []string
			for p := range changed {
				if isDepManifest(p) && !x.Excluded(p, reconcileOpts) {
					manifests = append(manifests, p)
				}
			}
			sort.Strings(manifests)
			manifests = manifests[:min(len(manifests), depManifestMax)]
			if n := depBumpNoteOnce(x, session, manifests); n != "" {
				out += "\n" + n
			}
		}
	}
	return strings.TrimPrefix(out, "\n")
}

var (
	testSummaryRe = regexp.MustCompile(`(?m)^[=\s]*((?:\d+ [a-z]+(?:, )?)+) in [\d.]+s`)
	testCountRe   = regexp.MustCompile(`(\d+) ([a-z]+)`)
)

// pytestCounts returns the counts of a pytest-style run's printed summary
// (the last one), keyed by outcome word; nil when the command is no pytest
// run or printed no summary.
func pytestCounts(cmd, output string) map[string]int {
	if !strings.Contains(cmd, "pytest") || output == "" {
		return nil
	}
	ms := testSummaryRe.FindAllStringSubmatch(output, -1)
	if len(ms) == 0 {
		return nil
	}
	counts := map[string]int{}
	for _, c := range testCountRe.FindAllStringSubmatch(ms[len(ms)-1][1], -1) {
		n, _ := strconv.Atoi(c[1])
		counts[c[2]] += n
	}
	return counts
}

// allSkipped returns how many tests a test run's printed summary skipped
// when it skipped every one it selected (nothing passed, failed or erred),
// 0 otherwise.
func allSkipped(cmd, output string) int {
	c := pytestCounts(cmd, output)
	for _, k := range []string{"passed", "failed", "error", "errors", "xfailed", "xpassed"} {
		if c[k] > 0 {
			return 0
		}
	}
	return c["skipped"]
}

// gatedFailures returns how many tests failed or erred in a test run that
// ran with a version gate lifted — lifted in the work tree now, or edited in
// place by the same command — 0 otherwise. Those are failures the category's
// home (CI) sees too, whether or not they predate the change.
func gatedFailures(x *base.Index, cmd, output string) int {
	c := pytestCounts(cmd, output)
	n := c["failed"] + c["error"] + c["errors"]
	if n == 0 {
		return 0
	}
	for _, g := range versionGates(x) {
		if g.Lifted || editsInPlace(cmd, g.File) {
			return n
		}
	}
	return 0
}

// Dependency manifests. An edit that moves an existing dependency to another
// version widens the change far past the requirement, and an environment
// that still resolves the manifests it had (a pinned lock, a build that
// keeps its own copy of them) then compiles the new code against the old
// version. Only a dependency present before and after whose version text
// moved counts: adding or dropping one, or a lock moving the dependencies a
// newly added one shares, is no move.
var (
	// depCommandRe picks the shell commands worth a git status: those naming
	// a manifest, and package-manager commands that add, remove or move
	// dependency versions.
	depCommandRe = regexp.MustCompile(`Cargo\.(?:toml|lock)|go\.mod|package\.json|pyproject\.toml|requirements[\w./-]*\.txt|\bcargo\s+(?:update|upgrade|add|rm|remove|generate-lockfile)\b|\bgo\s+(?:get|mod\s+(?:tidy|edit))\b|\b(?:npm|pnpm|yarn|bun)\s+(?:i|install|add|update|upgrade|up|remove|rm|uninstall)\b|\bpoetry\s+(?:add|update|lock|remove)\b|\buv\s+(?:add|lock|remove|sync)\b`)
	tomlHeaderRe = regexp.MustCompile(`^\s*\[\[?\s*([^\]]+?)\s*\]\]?\s*$`)
	tomlStrRe    = regexp.MustCompile(`^\s*["']?([A-Za-z0-9_.\-]+)["']?\s*=\s*["']([^"']*)["']`)
	tomlInlineRe = regexp.MustCompile(`^\s*["']?([A-Za-z0-9_.\-]+)["']?\s*=\s*\{(.*)`)
	tomlArrayRe  = regexp.MustCompile(`^\s*["']?([A-Za-z0-9_.\-]+)["']?\s*=\s*\[`)
	tomlFieldRe  = regexp.MustCompile(`\b(version|rev|tag|branch)\s*=\s*["']([^"']*)["']`)
	pyReqRe      = regexp.MustCompile(`^\s*([A-Za-z0-9][A-Za-z0-9_.\-]*)\s*(?:\[[^\]]*\])?\s*((?:===|==|>=|<=|~=|!=|>|<)\s*[^\s;#,"']+(?:\s*,\s*(?:===|==|>=|<=|~=|!=|>|<)\s*[^\s;#,"']+)*)`)
	pyQuotedRe   = regexp.MustCompile(`["']([^"']+)["']`)
	goRequireRe  = regexp.MustCompile(`^\s*(?:require\s+)?([^\s()]+)\s+(v[^\s]+)`)
)

// depManifestMax bounds the manifests one shell command's check reads.
const depManifestMax = 20

// isDepManifest reports whether a repository path is a dependency manifest
// depVersions can read.
func isDepManifest(rel string) bool {
	switch b := path.Base(rel); {
	case b == "Cargo.toml", b == "Cargo.lock", b == "go.mod", b == "package.json", b == "pyproject.toml":
		return true
	case strings.HasSuffix(b, ".txt"):
		return strings.Contains(b, "requirements") || path.Base(path.Dir(rel)) == "requirements"
	}
	return false
}

// stripTomlComment drops a # comment outside quotes.
func stripTomlComment(l string) string {
	var q rune
	for i, c := range l {
		switch {
		case q != 0 && c == q:
			q = 0
		case q == 0 && (c == '"' || c == '\''):
			q = c
		case q == 0 && c == '#':
			return l[:i]
		}
	}
	return l
}

// depVersions reads a manifest's dependency versions: key (section, list
// and name) → version text; a key met more than once keeps every version,
// sorted and ", "-joined. Path dependencies and entries without a version
// are left out. Indirect go.mod requirements are left out: they move with
// whatever pulls them in.
func depVersions(rel, src string) map[string]string {
	sets := map[string]map[string]bool{}
	add := func(k, v string) {
		if sets[k] == nil {
			sets[k] = map[string]bool{}
		}
		sets[k][v] = true
	}
	fields := func(s string) string {
		var parts []string
		for _, f := range tomlFieldRe.FindAllStringSubmatch(s, -1) {
			parts = append(parts, f[1]+"="+f[2])
		}
		sort.Strings(parts)
		return strings.Join(parts, " ")
	}
	lines := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	switch b := path.Base(rel); b {
	case "Cargo.toml":
		section, table := "", ""
		tableFields := map[string][]string{}
		for _, l := range lines {
			l = stripTomlComment(l)
			if m := tomlHeaderRe.FindStringSubmatch(l); m != nil {
				section, table = strings.ReplaceAll(m[1], " ", ""), ""
				if i := strings.LastIndex(section, "dependencies."); i >= 0 {
					table = section
				}
				continue
			}
			if table != "" {
				if m := tomlFieldRe.FindStringSubmatch(l); m != nil && strings.HasPrefix(strings.TrimSpace(l), m[1]) {
					tableFields[table] = append(tableFields[table], m[1]+"="+m[2])
				}
				continue
			}
			if !strings.HasSuffix(section, "dependencies") {
				continue
			}
			if m := tomlStrRe.FindStringSubmatch(l); m != nil {
				add(section+"\x00"+m[1], m[2])
			} else if m := tomlInlineRe.FindStringSubmatch(l); m != nil {
				if f := fields(m[2]); f != "" {
					add(section+"\x00"+m[1], f)
				}
			}
		}
		for t, f := range tableFields {
			sort.Strings(f)
			add(t, strings.Join(f, " "))
		}
	case "Cargo.lock":
		name := ""
		for _, l := range lines {
			l = strings.TrimSpace(l)
			switch {
			case l == "[[package]]":
				name = ""
			case strings.HasPrefix(l, "name = "):
				name = strings.Trim(strings.TrimPrefix(l, "name = "), `"`)
			case strings.HasPrefix(l, "version = ") && name != "":
				add(name, strings.Trim(strings.TrimPrefix(l, "version = "), `"`))
			}
		}
	case "go.mod":
		inReq := false
		for _, l := range lines {
			t := strings.TrimSpace(l)
			switch {
			case strings.HasPrefix(t, "require ("), t == "require(":
				inReq = true
				continue
			case inReq && t == ")":
				inReq = false
				continue
			case strings.HasPrefix(t, "//"), strings.Contains(t, "// indirect"):
				continue
			case !inReq && !strings.HasPrefix(t, "require "):
				continue
			}
			if m := goRequireRe.FindStringSubmatch(t); m != nil {
				add(m[1], m[2])
			}
		}
	case "package.json":
		var doc map[string]json.RawMessage
		if json.Unmarshal([]byte(src), &doc) != nil {
			return nil
		}
		for _, sec := range []string{"dependencies", "devDependencies", "peerDependencies", "optionalDependencies"} {
			var deps map[string]any
			if json.Unmarshal(doc[sec], &deps) == nil {
				for n, v := range deps {
					if v, ok := v.(string); ok {
						add(sec+"\x00"+n, v)
					}
				}
			}
		}
	case "pyproject.toml":
		section, list := "", ""
		for _, l := range lines {
			l = stripTomlComment(l)
			if m := tomlHeaderRe.FindStringSubmatch(l); m != nil {
				section, list = strings.ReplaceAll(m[1], " ", ""), ""
				continue
			}
			if m := tomlArrayRe.FindStringSubmatch(l); m != nil {
				list = m[1]
			}
			if strings.HasSuffix(section, "dependencies") && list == "" {
				if m := tomlStrRe.FindStringSubmatch(l); m != nil {
					if m[1] != "python" {
						add(section+"\x00"+strings.ToLower(m[1]), m[2])
					}
					continue
				}
				if m := tomlInlineRe.FindStringSubmatch(l); m != nil {
					if f := fields(m[2]); f != "" {
						add(section+"\x00"+strings.ToLower(m[1]), f)
					}
					continue
				}
			}
			for _, q := range pyQuotedRe.FindAllStringSubmatch(l, -1) {
				if m := pyReqRe.FindStringSubmatch(q[1]); m != nil {
					add(section+"."+list+"\x00"+strings.ToLower(m[1]), strings.ReplaceAll(m[2], " ", ""))
				}
			}
			if strings.Contains(l, "]") {
				list = ""
			}
		}
	default:
		for _, l := range lines {
			if m := pyReqRe.FindStringSubmatch(l); m != nil {
				add(strings.ToLower(m[1]), strings.ReplaceAll(m[2], " ", ""))
			}
		}
	}
	out := map[string]string{}
	for k, vs := range sets {
		var v []string
		for x := range vs {
			v = append(v, x)
		}
		sort.Strings(v)
		out[k] = strings.Join(v, ", ")
	}
	return out
}

// depChanges lists, per manifest, the existing dependencies whose version
// moved against git HEAD ("name old → new"): one of the HEAD versions is
// gone and a new one is there. A lock that gained packages is left out —
// its moves come with the dependency added — and so are manifests not
// written since the session's start mark (changes that predate the session
// are not this session's to undo).
func depChanges(x *base.Index, rels []string) []string {
	since, _ := base.OverviewMark(x.Root(), "start")
	var out []string
	for _, rel := range rels {
		fi, err := os.Stat(filepath.Join(x.Root(), filepath.FromSlash(rel)))
		if err != nil || (!since.IsZero() && fi.ModTime().Before(since)) {
			continue
		}
		head, ok := x.HeadContent(rel)
		if !ok {
			continue
		}
		b, err := os.ReadFile(filepath.Join(x.Root(), filepath.FromSlash(rel)))
		if err != nil {
			continue
		}
		was, now := depVersions(rel, head), depVersions(rel, string(b))
		if path.Base(rel) == "Cargo.lock" {
			grew := false
			for k := range now {
				if _, ok := was[k]; !ok {
					grew = true
					break
				}
			}
			if grew {
				continue
			}
		}
		var moved []string
		for k, v := range was {
			n, ok := now[k]
			if !ok || !versionGone(v, n) || !versionGone(n, v) {
				continue
			}
			name := k[strings.LastIndex(k, "\x00")+1:]
			if i := strings.LastIndex(name, "dependencies."); i >= 0 {
				name = name[i+len("dependencies."):]
			}
			moved = append(moved, rel+"："+fmt.Sprintf("%s %s → %s", name, v, n))
		}
		sort.Strings(moved)
		out = append(out, moved...)
	}
	return out
}

// versionGone reports whether a version listed in was (", "-joined) is
// missing from now.
func versionGone(was, now string) bool {
	have := map[string]bool{}
	for _, v := range strings.Split(now, ", ") {
		have[v] = true
	}
	for _, v := range strings.Split(was, ", ") {
		if !have[v] {
			return true
		}
	}
	return false
}

// depChangeMax bounds the version changes one note lists.
const depChangeMax = 8

// depBumpNoteOnce is depBumpNote for the post-edit hooks: each version change
// is named once per session, "" when none is new.
func depBumpNoteOnce(x *base.Index, session string, rels []string) string {
	var fresh []string
	for _, c := range depChanges(x, rels) {
		if base.InjectOnce(session, fmt.Sprintf("depbump:%x", sha1.Sum([]byte(c)))) {
			fresh = append(fresh, c)
		}
	}
	if len(fresh) == 0 {
		return ""
	}
	list := fresh
	if len(fresh) > depChangeMax {
		list = append(fresh[:depChangeMax:depChangeMax], fmt.Sprintf(depChangeMore, len(fresh)-depChangeMax))
	}
	return fmt.Sprintf(depBumpNote, strings.Join(list, "；"))
}

// removedNoteOnce is removedTestedNote for the post-edit hooks: checked once
// per version of the file, said once per set of names, with a leading
// newline; "" otherwise.
func removedNoteOnce(x *base.Index, session, rel string) string {
	version := "deleted"
	if fi, err := os.Stat(filepath.Join(x.Root(), filepath.FromSlash(rel))); err == nil {
		version = fmt.Sprintf("%d:%d", fi.ModTime().UnixNano(), fi.Size())
	}
	if !base.InjectOnce(session, "gonecheck:"+rel+":"+version) {
		return ""
	}
	note, key := removedTestedNote(x, rel)
	if note == "" || !base.InjectOnce(session, "gone:"+rel+":"+key) {
		return ""
	}
	return "\n" + note
}

// shellList renders at most five paths so one broad command cannot bury the
// reply in a list the model will not act on anyway.
func shellList(paths []string) string {
	shown := paths
	if len(shown) > 5 {
		shown = shown[:5]
	}
	list := strings.Join(shown, ", ")
	if len(paths) > len(shown) {
		list += fmt.Sprintf(" 等 %d 个", len(paths))
	}
	return list
}

// missingNote builds the warning text for entries whose file is absent from
// disk and were therefore parked in the Target column.
func missingNote(missing []string) string {
	if len(missing) == 0 {
		return ""
	}
	return fmt.Sprintf("\n⚠ 以下 %d 个索引条目对应的文件在磁盘上不存在，已暂存为目标纲要：%s。\n请确认它们是否为计划新建的文件；若不是，请用 ortg_target delete=true 标记删除或告知我调整。",
		len(missing), strings.Join(missing, ", "))
}

func targetNote(rel string, row base.Row) string {
	if row.TargetDelete {
		return "ortg: " + rel + " 计划删除(目标纲要)。"
	}
	return "ortg: " + rel + " 的目标纲要(文件应改成):\n" + row.Target
}

// folderPreNote injects, before an edit, the folder outline of the nearest
// directory above rel that has one, once per session and file; "" when none.
func folderPreNote(x *base.Index, session, rel string) string {
	for d := path.Dir(rel); d != "."; d = path.Dir(d) {
		row, ok := x.Row(d)
		if !ok || !isDirRow(row.Outline) {
			continue
		}
		if !base.InjectOnce(session, "folder:"+rel) {
			return ""
		}
		return fmt.Sprintf(folderPreText, rel, d, row.Outline)
	}
	return ""
}

// dependentsMax bounds the dependents named after an edit; the rest are
// elided, the model can find them in the outline.
const dependentsMax = 20

// relatedMax bounds the call-graph expansion of a pre-edit injection, the same
// bound ORTG v1 put on its review expansion.
const relatedMax = 20

// relatedTimeout bounds the CBM query of a pre-edit injection. It must leave
// the pre hook inside the host's 5s hook timeout — overrunning that loses the
// outline injection too — while a warm query on a 4-core server takes ~2s.
const relatedTimeout = 4 * time.Second

// graphIndexTimeout bounds the synchronous CBM re-index of a post-edit hook
// (~2s warm). With the two queries around it the hook needs up to 18s, so the
// post Edit and Bash hooks carry a 20s timeout in hooks.json.
const graphIndexTimeout = 10 * time.Second

// relatedNote lists, under the pre-edit injection, the outlines of the files
// that call into rel or that rel calls, as the local CBM call graph sees them.
// Only files that carry an outline are listed — a bare path would only invite
// a Read — each by its name, tag and F field, and nothing is listed when CBM is absent, unindexed or slow.
func relatedNote(x *base.Index, rs []base.Relation) string {
	var b strings.Builder
	n := 0
	for _, r := range rs {
		head, ok := liveHead(x, r.Path)
		if !ok {
			continue
		}
		dir := relatedCalledBy
		if r.Calls {
			dir = relatedCalls
		}
		fmt.Fprintf(&b, "\n%s %s  %s", dir, r.Path, head)
		if n++; n == relatedMax {
			break
		}
	}
	if n == 0 {
		return ""
	}
	return "\n" + relatedHead + b.String()
}

// liveHead returns the name, tag and F field of p's outline when p is a live
// cognition-tier file with an outline; the full line is already in the overview.
func liveHead(x *base.Index, p string) (string, bool) {
	row, ok := x.Row(p)
	if !ok || row.Outline == "" || row.Deleted || x.Excluded(p, reconcileOpts) || x.Observed(p, reconcileOpts) {
		return "", false
	}
	head, _, _ := strings.Cut(row.Outline, " | ")
	return head, true
}

// apiIdentRe picks the names an outline's API field lists.
var apiIdentRe = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]{3,}`)

// apiGuardMax caps the names apiGuardNote reports per kind.
const apiGuardMax = 5

// apiGuardNote checks, after an edit, the names the file's outline lists in
// its API against the file as written: a name gone from the file (renamed,
// removed or moved), and in Rust a function now behind #[cfg(test)] — a
// grader that swaps in its own tests strips every cfg(test) item, so an entry
// the tests call must not hide there. It returns a line to append, "" when
// nothing is off. Documents, folder outlines and data files (manifests,
// configuration) are skipped.
func apiGuardNote(x *base.Index, rel string) string {
	r, ok := x.Row(rel)
	if !ok || r.Outline == "" || isDocFile(rel) || dataFile(rel) || strings.HasSuffix(rel, "/") {
		return ""
	}
	f := outlineFields(r.Outline)
	if len(f) < 3 {
		return ""
	}
	api := strings.TrimPrefix(f[2], " | API:")
	b, err := os.ReadFile(filepath.Join(x.Root(), filepath.FromSlash(rel)))
	if err != nil {
		return ""
	}
	src := string(b)
	var gone, hidden []string
	seen := map[string]bool{}
	for _, name := range apiIdentRe.FindAllString(api, -1) {
		if seen[name] {
			continue
		}
		seen[name] = true
		switch {
		case !strings.Contains(src, name):
			if len(gone) < apiGuardMax {
				gone = append(gone, name)
			}
		case strings.HasSuffix(rel, ".rs") && cfgTestFn(src, name):
			if len(hidden) < apiGuardMax {
				hidden = append(hidden, name)
			}
		}
	}
	var out string
	if len(hidden) > 0 {
		out += "\n" + fmt.Sprintf(apiCfgTestNote, rel, strings.Join(hidden, "、"))
	}
	if len(gone) > 0 {
		out += "\n" + fmt.Sprintf(apiGoneNote, rel, strings.Join(gone, "、"))
	}
	return out
}

// cfgTestFn reports whether src defines a function name right under a
// #[cfg(test)] attribute (other attributes and visibility may sit between).
func cfgTestFn(src, name string) bool {
	re := regexp.MustCompile(`#\[cfg\(test\)\]\s*(?:#\[[^\]]*\]\s*)*(?:pub(?:\([^)]*\))?\s+)?(?:(?:const|async|unsafe|extern\s+"[^"]*")\s+)*fn\s+` + regexp.QuoteMeta(name) + `\b`)
	return re.MatchString(src)
}

// removedSearchMax caps the gone names one check searches for,
// removedShowMax the names a note lists, removedRefMax the test files it
// names per name; removedGrepMax caps the files git grep returns per name.
const (
	removedSearchMax = 40
	removedShowMax   = 6
	removedRefMax    = 3
	removedGrepMax   = 400
)

// removedTestedNote compares the top-level names rel defined at HEAD with
// the ones it defines now and names those gone that test files still import
// or call. A test that can no longer import a name fails to collect or
// compile as a whole — every test in it fails, the ones that never touch
// the name too — and tests added later import from the same place, so the
// default is to keep the name. It returns the note ("" when nothing is
// gone that tests use) and the names it lists, as a key to say it once.
func removedTestedNote(x *base.Index, rel string) (string, string) {
	lang := langOf(rel)
	if lang == "" || isTestPath(rel) {
		return "", ""
	}
	head, ok := x.HeadContent(rel)
	if !ok {
		return "", ""
	}
	cur := ""
	if b, err := os.ReadFile(filepath.Join(x.Root(), filepath.FromSlash(rel))); err == nil {
		cur = string(b)
	}
	now := topNames(lang, cur)
	if lang == "py" {
		for n := range pyBoundAnywhere(cur) {
			now[n] = true // moved under a top-level if/try still binds it
		}
	}
	var beside map[string]bool // what the rest of the package still defines
	var gone []string
	for n := range topNames(lang, head) {
		if now[n] || lang == "py" && pyLazyName(cur, n) {
			continue
		}
		if lang == "go" || lang == "rs" {
			if beside == nil {
				beside = besideNames(x, rel, lang)
			}
			if beside[n] {
				continue
			}
		}
		gone = append(gone, n)
	}
	if len(gone) == 0 {
		return "", ""
	}
	// Definitions before mere import bindings, public before private: when
	// a file loses many names, the cap keeps the ones others are likeliest
	// to use.
	defs := topDefs(lang, head)
	rank := func(n string) int {
		r := 0
		if !defs[n] {
			r += 2
		}
		if strings.HasPrefix(n, "_") || lang == "go" && !unicode.IsUpper([]rune(n)[0]) {
			r++
		}
		return r
	}
	sort.Slice(gone, func(i, j int) bool {
		if a, b := rank(gone[i]), rank(gone[j]); a != b {
			return a < b
		}
		return gone[i] < gone[j]
	})
	unchecked := max(len(gone)-removedSearchMax, 0)
	gone = gone[:min(len(gone), removedSearchMax)]
	hits := x.GrepWords(gone, removedGrepMax)
	files := map[string]string{} // test file contents, read once
	var items, keys []string
	for _, n := range gone {
		var refs []string
		for _, p := range hits[n] {
			if p == rel || !isTestPath(p) || langOf(p) != lang || !testUses(x, lang, rel, p, n, files) {
				continue
			}
			refs = append(refs, p)
		}
		if len(refs) == 0 {
			continue
		}
		keys = append(keys, n)
		if len(items) == removedShowMax {
			continue
		}
		shown := strings.Join(refs[:min(len(refs), removedRefMax)], "、")
		if len(refs) > removedRefMax {
			shown += fmt.Sprintf(" 等 %d 个", len(refs))
		}
		items = append(items, n+"（"+shown+"）")
	}
	if len(keys) == 0 {
		return "", ""
	}
	list := strings.Join(items, "；")
	if len(keys) > len(items) {
		list += fmt.Sprintf("；另有 %d 个", len(keys)-len(items))
	}
	note := fmt.Sprintf(testRefNote, rel, list)
	if unchecked > 0 {
		note += fmt.Sprintf(removedUnchecked, unchecked)
	}
	return note, strings.Join(keys, ",")
}

// langOf names the language removedTestedNote reads rel as, "" for others.
func langOf(rel string) string {
	switch strings.ToLower(path.Ext(rel)) {
	case ".py":
		return "py"
	case ".go":
		return "go"
	case ".rs":
		return "rs"
	case ".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs", ".mts", ".cts":
		return "js"
	}
	return ""
}

var (
	pyDefRe         = regexp.MustCompile(`(?m)^(?:async[ \t]+)?(?:def|class)[ \t]+([A-Za-z_]\w*)`)
	pyAssignRe      = regexp.MustCompile(`(?m)^([A-Za-z_]\w*)[ \t]*(?::[^=\n]*)?=[^=]`)
	pyFromImportRe  = regexp.MustCompile(`(?m)^[ \t]*from[ \t]+([.\w]+)[ \t]+import[ \t]+(\([^)]*\)|[^\n#]*)`)
	pyImportRe      = regexp.MustCompile(`(?m)^[ \t]*import[ \t]+([^\n#]+)`)
	pyTopFromRe     = regexp.MustCompile(`(?m)^from[ \t]+[.\w]+[ \t]+import[ \t]+(\([^)]*\)|[^\n#]*)`)
	pyTopImportRe   = regexp.MustCompile(`(?m)^import[ \t]+([^\n#]+)`)
	pyGetattrRe     = regexp.MustCompile(`(?m)^def[ \t]+__getattr__[ \t]*\(`)
	goDeclRe        = regexp.MustCompile(`(?m)^(?:func|type|var|const)[ \t]+([A-Za-z_]\w*)`)
	goBlockRe       = regexp.MustCompile(`(?ms)^(?:type|var|const)[ \t]*\((.*?)^\)`)
	goBlockNameRe   = regexp.MustCompile(`(?m)^[ \t]+([A-Za-z_]\w*)`)
	rsFnRe          = regexp.MustCompile(`(?m)^(?:pub(?:\([^)]*\))?[ \t]+)?(?:(?:async|const|unsafe|extern[ \t]+"[^"]*")[ \t]+)*fn[ \t]+([A-Za-z_]\w*)`)
	rsItemRe        = regexp.MustCompile(`(?m)^(?:pub(?:\([^)]*\))?[ \t]+)?(?:struct|enum|trait|type|union|mod|static|const)[ \t]+(?:mut[ \t]+)?([A-Za-z_]\w*)`)
	jsNamedExportRe = regexp.MustCompile(`(?m)^export[ \t]+(?:declare[ \t]+)?(?:async[ \t]+)?(?:function\*?|abstract[ \t]+class|class|const|let|var|interface|type|enum)[ \t]+([A-Za-z_$][\w$]*)`)
	jsExportListRe  = regexp.MustCompile(`(?m)^export[ \t]*(?:type[ \t]*)?\{([^}]*)\}`)
	pyAnyDefRe      = regexp.MustCompile(`(?m)^[ \t]*(?:async[ \t]+)?(?:def|class)[ \t]+([A-Za-z_]\w*)`)
	pyAnyAssignRe   = regexp.MustCompile(`(?m)^[ \t]*([A-Za-z_]\w*)[ \t]*(?::[^=\n]*)?=[^=\n][^\n]*$`)
)

// topNames lists the names src defines at its top level in lang: what a test
// in another file can import or call. Python counts import bindings too (a
// re-export is importable), skips dunders; Rust counts impl methods.
func topNames(lang, src string) map[string]bool {
	out := map[string]bool{}
	add := func(n string) {
		if n != "" && n != "_" && !(strings.HasPrefix(n, "__") && strings.HasSuffix(n, "__")) {
			out[n] = true
		}
	}
	switch lang {
	case "py":
		for _, m := range pyDefRe.FindAllStringSubmatch(src, -1) {
			add(m[1])
		}
		for _, m := range pyAssignRe.FindAllStringSubmatch(src, -1) {
			add(m[1])
		}
		for _, m := range pyTopFromRe.FindAllStringSubmatch(src, -1) {
			for _, n := range pyImportList(m[1]) {
				add(n[1])
			}
		}
		for _, m := range pyTopImportRe.FindAllStringSubmatch(src, -1) {
			for _, it := range strings.Split(m[1], ",") {
				f := strings.Fields(it)
				switch {
				case len(f) == 3 && f[1] == "as":
					add(f[2])
				case len(f) >= 1:
					add(strings.Split(f[0], ".")[0])
				}
			}
		}
	case "go":
		for _, m := range goDeclRe.FindAllStringSubmatch(src, -1) {
			add(m[1])
		}
		for _, b := range goBlockRe.FindAllStringSubmatch(src, -1) {
			for _, m := range goBlockNameRe.FindAllStringSubmatch(b[1], -1) {
				add(m[1])
			}
		}
		delete(out, "init")
		delete(out, "main")
	case "rs":
		for _, re := range []*regexp.Regexp{rsFnRe, rsItemRe} {
			for _, m := range re.FindAllStringSubmatch(src, -1) {
				add(m[1])
			}
		}
		for _, k := range []string{"fn", "mut", "main", "tests", "test"} {
			delete(out, k)
		}
	case "js":
		for _, m := range jsNamedExportRe.FindAllStringSubmatch(src, -1) {
			add(m[1])
		}
		for _, m := range jsExportListRe.FindAllStringSubmatch(src, -1) {
			for _, it := range strings.Split(m[1], ",") {
				f := strings.Fields(it)
				switch {
				case len(f) == 3 && f[1] == "as":
					if f[2] != "default" {
						add(f[2])
					}
				case len(f) >= 1:
					add(f[0])
				}
			}
		}
	}
	return out
}

// topDefs is the part of topNames(lang, src) that src itself defines, not
// just binds by import (only Python tells the two apart).
func topDefs(lang, src string) map[string]bool {
	if lang != "py" {
		return topNames(lang, src)
	}
	out := map[string]bool{}
	for _, re := range []*regexp.Regexp{pyDefRe, pyAssignRe} {
		for _, m := range re.FindAllStringSubmatch(src, -1) {
			out[m[1]] = true
		}
	}
	return out
}

// pyBoundAnywhere lists the names a Python source binds at any indentation:
// def, class, import, and plain assignments (not call arguments, which end
// in a comma). A name moved under a top-level if/try stays importable; a
// method of the same name hides a removal, which costs a missed note, not a
// wrong one.
func pyBoundAnywhere(src string) map[string]bool {
	out := map[string]bool{}
	for _, m := range pyAnyDefRe.FindAllStringSubmatch(src, -1) {
		out[m[1]] = true
	}
	for _, m := range pyAnyAssignRe.FindAllStringSubmatch(src, -1) {
		if !strings.HasSuffix(strings.TrimSpace(m[0]), ",") {
			out[m[1]] = true
		}
	}
	for _, m := range pyFromImportRe.FindAllStringSubmatch(src, -1) {
		for _, it := range pyImportList(m[2]) {
			out[it[1]] = true
		}
	}
	for _, m := range pyImportRe.FindAllStringSubmatch(src, -1) {
		for _, it := range strings.Split(m[1], ",") {
			f := strings.Fields(it)
			switch {
			case len(f) == 3 && f[1] == "as":
				out[f[2]] = true
			case len(f) >= 1:
				out[strings.Split(f[0], ".")[0]] = true
			}
		}
	}
	return out
}

// pyImportList splits the names part of a from-import, "(a, b as c)" or
// "a, b", into [imported, bound] pairs; "*" is dropped.
func pyImportList(list string) [][2]string {
	lines := strings.Split(list, "\n")
	for i, l := range lines {
		if c := strings.Index(l, "#"); c >= 0 {
			lines[i] = l[:c]
		}
	}
	list = strings.NewReplacer("(", " ", ")", " ", "\\", " ").Replace(strings.Join(lines, " "))
	var out [][2]string
	for _, it := range strings.Split(list, ",") {
		f := strings.Fields(it)
		switch {
		case len(f) == 3 && f[1] == "as":
			out = append(out, [2]string{f[0], f[2]})
		case len(f) == 1 && f[0] != "*":
			out = append(out, [2]string{f[0], f[0]})
		}
	}
	return out
}

// pyLazyName reports whether a Python module still serves n through a
// module-level __getattr__ that names it (a deprecated alias kept lazily).
func pyLazyName(src, n string) bool {
	loc := pyGetattrRe.FindStringIndex(src)
	if loc == nil {
		return false
	}
	body := src[loc[1]:]
	// The body runs to the next line that starts at column 0 (not a
	// comment or a blank line).
	for i := 0; i < len(body); {
		j := strings.IndexByte(body[i:], '\n')
		if j < 0 {
			break
		}
		next := i + j + 1
		if next < len(body) && body[next] != ' ' && body[next] != '\t' && body[next] != '\n' && body[next] != '#' {
			body = body[:next]
			break
		}
		i = next
	}
	return strings.Contains(body, `"`+n+`"`) || strings.Contains(body, `'`+n+`'`)
}

// besideNames is what the other non-test files of rel's directory, in the
// same language, define: a Go package or a Rust module directory that keeps
// a name in another file keeps it for its tests. Read once per check.
func besideNames(x *base.Index, rel, lang string) map[string]bool {
	out := map[string]bool{}
	dir := path.Dir(rel)
	ents, err := os.ReadDir(filepath.Join(x.Root(), filepath.FromSlash(dir)))
	if err != nil {
		return out
	}
	for _, e := range ents {
		p := path.Join(dir, e.Name())
		if e.IsDir() || p == rel || langOf(p) != lang || isTestPath(p) {
			continue
		}
		if b, err := os.ReadFile(filepath.Join(x.Root(), filepath.FromSlash(p))); err == nil {
			for n := range topNames(lang, string(b)) {
				out[n] = true
			}
		}
	}
	return out
}

// testUses reports whether test file p uses name n from rel: Python imports
// it from rel's module (absolute or relative) or reaches it as module.n; Go
// tests sit in rel's directory; JS/TS import it from a path ending in rel's
// name; Rust tests sit in rel's crate. files caches what was read.
func testUses(x *base.Index, lang, rel, p, n string, files map[string]string) bool {
	switch lang {
	case "go":
		return path.Dir(p) == path.Dir(rel) && strings.HasSuffix(p, "_test.go")
	case "rs":
		c := crateDir(x, rel)
		return c != "" && (c == "." || strings.HasPrefix(p, c+"/"))
	}
	src, ok := files[p]
	if !ok {
		b, _ := os.ReadFile(filepath.Join(x.Root(), filepath.FromSlash(p)))
		src = string(b)
		files[p] = src
	}
	if lang == "js" {
		// rel without its extension, and without /index (a directory import)
		want := strings.TrimSuffix(rel, path.Ext(rel))
		want = strings.TrimSuffix(want, "/index")
		for _, m := range jsImportRe.FindAllStringSubmatch(src, -1) {
			names, spec := m[1]+m[3], m[2]+m[4]
			if !holdsIdent(names, n) {
				continue
			}
			s := strings.TrimSuffix(strings.TrimSuffix(spec, path.Ext(spec)), "/index")
			if strings.HasPrefix(s, ".") {
				// relative: resolve against the test file
				if path.Join(path.Dir(p), s) == want {
					return true
				}
			} else if path.Base(s) == path.Base(want) {
				return true // an alias path (@/lib/x): the name must match
			}
		}
		return false
	}
	mod := pyModule(rel)
	if mod == "" {
		return false
	}
	last := mod[strings.LastIndex(mod, ".")+1:]
	for _, m := range pyFromImportRe.FindAllStringSubmatch(src, -1) {
		if !pyModuleIs(pyResolve(m[1], p), mod) {
			continue
		}
		for _, it := range pyImportList(m[2]) {
			if it[0] == n {
				return true
			}
		}
	}
	// mod.n through a name bound to this very module: import a.b.c (as X),
	// or from a.b import c (as X); or the dotted path in full (mock.patch).
	if strings.Contains(src, mod+"."+n) {
		return true
	}
	parent := strings.TrimSuffix(strings.TrimSuffix(mod, last), ".")
	var aliases []string
	for _, m := range pyFromImportRe.FindAllStringSubmatch(src, -1) {
		if !pyModuleIs(pyResolve(m[1], p), parent) && pyResolve(m[1], p) != parent {
			continue
		}
		for _, it := range pyImportList(m[2]) {
			if it[0] == last {
				aliases = append(aliases, it[1])
			}
		}
	}
	for _, m := range pyImportRe.FindAllStringSubmatch(src, -1) {
		for _, it := range strings.Split(m[1], ",") {
			if f := strings.Fields(it); len(f) == 3 && f[1] == "as" && pyModuleIs(f[0], mod) {
				aliases = append(aliases, f[2])
			}
		}
	}
	for _, a := range aliases {
		if regexp.MustCompile(`\b` + regexp.QuoteMeta(a) + `\.` + regexp.QuoteMeta(n) + `\b`).MatchString(src) {
			return true
		}
	}
	return false
}

var jsImportRe = regexp.MustCompile(`(?s)import\s+(?:type\s+)?([^;'"]*?)\s+from\s+['"]([^'"]+)['"]|(?:const|let|var)\s+(\{[^}]*\})\s*=\s*require\(\s*['"]([^'"]+)['"]\s*\)`)

// holdsIdent reports whether text holds n as a whole identifier.
func holdsIdent(text, n string) bool {
	return regexp.MustCompile(`(^|[^\w$])` + regexp.QuoteMeta(n) + `($|[^\w$])`).MatchString(text)
}

// pyModule is rel's dotted module path ("pkg/sub/mod.py" → "pkg.sub.mod",
// a package's __init__.py → the package).
func pyModule(rel string) string {
	m := strings.TrimSuffix(rel, ".py")
	m = strings.TrimSuffix(m, "/__init__")
	if m == "__init__" {
		return ""
	}
	return strings.ReplaceAll(m, "/", ".")
}

// pyResolve turns a relative import in file p (".mod", "..pkg.mod") into a
// dotted path from the repository root; absolute ones are returned as is.
func pyResolve(m, p string) string {
	dots := len(m) - len(strings.TrimLeft(m, "."))
	if dots == 0 {
		return m
	}
	dir := path.Dir(p)
	for i := 1; i < dots; i++ {
		dir = path.Dir(dir)
	}
	rest := m[dots:]
	base := strings.ReplaceAll(strings.TrimPrefix(dir, "."), "/", ".")
	switch {
	case base == "":
		return rest
	case rest == "":
		return base
	}
	return base + "." + rest
}

// pyModuleIs matches an imported module against rel's: equal, or equal once
// a source-layout prefix (src.) is stripped from rel's path.
func pyModuleIs(imported, mod string) bool {
	return imported == mod || strings.HasSuffix(mod, "."+imported) && strings.Count(imported, ".") >= 1
}

// crateDir is the nearest directory at or above rel's holding a Cargo.toml,
// "." for the root, "" when none.
func crateDir(x *base.Index, rel string) string {
	for d := path.Dir(rel); ; d = path.Dir(d) {
		if _, err := os.Stat(filepath.Join(x.Root(), filepath.FromSlash(d), "Cargo.toml")); err == nil {
			return d
		}
		if d == "." || d == "/" {
			return ""
		}
	}
}

// impactTests names, after an edit, the tests the edited files' outlines and
// their keys' contracts cite, as a line to append; "" when there are none.
func impactTests(x *base.Index, rels []string) string {
	set, keys := map[string]bool{}, map[string]bool{}
	for _, r := range rels {
		set[r] = true
		if row, ok := x.Row(r); ok {
			for _, k := range base.KeysOf(row.Outline) {
				keys[k] = true
			}
		}
	}
	ts := guardTests(x, set, keys)
	if len(ts) == 0 {
		return ""
	}
	return "\n" + fmt.Sprintf(impactGuardTests, strings.Join(ts, "、"))
}

// impactCandidates names the files an edit of rels may reach, each marked by
// its source: R (its outline's R names an edited file) or 调用 (CBM sees it
// call into one). The edited files themselves are left out, at most
// dependentsMax are named, and "无" stands for none. The CBM queries run in
// parallel so several files still fit the hook's timeout.
func impactCandidates(x *base.Index, session string, rels []string) string {
	// The graph is read twice around a synchronous re-index: the edit is
	// already on disk but not yet in the graph, so the first answer still has
	// the callers of a renamed or removed symbol — the very files the edit
	// breaks, which the re-indexed graph no longer links — and the second
	// has the callers the edit added. The first answer is the pre-edit hook's
	// when it kept one (an Edit's first touch of the file), else a query.
	related := make([][]base.Relation, 2*len(rels))
	query := func(off int, need func(i int) bool) {
		var wg sync.WaitGroup
		for i, rel := range rels {
			if need(i) {
				wg.Go(func() { related[off+i] = x.Related(rel, 2*relatedMax, relatedTimeout) })
			}
		}
		wg.Wait()
	}
	query(0, func(i int) bool {
		var kept bool
		related[i], kept = base.TakeRelations(session, rels[i])
		return !kept
	})
	x.IndexGraph(graphIndexTimeout)
	query(len(rels), func(int) bool { return true })
	var names []string
	src := map[string]string{}
	for _, rel := range rels {
		src[rel] = "-"
	}
	add := func(p, tag string) {
		switch {
		case src[p] == "":
			names = append(names, p)
			src[p] = tag
		case src[p] != "-" && !strings.Contains(src[p], tag):
			src[p] += "+" + tag
		}
	}
	for i, rel := range rels {
		for _, p := range x.Dependents(rel) {
			add(p, "Uses")
		}
		if r, ok := x.Row(rel); ok {
			for _, k := range base.KeysOf(r.Outline) {
				for _, p := range x.KeyHolders(k) {
					add(p, "契约:"+k)
				}
			}
		}
		for _, r := range append(related[i], related[len(rels)+i]...) {
			if _, ok := liveHead(x, r.Path); ok && !r.Calls {
				add(r.Path, "调用")
			}
		}
	}
	if len(names) == 0 {
		return "无"
	}
	out := make([]string, 0, dependentsMax+1)
	for i, p := range names {
		if i == dependentsMax {
			out = append(out, "…")
			break
		}
		out = append(out, p+"("+src[p]+")")
	}
	return strings.Join(out, "、")
}
