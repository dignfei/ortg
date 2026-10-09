package base

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// entry is one parsed outline line:
// name[TAG]: Role:.. | Uses:.. | API:.. | Constraints:..
type entry struct {
	Line, Name, Tag              string
	Role, Uses, API, Constraints string
	// Keys is the optional fifth field: the contract keys the file takes part
	// in, each registered in the header's 【契约】 table.
	Keys           string
	TA, TB, TD, TE string
	TC             int
	TagOK          bool
}

var entryRe = regexp.MustCompile(`^([^\[\s]+)\[([A-Za-z0-9 ]*)\]: Role:(.*?) \| Uses:(.*?) \| API:(.*?) \| Constraints:(.*)$`)

func parseEntry(line string) (entry, error) {
	m := entryRe.FindStringSubmatch(strings.TrimRight(line, "\r"))
	if m == nil {
		return entry{}, fmt.Errorf("不是纲要行(应为 文件名[标签]: Role:.. | Uses:.. | API:.. | Constraints:..): %.60s", line)
	}
	e := entry{Line: m[0], Name: m[1], Tag: m[2], Role: m[3], Uses: m[4], API: m[5], Constraints: m[6]}
	if i := strings.LastIndex(e.Constraints, keysLabel); i >= 0 {
		e.Constraints, e.Keys = e.Constraints[:i], e.Constraints[i+len(keysLabel):]
	}
	e.TA, e.TB, e.TC, e.TD, e.TE, e.TagOK = parseTag(e.Tag)
	return e, nil
}

// keysLabel opens the optional Keys field after Constraints.
const keysLabel = " | Keys:"

// splitItems splits a field into its items at commas and enumeration commas
// outside parentheses, dropping each item's parenthetical note; "-" and empty
// items are skipped.
func splitItems(field string) []string {
	var out []string
	var cur strings.Builder
	depth := 0
	flush := func() {
		if it := strings.TrimSpace(cur.String()); it != "" && it != "-" {
			out = append(out, it)
		}
		cur.Reset()
	}
	for _, c := range field {
		switch {
		case c == '(' || c == '（':
			depth++
		case c == ')' || c == '）':
			if depth > 0 {
				depth--
			}
		case depth == 0 && (c == ',' || c == '，' || c == '、'):
			flush()
		case depth == 0:
			cur.WriteRune(c)
		}
	}
	flush()
	return out
}

// contractPrefix opens a line of the header's contract table:
// "#【契约】key：text", the key ending at the first full-width colon so that
// it may hold ASCII colons and spaces (route:POST /api/v1/order).
const contractPrefix = "#【契约】"

// contracts reads the header's contract table: key → text.
func contracts(header string) map[string]string {
	out := map[string]string{}
	for _, l := range strings.Split(header, "\n") {
		l = strings.TrimSpace(l)
		if !strings.HasPrefix(l, contractPrefix) {
			continue
		}
		k, text, ok := strings.Cut(strings.TrimPrefix(l, contractPrefix), "：")
		if k = strings.TrimSpace(k); ok && k != "" {
			out[k] = strings.TrimSpace(text)
		}
	}
	return out
}

// parseTag reads a tag "C A B D… E": the importance digit C, then capital
// letters separated by spaces — the layer A, the module B (one or two
// letters), any number of traits D (joined into d), and the scale E. Every
// value is one token to a tokenizer, and the digit first puts a space before
// each letter.
func parseTag(tag string) (a, b string, c int, d, e string, ok bool) {
	f := strings.Split(tag, " ")
	if len(f) < 4 || len(f[0]) != 1 || f[0][0] < '1' || f[0][0] > '9' {
		return
	}
	for i, v := range f[1:] {
		if v == "" || strings.Trim(v, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") != "" || len(v) > 2 || (len(v) == 2 && i != 1) {
			return
		}
	}
	a, b, c, e = f[1], f[2], int(f[0][0]-'0'), f[len(f)-1]
	d = strings.Join(f[3:len(f)-1], "")
	return a, b, c, d, e, true
}

type dict map[string]map[string]bool

var (
	dictLineRe = regexp.MustCompile(`^#([ABDE])[^:：]*[:：]\s*(.*)$`)
	symbolRe   = regexp.MustCompile(`^[A-Za-z]+`)
)

// dicts extracts the A/B/D/E symbol tables from header lines.
func dicts(header string) dict {
	d := dict{}
	for _, l := range strings.Split(header, "\n") {
		m := dictLineRe.FindStringSubmatch(strings.TrimSpace(l))
		if m == nil {
			continue
		}
		set := d[m[1]]
		if set == nil {
			set = map[string]bool{}
			d[m[1]] = set
		}
		for _, tok := range strings.Fields(m[2]) {
			if s := symbolRe.FindString(tok); s != "" {
				set[s] = true
			}
		}
	}
	return d
}

// checkTag reports which axes of the entry's tag are not in the dictionary.
func checkTag(e entry, d dict) []string {
	if !e.TagOK {
		return []string{"标签不可解析: " + e.Tag}
	}
	var out []string
	has := func(axis, sym string) bool { return len(d[axis]) == 0 || d[axis][sym] }
	if !has("A", e.TA) {
		out = append(out, "A层级 "+e.TA+" 不在字典")
	}
	if !has("B", e.TB) {
		out = append(out, "B模块 "+e.TB+" 不在字典")
	}
	for _, ch := range e.TD {
		if !has("D", string(ch)) {
			out = append(out, "D特征 "+string(ch)+" 不在字典")
		}
	}
	if !has("E", e.TE) {
		out = append(out, "E规模 "+e.TE+" 不在字典")
	}
	return out
}

func consLen(e entry) int { return utf8.RuneCountInString(e.Constraints) }

// guardClause marks a test contract clause: "必须保持 X(测试名)".
const guardClause = "必须保持"

// clauses splits Constraints-like text at full- and half-width semicolons.
func clauses(text string) []string {
	return strings.FieldsFunc(text, func(c rune) bool { return c == '；' || c == ';' })
}

// contractLen counts the runes of the Constraints clauses that state a test
// contract; they have a budget of their own, so recording what the tests lock
// does not push out the other constraints.
func contractLen(e entry) int {
	n := 0
	for _, cl := range clauses(e.Constraints) {
		if strings.Contains(cl, guardClause) {
			n += utf8.RuneCountInString(cl)
		}
	}
	return n
}

var (
	testNameRe = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_:.*/-]*`)
	// A test named without a path: TestXxx (Go), test_xxx (Python, Rust),
	// xxx_test; a word merely containing "Test" (a helper type such as
	// ParserTester) or a bare "test"/"tests" is no test.
	bareTestRe = regexp.MustCompile(`^(Test[A-Z0-9_]|test_|[A-Za-z0-9_]+_test\b)`)
)

// isTestName reports whether a word cited in a contract clause names a test:
// a path holding "::" (tests::basic, regression::issue42) or a bare test name.
func isTestName(w string) bool {
	if strings.Contains(w, "::") {
		return true
	}
	return bareTestRe.MatchString(w)
}

// testNames returns the tests the contract clauses of text cite: within the
// parentheses of a clause holding "必须保持", the words isTestName accepts,
// in order, once each.
func testNames(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, cl := range clauses(text) {
		if !strings.Contains(cl, guardClause) {
			continue
		}
		depth := 0
		var cur strings.Builder
		for _, c := range cl {
			switch {
			case c == '(' || c == '（':
				depth++
				cur.WriteRune(' ')
			case c == ')' || c == '）':
				if depth > 0 {
					depth--
				}
				cur.WriteRune(' ')
			case depth > 0:
				cur.WriteRune(c)
			}
		}
		for _, w := range testNameRe.FindAllString(cur.String(), -1) {
			w = strings.TrimRight(w, ".:/-")
			if isTestName(w) && !seen[w] {
				seen[w] = true
				out = append(out, w)
			}
		}
	}
	return out
}

// moduleOf returns the B (module) segment of an outline line's tag, "" when
// the line does not parse.
func moduleOf(line string) string {
	e, err := parseEntry(strings.TrimSpace(line))
	if err != nil || !e.TagOK {
		return ""
	}
	return e.TB
}
