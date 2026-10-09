package main

import (
	"encoding/json"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func TestManifestsAgree(t *testing.T) {
	read := func(p string) map[string]any {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		return m
	}
	for _, p := range []string{".claude-plugin/plugin.json", ".codex-plugin/plugin.json"} {
		if v := read(p)["version"]; v != version {
			t.Errorf("%s version %v != binary %s", p, v, version)
		}
	}
	mk := read(".claude-plugin/marketplace.json")
	if plugins, _ := mk["plugins"].([]any); len(plugins) != 1 || plugins[0].(map[string]any)["version"] != version {
		t.Errorf("marketplace version mismatch: %v", mk["plugins"])
	}
	// Hooks reach every repo, this one included, through the installed plugin.
	// A copy in the repo's own settings is not a fallback but a second
	// registration: the host runs identical commands from both sources, so each
	// hook fires twice (two SessionStart reconciles per session, seen live).
	local, _ := read(".claude/settings.json")["hooks"].(map[string]any)
	for ev := range local {
		t.Errorf(".claude/settings.json registers %s; the plugin already provides it", ev)
	}
	type hook struct {
		ev, matcher string
		h           map[string]any
	}
	walk := func(p string) (commands, others []hook) {
		for ev, list := range read(p)["hooks"].(map[string]any) {
			for _, entry := range list.([]any) {
				matcher, _ := entry.(map[string]any)["matcher"].(string)
				for _, h := range entry.(map[string]any)["hooks"].([]any) {
					hk := hook{ev, matcher, h.(map[string]any)}
					if hk.h["type"] == "command" {
						commands = append(commands, hk)
					} else {
						others = append(others, hk)
					}
				}
			}
		}
		sort.Slice(commands, func(i, j int) bool {
			return commands[i].ev+commands[i].matcher < commands[j].ev+commands[j].matcher
		})
		return commands, others
	}
	claude, gates := walk("hooks/hooks.json")
	codex, codexOthers := walk("hooks/codex.json")
	safe := regexp.MustCompile(`^ortg hook --event (session|subagent|prompt|pre|post|compact|stop)$`)
	applyPatchMatchers := 0
	for _, hk := range claude {
		cmd, _ := hk.h["command"].(string)
		if !safe.MatchString(cmd) || strings.ContainsAny(cmd, "&|>;") {
			t.Errorf("%s: unsafe hook command %q", hk.ev, cmd)
		}
		if (hk.ev == "PreToolUse" || hk.ev == "PostToolUse") && strings.Contains(hk.matcher, "Edit") {
			if !strings.Contains(hk.matcher, "apply_patch") {
				t.Errorf("%s edit matcher does not include Codex apply_patch: %q", hk.ev, hk.matcher)
			} else {
				applyPatchMatchers++
			}
		}
	}
	// session, subagent, prompt, pre, post(Read), post(Edit|Write|MultiEdit), post(Bash), compact, stop
	if len(claude) != 9 {
		t.Errorf("expected 9 hook commands, got %d", len(claude))
	}
	if applyPatchMatchers != 2 {
		t.Errorf("expected apply_patch in both pre/post edit matchers, got %d", applyPatchMatchers)
	}
	// Codex gets its own file only because the gate cannot be shared: its MCP
	// server is named by the config.toml key, not plugin:ortg:ortg, and it fails
	// a hook whose input names a field the call lacks — its gate may only use
	// what both Bash and apply_patch carry. Every command hook must stay
	// identical in both files.
	if !reflect.DeepEqual(claude, codex) {
		t.Errorf("hooks/codex.json command hooks differ from hooks/hooks.json:\n%v\n%v", codex, claude)
	}
	if len(codexOthers) != 1 || codexOthers[0].ev != "PreToolUse" || codexOthers[0].h["type"] != "mcp_tool" ||
		codexOthers[0].h["server"] != "ortg" || codexOthers[0].h["tool"] != "ortg_gate" {
		t.Errorf("hooks/codex.json must carry exactly the ortg_gate mcp_tool hook on PreToolUse with server ortg, got %v", codexOthers)
	} else {
		for k, v := range codexOthers[0].h["input"].(map[string]any) {
			if v != "${tool_name}" && v != "${tool_input.command}" {
				t.Errorf("codex gate input %s=%v names a field Bash or apply_patch may lack", k, v)
			}
		}
	}
	if len(gates) != 1 || gates[0].ev != "PreToolUse" || gates[0].h["type"] != "mcp_tool" ||
		gates[0].h["server"] != "plugin:ortg:ortg" || gates[0].h["tool"] != "ortg_gate" {
		t.Errorf("hooks/hooks.json must carry exactly the ortg_gate mcp_tool hook on PreToolUse, got %v", gates)
	}
}
