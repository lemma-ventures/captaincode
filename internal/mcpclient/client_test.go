package mcpclient

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMCPHelperProcess(t *testing.T) {
	if os.Getenv("MCP_HELPER") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	stage := 0
	for scanner.Scan() {
		var msg struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
			Params struct {
				Name string `json:"name"`
			} `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &msg) != nil {
			os.Exit(2)
		}
		var result any
		switch stage {
		case 0:
			if msg.Method != "initialize" {
				os.Exit(3)
			}
			result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}}}
		case 1:
			if msg.Method != "notifications/initialized" {
				os.Exit(4)
			}
			stage++
			continue
		case 2:
			if msg.Method != "tools/list" {
				os.Exit(5)
			}
			result = map[string]any{"tools": []any{map[string]string{"name": "fixture"}}}
		case 3:
			if msg.Method != "tools/call" || msg.Params.Name != "fixture" {
				os.Exit(6)
			}
			if os.Getenv("MCP_CASE") == "timeout" {
				time.Sleep(10 * time.Second)
			}
			if os.Getenv("MCP_CASE") == "wrong-id" {
				msg.ID++
			}
			result = map[string]any{"isError": os.Getenv("MCP_CASE") == "tool-error", "structuredContent": map[string]string{"answer": "fixture"}, "content": []any{map[string]string{"type": "text", "text": "fixture"}}}
		}
		data, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": result})
		fmt.Println(string(data))
		stage++
	}
	os.Exit(0)
}

func TestCallNegotiatesCapabilitiesAndRejectsFailures(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"ok", "wrong-id", "tool-error", "timeout", "missing-tool"} {
		t.Run(mode, func(t *testing.T) {
			config := filepath.Join(t.TempDir(), "mcp.json")
			data, _ := json.Marshal(Config{Command: executable, Args: []string{"-test.run=^TestMCPHelperProcess$"}, Env: map[string]string{"MCP_HELPER": "1", "MCP_CASE": mode}})
			if err := os.WriteFile(config, data, 0o600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			tool := "fixture"
			if mode == "missing-tool" {
				tool = "absent"
			}
			result, err := Call(ctx, config, tool, map[string]string{"query": "fixture"}, nil)
			if mode == "ok" {
				if err != nil || result.Text() != "fixture" {
					t.Fatalf("result %v, error %v", result, err)
				}
			} else if err == nil {
				t.Fatal("expected failure")
			}
		})
	}
}
