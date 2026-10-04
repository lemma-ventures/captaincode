package mcpclient

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDepthHelper(t *testing.T) {
	if os.Getenv("DEPTH_HELPER") != "1" {
		return
	}
	logPath := os.Getenv("DEPTH_LOG")
	old, _ := os.ReadFile(logPath)
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		os.Exit(2)
	}
	fmt.Fprintln(f, os.Getenv("MCP_CALL_DEPTH"))
	f.Close()
	scan := bufio.NewScanner(os.Stdin)
	enc := json.NewEncoder(os.Stdout)
	for scan.Scan() {
		var msg struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		if json.Unmarshal(scan.Bytes(), &msg) != nil {
			os.Exit(3)
		}
		if msg.ID == 0 {
			continue
		}
		var result any
		switch msg.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}}}
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]string{"name": "recurse"}}}
		case "tools/call":
			answer := "test safety stop"
			if strings.Count(string(old), "\n") < 6 {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				nested, err := Call(ctx, os.Getenv("DEPTH_CONFIG"), "recurse", map[string]any{}, map[string]string{"MCP_CALL_DEPTH": "0"})
				cancel()
				if err != nil {
					answer = err.Error()
				} else {
					answer = nested.Text()
				}
			}
			result = map[string]any{"content": []any{map[string]string{"type": "text", "text": answer}}}
		}
		if enc.Encode(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": result}) != nil {
			os.Exit(4)
		}
	}
	os.Exit(0)
}

func depthConfig(t *testing.T) (string, string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	config := filepath.Join(dir, "mcp.json")
	log := filepath.Join(dir, "starts")
	data, _ := json.Marshal(Config{Command: exe, Args: []string{"-test.run=^TestDepthHelper$"}, Env: map[string]string{
		"DEPTH_HELPER": "1", "DEPTH_CONFIG": config, "DEPTH_LOG": log, "MCP_CALL_DEPTH": "0"}})
	if err := os.WriteFile(config, data, 0600); err != nil {
		t.Fatal(err)
	}
	return config, log
}

func TestRecursiveMCPStopsBeforeFifthLaunch(t *testing.T) {
	t.Setenv("MCP_CALL_DEPTH", "")
	config, log := depthConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for i := 0; i < 2; i++ {
		os.Remove(log)
		result, err := Call(ctx, config, "recurse", map[string]any{}, map[string]string{"MCP_CALL_DEPTH": "0"})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(result.Text(), "MCP call depth") {
			t.Fatalf("recursion not blocked: %s", result.Text())
		}
		data, err := os.ReadFile(log)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != "1\n2\n3\n4\n" {
			t.Fatalf("unexpected launches: %q", data)
		}
	}
}

func TestInvalidOrExhaustedMCPDepthDoesNotLaunch(t *testing.T) {
	for _, depth := range []string{"4", "5", "-1", "junk", "01", "99999999999999999999999999999"} {
		t.Run(depth, func(t *testing.T) {
			t.Setenv("MCP_CALL_DEPTH", depth)
			config, log := depthConfig(t)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_, err := Call(ctx, config, "recurse", nil, nil)
			if err == nil || !strings.Contains(err.Error(), "MCP call depth") {
				t.Fatalf("depth %q accepted: %v", depth, err)
			}
			if _, err := os.Stat(log); !os.IsNotExist(err) {
				t.Fatal("server started for rejected depth")
			}
		})
	}
}

func TestConcurrentMCPQueriesHaveIndependentDepth(t *testing.T) {
	t.Setenv("MCP_CALL_DEPTH", "")
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		config, log := depthConfig(t)
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			result, err := Call(ctx, config, "recurse", nil, nil)
			if err != nil || !strings.Contains(result.Text(), "MCP call depth") {
				t.Errorf("call failed: %v %s", err, result.Text())
				return
			}
			data, err := os.ReadFile(log)
			if err != nil || string(data) != "1\n2\n3\n4\n" {
				t.Errorf("cross-request depth: %q %v", data, err)
			}
		}()
	}
	wg.Wait()
}
