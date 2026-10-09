// Package migrate turns what older ORTG versions wrote into the current
// format, so the core reads one format only: version-1 ortg.tsv tables —
// from before the module column (ten columns), from before the target rename
// (future_* columns, 未来 terms in the header), and with the F/R/A/S outline
// fields — and the outline documents a table is first built from, a
// hand-written overview.txt or ORTG v1's split volumes. The core never imports this package; logic.go calls it at the
// entry points that may write. Dropping an old format means deleting its code
// here — the core does not change.
package migrate

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"ortg/base"
)

// v1Version heads every table an ORTG before the Role/Uses/API/Constraints
// fields wrote; the column header lines older tables carry.
const (
	v1Version     = "#ORTG-TSV: 1"
	tenColumns    = "path\tcrc32\tignored\tdeleted\tmtime\toutline\toutline_time\tfuture_outline\tfuture_time\tfuture_delete"
	futureColumns = "path\tcrc32\tignored\tdeleted\tmtime\tmodule\toutline\toutline_time\tfuture_outline\tfuture_time\tfuture_delete"
)

// headerTerms renames what older tables' headers say in retired terms: the
// 未来 wording from before the target rename, the F/R/A/S outline fields and
// the compact tag. The template's rule, format and tag lines go whole and
// must stay the lines prompt.go's headerTemplate carries (logic_test checks).
// No header line is dropped: the rules there are what a model reads when the
// hooks that inject the rules block are not running.
var headerTerms = strings.NewReplacer(
	"#未来规则：条目下一行 #未来: 是未来纲要；#未来:删除 计划删除；#计划新建 文件尚不存在",
	"#目标规则：条目下一行 #目标: 是目标纲要(改完后应有的纲要,不写待核查事项)；#目标:删除 计划删除；#计划新建 文件尚不存在",
	"#纲要格式：文件名[ABCDE]: F:功能 | R:关联 | A:接口 | S:简述",
	"#纲要格式：文件名[标签]: Role:职责 | Uses:依赖 | API:对外契约 | Constraints:约束",
	"F:功能 | R:关联 | A:接口 | S:简述", "Role:职责 | Uses:依赖 | API:对外契约 | Constraints:约束",
	"#【标签ABCDE】紧凑连写,首个数字是C重要度的切分锚点,B与D符号禁用数字,D可多选连写可为空",
	"#【标签】[C A B D… E] 重要度数字在前,其后层级、模块、特征(0到多个)、规模,空格分隔,每个是一个大写字母(模块可两个)",
	"#S配额:", "#Constraints配额:",
	"未来规则", "目标规则", "未来纲要", "目标纲要", "#未来:", "#目标:",
	"ortg_future", "ortg_target", "ortg future", "ortg target")

// oldEntry is an outline line with the F/R/A/S fields and a compact tag.
var oldEntry = regexp.MustCompile(`^([^\[\s]+)\[([A-Za-z0-9]*)\]: F:(.*?) \| R:(.*?) \| A:(.*?) \| S:(.*)$`)

// Entry rewrites an outline line with the F/R/A/S fields in the current
// format — the fields renamed, the compact tag spelled out — and returns any
// other line as it is.
func Entry(line string) string {
	m := oldEntry.FindStringSubmatch(line)
	if m == nil {
		return line
	}
	return m[1] + "[" + Tag(m[2]) + "]: Role:" + m[3] + " | Uses:" + m[4] + " | API:" + m[5] + " | Constraints:" + m[6]
}

// Tag spells a compact tag "ABC…DE" (layer, module up to the first digit, the
// importance digit, traits, scale) as "C A B D… E". A tag with no module
// between the layer and the digit has no place to put one and stays as it is:
// the core then reports the entry as broken and asks for it to be rewritten.
func Tag(tag string) string {
	k := strings.IndexAny(tag, "0123456789")
	if k < 2 || k >= len(tag)-1 {
		return tag
	}
	f := []string{tag[k : k+1], tag[:1], tag[1:k]}
	for _, t := range tag[k+1 : len(tag)-1] {
		f = append(f, string(t))
	}
	return strings.Join(append(f, tag[len(tag)-1:]), " ")
}

// Table rewrites the table at path in the current format when an older ORTG
// wrote it, keeping the old bytes in <path>.old — not .bak, which the first
// save afterwards overwrites — and reports whether it did. A version-1 table
// gets the current version line and column header (a ten-column one gains an
// empty module column, which the core derives from the outline on the next
// save), its outline and target outline lines in the current fields, and its
// header in the current terms. Anything else it leaves alone.
func Table(path string) (bool, error) {
	old, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	text := strings.ReplaceAll(strings.TrimPrefix(string(old), "\uFEFF"), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	if strings.TrimSpace(lines[0]) != v1Version {
		return false, nil
	}
	col := -1
	for i, l := range lines {
		if strings.HasPrefix(l, "path\t") {
			col = i
			break
		}
	}
	if col < 0 || (lines[col] != tenColumns && lines[col] != futureColumns && lines[col] != base.ColumnHeader()) {
		return false, nil
	}
	lines[0] = base.VersionLine()
	for j := col + 1; j < len(lines); j++ {
		f := strings.Split(lines[j], "\t")
		if lines[col] == tenColumns && len(f) == 10 {
			f = append(append(append([]string{}, f[:5]...), ""), f[5:]...)
		}
		if len(f) == 11 { // outline and target outline
			f[6], f[8] = Entry(f[6]), Entry(f[8])
		}
		lines[j] = strings.Join(f, "\t")
	}
	lines[col] = base.ColumnHeader()
	for i := 1; i < col && !strings.HasPrefix(lines[i], "{"); i++ { // the # lines before the JSON object
		lines[i] = headerTerms.Replace(lines[i])
	}
	if err := os.WriteFile(path+".old", old, 0o644); err != nil {
		return false, err
	}
	if err := base.AtomicWrite(path, []byte(strings.Join(lines, "\n"))); err != nil {
		return false, err
	}
	return true, nil
}

// v1Terms rewrites ORTG v1 wording and tag-dictionary keys.
var v1Terms = strings.NewReplacer(
	"索引", "纲要",
	"#A Layer:", "#A层级:", "#B Module:", "#B模块:", "#C Importance:", "#C重要度:",
	"#D Trait:", "#D特征:", "#E Scale:", "#E规模:",
)

// volumeMarker heads an ORTG v1 code volume: a format artifact, never part of
// the header.
const volumeMarker = "#ORTG-CODE-VOLUME:"

// Document rewrites an older outline document in the current format: it
// drops v1 volume markers, applies the v1 wording, renames the 未来 markers
// of documents written before the target rename, and puts entry lines in the
// current fields.
func Document(doc string) string {
	lines := strings.Split(doc, "\n")
	out := lines[:0]
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, volumeMarker) {
			continue
		}
		if strings.HasPrefix(t, "#未来:") {
			l = "#目标:" + strings.TrimPrefix(t, "#未来:")
		}
		if t := strings.TrimSpace(l); strings.HasPrefix(t, "#目标: ") {
			l = "#目标: " + Entry(strings.TrimPrefix(t, "#目标: "))
		} else {
			l = Entry(l)
		}
		out = append(out, l)
	}
	return v1Terms.Replace(strings.Join(out, "\n"))
}

// OldDoc is the outline document an older ORTG kept at a repository root.
const OldDoc = "overview.txt"

// Result reports how Import built a new table.
type Result struct {
	Found  bool // the root had an old document at all
	FromV1 bool // the entries came from ORTG v1's split volumes
	Res    base.ImportResult
	Err    error  // why the old document could not be imported, when nothing could
	Backup string // where overview.txt was moved; "" when the move failed
}

// Import fills a new table from the outline documents an older ORTG left at
// root: overview.txt, moved aside first so no stale copy stays where a model
// would read it, and failing that (an error, or no entries) ORTG v1's split
// volumes merged into template. imp imports one current-format document.
func Import(root, template string, imp func(doc string) (base.ImportResult, error)) Result {
	var r Result
	doc, err := os.ReadFile(filepath.Join(root, OldDoc))
	if err != nil {
		return r
	}
	r.Found = true
	r.Backup = backup(root)
	r.Res, r.Err = imp(Document(string(doc)))
	if r.Err == nil && r.Res.Entries > 0 {
		return r
	}
	if v1, ok := v1Doc(root, template); ok {
		if res, err := imp(v1); err == nil {
			r.Res, r.Err, r.FromV1 = res, nil, true
		}
	}
	return r
}

// backup renames <root>/overview.txt to the first free overview.txt.bak name
// and returns it, never clobbering an earlier backup; "" when it could not.
func backup(root string) string {
	for i := 1; i <= 20; i++ {
		name := OldDoc + ".bak"
		if i > 1 {
			name = fmt.Sprintf("%s.bak.%d", OldDoc, i)
		}
		if _, err := os.Stat(filepath.Join(root, name)); err == nil {
			continue
		}
		if os.Rename(filepath.Join(root, OldDoc), filepath.Join(root, name)) != nil {
			return ""
		}
		return name
	}
	return ""
}

// v1DictKeys are the header dictionary lines a v1 meta volume may replace,
// in current spelling (v1Terms renames the v1 keys on the way in).
var v1DictKeys = []string{"#A层级:", "#B模块:", "#C重要度:", "#D特征:", "#E规模:"}

// v1Doc assembles a current-format document from ORTG v1 split volumes: the
// code tag dictionaries of overview.meta.txt replace the matching lines of
// template, followed by the body of overview.code.txt. The template's #S配额
// is kept on purpose — it is the quota the core enforces. It reports false
// when there is no code volume or no dictionaries to take.
func v1Doc(root, template string) (string, bool) {
	code, err := os.ReadFile(filepath.Join(root, "overview.code.txt"))
	if err != nil {
		return "", false
	}
	meta, err := os.ReadFile(filepath.Join(root, "overview.meta.txt"))
	if err != nil {
		return "", false
	}
	repl := map[string]string{} // current dictionary key -> replacement line
	inCodeDict := false
	for _, raw := range strings.Split(Document(string(meta)), "\n") {
		l := strings.TrimSpace(raw)
		if strings.HasPrefix(l, "#[Tag dictionary:") {
			inCodeDict = l == "#[Tag dictionary: code]"
			continue
		}
		if !inCodeDict {
			continue
		}
		for _, key := range v1DictKeys {
			if strings.HasPrefix(l, key) {
				repl[key] = l
			}
		}
	}
	for _, key := range []string{"#A层级:", "#B模块:", "#E规模:"} { // the header minimum
		if repl[key] == "" {
			return "", false
		}
	}
	var hdr []string
	for _, l := range strings.Split(template, "\n") {
		swapped := false
		for key, line := range repl {
			if strings.HasPrefix(l, key) {
				hdr = append(hdr, line)
				swapped = true
				break
			}
		}
		if !swapped {
			hdr = append(hdr, l)
		}
	}
	return Document(strings.Join(hdr, "\n") + "\n" + string(code)), true
}
