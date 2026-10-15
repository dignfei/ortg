package base

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func TestServeProtocol(t *testing.T) {
	tools := []Tool{{Name: "echo", Description: "d", Props: map[string]Prop{"s": {Type: "string"}}, Required: []string{"s"},
		Handler: func(a Args) (string, error) { s, _ := a.String("s"); return "hi " + s, nil }},
		{Name: "boom", Description: "d", Props: map[string]Prop{}, Handler: func(a Args) (string, error) { panic("x") }}}
	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"echo","arguments":{"s":"a"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"echo","arguments":{"s":"a","zz":1}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"boom","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":6,"method":"nope"}`,
		`not json`,
		`{"jsonrpc":"2.0","id":7,"method":"ping"}`,
	}, "\n") + "\n"
	pr, pw := io.Pipe()
	go func() { Serve(strings.NewReader(in), pw, "ortg", "t", tools); pw.Close() }()
	var lines []map[string]any
	sc := bufio.NewScanner(pr)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("stdout not pure JSON: %q", sc.Text())
		}
		lines = append(lines, m)
	}
	if len(lines) != 8 {
		t.Fatalf("expected 8 responses (notification silent), got %d", len(lines))
	}
	if lines[0]["result"].(map[string]any)["protocolVersion"] != "2025-06-18" {
		t.Fatal("initialize did not echo protocol version")
	}
	list := lines[1]["result"].(map[string]any)["tools"].([]any)
	schema := list[0].(map[string]any)["inputSchema"].(map[string]any)
	if len(list) != 2 || schema["additionalProperties"] != false {
		t.Fatalf("tools/list wrong: %v", lines[1])
	}
	text := func(i int) string {
		return lines[i]["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	}
	if text(2) != "hi a" || lines[3]["result"].(map[string]any)["isError"] != true || !strings.Contains(text(3), "zz") || lines[4]["result"].(map[string]any)["isError"] != true {
		t.Fatalf("call results wrong: %v %v %v", lines[2], lines[3], lines[4])
	}
	if lines[5]["error"].(map[string]any)["code"].(float64) != -32601 || lines[6]["error"].(map[string]any)["code"].(float64) != -32700 {
		t.Fatalf("error codes wrong: %v %v", lines[5], lines[6])
	}
}

// Instructions go out with the initialize result, and only when given.
func TestInitializeInstructions(t *testing.T) {
	init := func(instructions ...string) map[string]any {
		var out bytes.Buffer
		Serve(strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`+"\n"), &out, "t", "0", nil, instructions...)
		var resp struct {
			Result map[string]any `json:"result"`
		}
		if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
			t.Fatalf("%v: %s", err, out.String())
		}
		return resp.Result
	}
	if got := init("off here"); got["instructions"] != "off here" {
		t.Fatalf("instructions: %v", got)
	}
	if _, ok := init()["instructions"]; ok {
		t.Fatal("no instructions key without instructions")
	}
}

// A tool's Meta is published as _meta in tools/list; tools without one carry
// no _meta key at all.
func TestToolsListMeta(t *testing.T) {
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}` + "\n")
	var out bytes.Buffer
	tools := []Tool{
		{Name: "big", Meta: map[string]any{"anthropic/maxResultSizeChars": 500000}, Handler: func(Args) (string, error) { return "", nil }},
		{Name: "plain", Handler: func(Args) (string, error) { return "", nil }},
	}
	Serve(in, &out, "t", "0", tools)
	var resp struct {
		Result struct {
			Tools []map[string]any `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("%v: %s", err, out.String())
	}
	got := map[string]any{}
	for _, tl := range resp.Result.Tools {
		got[tl["name"].(string)] = tl["_meta"]
	}
	if m, ok := got["big"].(map[string]any); !ok || m["anthropic/maxResultSizeChars"] != float64(500000) {
		t.Fatalf("big tool _meta: %v", got["big"])
	}
	if got["plain"] != nil {
		t.Fatalf("plain tool must carry no _meta: %v", got["plain"])
	}
}

// A hidden tool answers tools/call — the host's mcp_tool hooks call it by
// name — but stays out of tools/list, so the model never sees it.
func TestHiddenTool(t *testing.T) {
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"gate","arguments":{}}}` + "\n")
	var out bytes.Buffer
	tools := []Tool{
		{Name: "gate", Hidden: true, Handler: func(Args) (string, error) { return "from gate", nil }},
		{Name: "plain", Handler: func(Args) (string, error) { return "", nil }},
	}
	Serve(in, &out, "t", "0", tools)
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 || strings.Contains(lines[0], `"gate"`) || !strings.Contains(lines[0], `"plain"`) {
		t.Fatalf("tools/list must list plain and not gate: %s", out.String())
	}
	if !strings.Contains(lines[1], "from gate") || strings.Contains(lines[1], "isError") {
		t.Fatalf("hidden tool must still be callable: %s", lines[1])
	}
}

// A call's _meta reaches the handler under "_meta" (the host names the
// session there); an argument of that name is refused like any unknown one,
// and a call without _meta brings no such key.
func TestCallMeta(t *testing.T) {
	in := strings.NewReader(strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"who","arguments":{},"_meta":{"threadId":"t1","progressToken":1}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"who","arguments":{"_meta":{"threadId":"forged"}}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"who"}}`,
	}, "\n") + "\n")
	var out bytes.Buffer
	tools := []Tool{{Name: "who", Handler: func(a Args) (string, error) {
		m, ok := a["_meta"].(map[string]any)
		if !ok {
			return "no meta", nil
		}
		return "thread " + m["threadId"].(string), nil
	}}}
	Serve(in, &out, "t", "0", tools)
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("want 3 replies: %s", out.String())
	}
	if !strings.Contains(lines[0], "thread t1") {
		t.Fatalf("the call's _meta must reach the handler: %s", lines[0])
	}
	if !strings.Contains(lines[1], "isError") || !strings.Contains(lines[1], "_meta") || strings.Contains(lines[1], "forged") {
		t.Fatalf("_meta as an argument must be refused: %s", lines[1])
	}
	if !strings.Contains(lines[2], "no meta") {
		t.Fatalf("a call without _meta carries none: %s", lines[2])
	}
}
