package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"ortg/base"
)

func main() {
	os.Exit(base.Run(commands(), os.Args[1:]))
}

func commands() []base.Command {
	return []base.Command{
		{Name: "scan", Usage: "对账并建表(无表时)", Run: func(args []string) error { return printResult(Scan()) }},
		{Name: "status", Usage: "只读对账报告", Run: func(args []string) error { return printResult(Status()) }},
		{Name: "align", Usage: "--all | 路径...  人工核对后前移指纹", Run: runAlign},
		{Name: "target", Usage: "--path P --outline L | --path P --delete | --document FILE", Run: runTarget},
		{Name: "hook", Usage: "--event session|subagent|prompt|pre|post|compact|stop  (宿主 hook 入口,读 stdin)", Run: runHook},
		{Name: "mcp", Usage: "启动 stdio MCP 服务器", Run: runMCP},
		{Name: "graph", Usage: "启动 CBM 常驻进程并刷新本仓库调用图(会话开始时 hook 在后台调用)", Run: func(args []string) error { return printResult(Graph()) }},
	}
}

func printResult(s string, err error) error {
	if err != nil {
		return err
	}
	fmt.Println(strings.TrimRight(s, "\n"))
	return nil
}

func runAlign(args []string) error {
	fs := flag.NewFlagSet("align", flag.ContinueOnError)
	all := fs.Bool("all", false, "前移全部文件")
	if err := fs.Parse(args); err != nil {
		return base.ErrUsage
	}
	if *all {
		return printResult(Align("all", nil))
	}
	if fs.NArg() == 0 {
		return fmt.Errorf("%w: align --all 或 align 路径...", base.ErrUsage)
	}
	return printResult(Align("paths", fs.Args()))
}

func runTarget(args []string) error {
	fs := flag.NewFlagSet("target", flag.ContinueOnError)
	p := fs.String("path", "", "文件路径")
	line := fs.String("outline", "", "目标纲要行；传 = 表示纲要不变")
	del := fs.Bool("delete", false, "标记计划删除")
	doc := fs.String("document", "", "整份纲要文档路径")
	if err := fs.Parse(args); err != nil {
		return base.ErrUsage
	}
	var text string
	if *doc != "" {
		b, err := os.ReadFile(*doc)
		if err != nil {
			return err
		}
		text = string(b)
	}
	return printResult(Target(*p, *line, *del, text))
}

func runHook(args []string) (err error) {
	fs := flag.NewFlagSet("hook", flag.ContinueOnError)
	event := fs.String("event", "", "session|subagent|prompt|pre|post|compact|stop")
	wrote := false
	defer func() {
		if r := recover(); r != nil {
			err = nil
			if *event == "stop" && !wrote {
				os.Stdout.Write([]byte("{}")) // Codex rejects an empty Stop reply
			}
		}
	}()
	if perr := fs.Parse(args); perr != nil {
		return nil
	}
	in := base.ReadHookInput(os.Stdin, time.Second)
	hostEvent := in.Event
	if hostEvent == "" {
		hostEvent = map[string]string{"session": "SessionStart", "subagent": "SubagentStart", "prompt": "UserPromptSubmit", "pre": "PreToolUse", "post": "PostToolUse", "compact": "PostCompact", "stop": "Stop"}[*event]
	}
	if *event == "stop" {
		hostEvent = "Stop" // the reply form is fixed by our event, not the payload's spelling
	}
	ctx, userMsg := HookReplyFull(*event, in)
	wrote = true
	base.WriteHookOutput(os.Stdout, base.Host(), hostEvent, ctx, userMsg)
	return nil
}

func runMCP(args []string) error {
	tools := []base.Tool{
		{Name: "ortg_overview", Description: toolDescription("ortg_overview"),
			Props: map[string]base.Prop{"module": {Type: "string", Description: "只看这个模块(纲要行 B 标签)；\"*\" 取全部；留空按整份预算给整份或模块清单"},
				"force": {Type: "boolean", Description: "本会话已发过的条目也重发；只在上下文里确实看不到它们时用"}},
			// Claude Code saves an MCP result above 50000 chars to a file and
			// shows the model a 2KB preview unless the tool declares a larger
			// threshold here (its ceiling is 500000); Codex keeps a result
			// up to tool_output_token_limit from its config.toml instead.
			Meta: map[string]any{"anthropic/maxResultSizeChars": claudeLimits.max},
			Handler: func(a base.Args) (string, error) {
				m, _ := a.String("module")
				f, err := a.Bool("force")
				if err != nil {
					return "", err
				}
				return Overview(m, f)
			}},
		// Called by the PreToolUse hook (type mcp_tool in hooks/hooks.json),
		// never by the model: the host fills the arguments from the hook input,
		// a field the call lacks arriving as "".
		{Name: "ortg_gate", Hidden: true,
			Props: map[string]base.Prop{"tool": {Type: "string"}, "path": {Type: "string"}, "dir": {Type: "string"},
				"command": {Type: "string"}, "agent": {Type: "string"}},
			Handler: func(a base.Args) (string, error) {
				tool, _ := a.String("tool")
				p, _ := a.String("path")
				dir, _ := a.String("dir")
				cmd, _ := a.String("command")
				agent, _ := a.String("agent")
				return Gate(tool, p, dir, cmd, agent)
			}},
		{Name: "ortg_review", Description: toolDescription("ortg_review"),
			Props: map[string]base.Prop{"rulings": {Type: "array", Description: "对上一次核对给了正文的每条契约写一行「契约键：裁决」（需求是否讲到这个场景、与守护测试一致还是相反、按哪条证据裁决）；缺了裁决计划不收敛"}},
			Handler: func(a base.Args) (string, error) {
				rs, err := a.Strings("rulings")
				if err != nil {
					return "", err
				}
				return Review(rs...)
			}},
		{Name: "ortg_status", Description: toolDescription("ortg_status"),
			Props:   map[string]base.Prop{},
			Handler: func(a base.Args) (string, error) { return Scan() }},
		{Name: "ortg_update", Description: toolDescription("ortg_update"),
			Props:    map[string]base.Prop{"path": {Type: "string", Description: "仓库相对路径"}, "outline": {Type: "string", Description: "完整纲要行"}},
			Required: []string{"path", "outline"},
			Handler: func(a base.Args) (string, error) {
				p, _ := a.String("path")
				l, _ := a.String("outline")
				return Update(p, l)
			}},
		{Name: "ortg_target", Description: toolDescription("ortg_target"),
			Props: map[string]base.Prop{"path": {Type: "string", Description: "仓库相对路径"}, "outline": {Type: "string", Description: "目标纲要行；只改实现、改完纲要不变时传 = (等于现在的纲要)"},
				"delete": {Type: "boolean", Description: "标记计划删除"}, "document": {Type: "string", Description: "整份纲要文档文本"},
				"items": {Type: "array", Description: "一次写多个文件：每项 \"路径 目标纲要行\"，纲要行写 = 表示纲要不变，写 删除 表示计划删除；给了 items 就不看 path/outline/delete"}},
			Handler: func(a base.Args) (string, error) {
				items, err := a.Strings("items")
				if err != nil {
					return "", err
				}
				if len(items) > 0 {
					return TargetItems(items)
				}
				p, _ := a.String("path")
				l, _ := a.String("outline")
				d, err := a.Bool("delete")
				if err != nil {
					return "", err
				}
				doc, _ := a.String("document")
				return Target(p, l, d, doc)
			}},
		{Name: "ortg_align", Description: toolDescription("ortg_align"),
			Props:    map[string]base.Prop{"mode": {Type: "string", Description: "all 或 paths", Enum: []string{"all", "paths"}}, "paths": {Type: "array", Description: "mode=paths 时的路径列表"}},
			Required: []string{"mode"},
			Handler: func(a base.Args) (string, error) {
				m, _ := a.String("mode")
				ps, err := a.Strings("paths")
				if err != nil {
					return "", err
				}
				return Align(m, ps)
			}},
		{Name: "ortg_header", Description: toolDescription("ortg_header"),
			Props: map[string]base.Prop{"header": {Type: "string", Description: "整块纲要头，每行以#开头"}, "ignore_dirs": {Type: "array", Description: "忽略目录"},
				"ignore_files": {Type: "array", Description: "忽略文件或glob"},
				"keep_files":   {Type: "array", Description: "白名单，压过忽略规则的文件或glob"},
				"observe_dirs": {Type: "array", Description: "观察目录：记指纹报变动，不要求纲要"}, "observe_files": {Type: "array", Description: "观察文件或glob：记指纹报变动，不要求纲要"},
				"dirs": {Type: "array", Description: "目录标题，形如 目录=标题"}},
			Handler: func(a base.Args) (string, error) {
				h, _ := a.String("header")
				id, err := a.Strings("ignore_dirs")
				if err != nil {
					return "", err
				}
				ifl, err := a.Strings("ignore_files")
				if err != nil {
					return "", err
				}
				kf, err := a.Strings("keep_files")
				if err != nil {
					return "", err
				}
				od, err := a.Strings("observe_dirs")
				if err != nil {
					return "", err
				}
				of, err := a.Strings("observe_files")
				if err != nil {
					return "", err
				}
				ds, err := a.Strings("dirs")
				if err != nil {
					return "", err
				}
				return Header(h, id, ifl, kf, od, of, ds)
			}},
	}
	return base.Serve(os.Stdin, os.Stdout, "ortg", version, tools)
}
