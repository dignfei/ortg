package base

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// tsvVersionLine is the first line of the table and names its format. 2:
// outline lines carry the fields Role/Uses/API/Constraints and a tag with the
// importance digit first and its other values separated by spaces.
const tsvVersionLine = "#ORTG-TSV: 2"

// VersionLine is the current version line; a table with another one was
// written by another ORTG (the migrate package upgrades older ones).
func VersionLine() string { return tsvVersionLine }

var columns = []string{"path", "crc32", "ignored", "deleted", "mtime", "module", "outline", "outline_time", "target_outline", "target_time", "target_delete"}

// ColumnHeader is the current column header line. The core reads no other:
// tables an older ORTG wrote are the migrate package's business.
func ColumnHeader() string { return strings.Join(columns, "\t") }

// ErrNoTable means ortg.tsv does not exist; ErrCorrupt means it cannot be
// trusted; ErrFormat means its version line or column header is not the
// current one — a table an older ORTG wrote (migrate upgrades it), or a newer
// one this ORTG predates.
var (
	ErrNoTable = errors.New("ortg.tsv not found")
	ErrCorrupt = errors.New("ortg.tsv corrupt")
	ErrFormat  = errors.New("ortg.tsv format not recognized")
)

type record struct {
	Path         string
	CRC          uint32
	Ignored      bool
	Deleted      bool
	Mtime        time.Time
	Module       string
	Outline      string
	OutlineTime  time.Time
	Target       string
	TargetTime   time.Time
	TargetDelete bool
}

// moduleOrDerived is the module cell: the B tag of the outline, so the column
// is written from the outline rather than tracked separately.
func (r *record) moduleOrDerived() string {
	if r.Module == "" {
		return moduleOf(r.Outline)
	}
	return r.Module
}

type config struct{ raw map[string]json.RawMessage }

func (c *config) strings(key string) []string {
	var out []string
	if v, ok := c.raw[key]; ok {
		_ = json.Unmarshal(v, &out)
	}
	return out
}

func (c *config) stringMap(key string) map[string]string {
	out := map[string]string{}
	if v, ok := c.raw[key]; ok {
		_ = json.Unmarshal(v, &out)
	}
	return out
}

func (c *config) set(key string, v any) {
	if c.raw == nil {
		c.raw = map[string]json.RawMessage{}
	}
	b, _ := json.Marshal(v)
	c.raw[key] = b
}

// table is the in-memory ortg.tsv. path=="" is a memory-only table.
type table struct {
	path    string
	header  string
	cfg     config
	rows    map[string]*record
	size    int64
	mtime   time.Time
	inBatch bool
}

func newTable(path, header string) *table {
	return &table{path: path, header: strings.TrimRight(header, "\n"), cfg: config{raw: map[string]json.RawMessage{}}, rows: map[string]*record{}}
}

func openTable(path string) (*table, error) {
	st, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoTable
	}
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	t := newTable(path, "")
	if err := t.parse(data); err != nil {
		return nil, err
	}
	t.size, t.mtime = st.Size(), st.ModTime()
	return t, nil
}

func (t *table) parse(data []byte) error {
	data = bytes.TrimPrefix(data, []byte("\uFEFF"))
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	i := 0
	switch v := strings.TrimSpace(lines[0]); {
	case v == tsvVersionLine:
	case strings.HasPrefix(v, "#ORTG-TSV:"):
		return fmt.Errorf("%w: %s (this ORTG reads %s)", ErrFormat, v, tsvVersionLine)
	default:
		return fmt.Errorf("%w: missing %s", ErrCorrupt, tsvVersionLine)
	}
	i++
	var hdr []string
	for ; i < len(lines) && strings.HasPrefix(lines[i], "#"); i++ {
		hdr = append(hdr, lines[i])
	}
	t.header = strings.Join(hdr, "\n")
	for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
		i++
	}
	if i < len(lines) && strings.HasPrefix(lines[i], "{") {
		var js []string
		for ; i < len(lines); i++ {
			js = append(js, lines[i])
			if strings.TrimSpace(lines[i]) == "}" || (len(js) == 1 && strings.HasSuffix(strings.TrimSpace(lines[i]), "}")) {
				i++
				break
			}
		}
		raw := map[string]json.RawMessage{}
		if err := json.Unmarshal([]byte(strings.Join(js, "\n")), &raw); err != nil {
			return fmt.Errorf("%w: config json: %v", ErrCorrupt, err)
		}
		t.cfg.raw = raw
	}
	for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
		i++
	}
	switch {
	case i >= len(lines):
		return fmt.Errorf("%w: column header missing", ErrCorrupt)
	case lines[i] != ColumnHeader():
		return fmt.Errorf("%w: column header %q", ErrFormat, lines[i])
	}
	i++
	t.rows = map[string]*record{}
	for ; i < len(lines); i++ {
		if lines[i] == "" {
			continue
		}
		r, err := parseRow(lines[i])
		if err != nil {
			return fmt.Errorf("%w: line %d: %v", ErrCorrupt, i+1, err)
		}
		if _, dup := t.rows[r.Path]; dup {
			return fmt.Errorf("%w: duplicate path %s", ErrCorrupt, r.Path)
		}
		t.rows[r.Path] = r
	}
	return nil
}

func parseRow(line string) (*record, error) {
	f := strings.Split(line, "\t")
	if len(f) != len(columns) {
		return nil, fmt.Errorf("expected %d columns, got %d", len(columns), len(f))
	}
	crc, err := strconv.ParseUint(f[1], 16, 32)
	if err != nil {
		return nil, fmt.Errorf("bad crc32 %q", f[1])
	}
	r := &record{Path: f[0], CRC: uint32(crc), Ignored: f[2] == "1", Deleted: f[3] == "1", Module: f[5], Outline: f[6], Target: f[8], TargetDelete: f[10] == "1"}
	for _, p := range []struct {
		s   string
		dst *time.Time
	}{{f[4], &r.Mtime}, {f[7], &r.OutlineTime}, {f[9], &r.TargetTime}} {
		if p.s == "" {
			continue
		}
		ts, err := time.Parse(time.RFC3339, p.s)
		if err != nil {
			return nil, fmt.Errorf("bad time %q", p.s)
		}
		*p.dst = ts.UTC()
	}
	return r, nil
}

func fmtTime(ts time.Time) string {
	if ts.IsZero() {
		return ""
	}
	return ts.UTC().Format(time.RFC3339)
}

func fmtBool(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

func formatRow(r *record) string {
	return strings.Join([]string{r.Path, fmt.Sprintf("%08x", r.CRC), fmtBool(r.Ignored), fmtBool(r.Deleted), fmtTime(r.Mtime), r.moduleOrDerived(), r.Outline, fmtTime(r.OutlineTime), r.Target, fmtTime(r.TargetTime), fmtBool(r.TargetDelete)}, "\t")
}

func validateCells(r *record) error {
	for _, s := range []string{r.Path, r.Module, r.Outline, r.Target} {
		if strings.ContainsAny(s, "\t\n\r") {
			return fmt.Errorf("cell contains tab or newline: %q", s)
		}
	}
	if r.Path == "" {
		return errors.New("empty path")
	}
	return nil
}

func (t *table) marshal() []byte {
	var b bytes.Buffer
	b.WriteString(tsvVersionLine + "\n")
	if t.header != "" {
		b.WriteString(t.header + "\n")
	}
	b.WriteString("\n")
	js := []byte("{}")
	if len(t.cfg.raw) > 0 {
		js, _ = json.MarshalIndent(t.cfg.raw, "", "  ")
	}
	b.Write(js)
	b.WriteString("\n\n")
	b.WriteString(strings.Join(columns, "\t") + "\n")
	for _, r := range t.list() {
		b.WriteString(formatRow(r) + "\n")
	}
	return b.Bytes()
}

// save writes only when the content changed, keeping the previous bytes in
// <path>.bak first.
func (t *table) save() error {
	if t.path == "" {
		return nil
	}
	data := t.marshal()
	if old, err := os.ReadFile(t.path); err == nil {
		if bytes.Equal(old, data) {
			return nil
		}
		_ = os.WriteFile(t.path+".bak", old, 0o644)
	}
	if err := AtomicWrite(t.path, data); err != nil {
		return err
	}
	if st, err := os.Stat(t.path); err == nil {
		t.size, t.mtime = st.Size(), st.ModTime()
	}
	return nil
}

// reloadIfChanged re-reads the file when another process replaced it.
func (t *table) reloadIfChanged() error {
	if t.path == "" {
		return nil
	}
	st, err := os.Stat(t.path)
	if err != nil {
		return err
	}
	if st.Size() == t.size && st.ModTime().Equal(t.mtime) {
		return nil
	}
	nt, err := openTable(t.path)
	if err != nil {
		return err
	}
	t.header, t.cfg, t.rows, t.size, t.mtime = nt.header, nt.cfg, nt.rows, nt.size, nt.mtime
	return nil
}

func (t *table) get(p string) *record { return t.rows[p] }

func (t *table) upsert(r *record) error {
	if err := validateCells(r); err != nil {
		return err
	}
	t.rows[r.Path] = r
	if t.inBatch {
		return nil
	}
	return t.save()
}

func (t *table) del(p string) error {
	delete(t.rows, p)
	if t.inBatch {
		return nil
	}
	return t.save()
}

func (t *table) list() []*record {
	out := make([]*record, 0, len(t.rows))
	for _, r := range t.rows {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// batch locks the file, reloads if changed, runs fn, then saves once.
func (t *table) batch(fn func() error) error {
	if t.path != "" {
		unlock, err := lock(t.path)
		if err != nil {
			return err
		}
		defer unlock()
		if err := t.reloadIfChanged(); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	t.inBatch = true
	err := fn()
	t.inBatch = false
	if err != nil {
		if t.path != "" {
			t.size = -1
			_ = t.reloadIfChanged()
		}
		return err
	}
	return t.save()
}
