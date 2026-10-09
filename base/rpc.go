package base

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"sort"
)

// Prop describes one tool argument.
type Prop struct {
	Type        string
	Description string
	Enum        []string
}

// Tool is a registered MCP tool; the schema is generated from Props.
type Tool struct {
	Name        string
	Description string
	Props       map[string]Prop
	Required    []string
	Handler     func(Args) (string, error)
	// Meta is published as the tool's _meta in tools/list: host-specific
	// hints such as Claude Code's anthropic/maxResultSizeChars.
	Meta map[string]any
	// Hidden tools stay out of tools/list — the model never sees them — but
	// answer tools/call: hooks the host runs as MCP tool calls use them.
	Hidden bool
}

// Args is the decoded arguments object.
type Args map[string]any

func (a Args) String(key string) (string, error) {
	v, ok := a[key]
	if !ok || v == nil {
		return "", nil
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("参数 %s 必须是字符串", key)
	}
	return s, nil
}

func (a Args) Bool(key string) (bool, error) {
	v, ok := a[key]
	if !ok || v == nil {
		return false, nil
	}
	b, ok := v.(bool)
	if !ok {
		return false, fmt.Errorf("参数 %s 必须是布尔值", key)
	}
	return b, nil
}

func (a Args) Strings(key string) ([]string, error) {
	v, ok := a[key]
	if !ok || v == nil {
		return nil, nil
	}
	list, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("参数 %s 必须是字符串数组", key)
	}
	out := make([]string, 0, len(list))
	for _, it := range list {
		s, ok := it.(string)
		if !ok {
			return nil, fmt.Errorf("参数 %s 必须是字符串数组", key)
		}
		out = append(out, s)
	}
	return out, nil
}

type rpcRequest struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

func schema(t Tool) map[string]any {
	props := map[string]any{}
	for name, p := range t.Props {
		m := map[string]any{"type": p.Type, "description": p.Description}
		if len(p.Enum) > 0 {
			m["enum"] = p.Enum
		}
		if p.Type == "array" {
			m["items"] = map[string]any{"type": "string"}
		}
		props[name] = m
	}
	req := t.Required
	if req == nil {
		req = []string{}
	}
	return map[string]any{"type": "object", "properties": props, "required": req, "additionalProperties": false}
}

// Serve runs the stdio JSON-RPC 2.0 loop: one request per line, one
// response per line. Notifications (no id) get no reply.
func Serve(in io.Reader, out io.Writer, name, version string, tools []Tool) error {
	byName := map[string]Tool{}
	for _, t := range tools {
		byName[t.Name] = t
	}
	w := bufio.NewWriter(out)
	send := func(v any) {
		b, _ := json.Marshal(v)
		w.Write(b)
		w.WriteByte('\n')
		w.Flush()
	}
	errResp := func(id json.RawMessage, code int, msg string) {
		if id == nil {
			id = json.RawMessage("null")
		}
		send(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": msg}})
	}
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal(line, &req); err != nil {
			errResp(nil, -32700, "parse error")
			continue
		}
		if req.ID == nil || string(req.ID) == "null" {
			continue
		}
		result := func(v any) { send(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": v}) }
		switch req.Method {
		case "initialize":
			var p struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			_ = json.Unmarshal(req.Params, &p)
			if p.ProtocolVersion == "" {
				p.ProtocolVersion = "2025-06-18"
			}
			result(map[string]any{"protocolVersion": p.ProtocolVersion, "capabilities": map[string]any{"tools": map[string]any{}},
				"serverInfo": map[string]any{"name": name, "version": version}})
		case "ping":
			result(map[string]any{})
		case "tools/list":
			list := make([]map[string]any, 0, len(tools))
			for _, t := range tools {
				if t.Hidden {
					continue
				}
				entry := map[string]any{"name": t.Name, "description": t.Description, "inputSchema": schema(t)}
				if len(t.Meta) > 0 {
					entry["_meta"] = t.Meta
				}
				list = append(list, entry)
			}
			result(map[string]any{"tools": list})
		case "tools/call":
			var p struct {
				Name      string `json:"name"`
				Arguments Args   `json:"arguments"`
			}
			if err := json.Unmarshal(req.Params, &p); err != nil {
				errResp(req.ID, -32602, "invalid params")
				continue
			}
			t, ok := byName[p.Name]
			if !ok {
				errResp(req.ID, -32602, "unknown tool "+p.Name)
				continue
			}
			text, err := callTool(t, p.Arguments)
			resp := map[string]any{"content": []map[string]any{{"type": "text", "text": text}}}
			if err != nil {
				resp["content"] = []map[string]any{{"type": "text", "text": err.Error()}}
				resp["isError"] = true
			}
			result(resp)
		default:
			errResp(req.ID, -32601, "method not found: "+req.Method)
		}
	}
	return sc.Err()
}

func callTool(t Tool, args Args) (text string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("internal error: %v", r)
		}
	}()
	if args == nil {
		args = Args{}
	}
	var unknown []string
	for k := range args {
		if _, ok := t.Props[k]; !ok {
			unknown = append(unknown, k)
		}
	}
	sort.Strings(unknown)
	if len(unknown) > 0 {
		return "", fmt.Errorf("未知参数: %v", unknown)
	}
	for _, k := range t.Required {
		if v, ok := args[k]; !ok || v == nil || v == "" {
			return "", fmt.Errorf("缺少参数 %s", k)
		}
	}
	return t.Handler(args)
}
