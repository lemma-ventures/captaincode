package mcpclient

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

type Config struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
}

type Result struct {
	StructuredContent json.RawMessage `json:"structuredContent"`
	Content           []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	IsError bool `json:"isError"`
}

func (r Result) Text() string {
	var out string
	for _, c := range r.Content {
		if c.Type == "text" {
			if out != "" {
				out += "\n"
			}
			out += c.Text
		}
	}
	return out
}

func ReadConfig(path string) (Config, error) {
	var c Config
	if !filepath.IsAbs(path) {
		return c, fmt.Errorf("MCP config path must be absolute")
	}
	f, err := os.Open(path)
	if err != nil {
		return c, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil || len(data) > 65536 {
		return c, fmt.Errorf("MCP config exceeds 64 KiB")
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return c, err
	}
	if !filepath.IsAbs(c.Command) {
		return c, fmt.Errorf("MCP command must be absolute")
	}
	return c, nil
}

func Call(ctx context.Context, path, tool string, args any, env map[string]string) (Result, error) {
	var result Result
	depth := os.Getenv("MCP_CALL_DEPTH")
	if depth == "" {
		depth = "0"
	}
	hops, err := strconv.Atoi(depth)
	if err != nil || hops < 0 || hops >= 4 || strconv.Itoa(hops) != depth {
		return result, fmt.Errorf("MCP call depth invalid or exhausted")
	}
	cfg, err := ReadConfig(path)
	if err != nil {
		return result, err
	}
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, cfg.Command, cfg.Args...)
	cmd.WaitDelay = time.Second
	values := map[string]string{}
	for _, k := range []string{"PATH", "TMPDIR", "SYSTEMROOT"} {
		if v, ok := os.LookupEnv(k); ok {
			values[k] = v
		}
	}
	for k, v := range cfg.Env {
		values[k] = v
	}
	for k, v := range env {
		values[k] = v
	}
	values["MCP_CALL_DEPTH"] = strconv.Itoa(hops + 1)
	for k, v := range values {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	in, err := cmd.StdinPipe()
	if err != nil {
		return result, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		in.Close()
		return result, err
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		in.Close()
		out.Close()
		return result, err
	}
	defer func() { in.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	encoder := json.NewEncoder(in)
	scanner := bufio.NewScanner(out)
	scanner.Buffer(make([]byte, 65536), 4<<20)
	send := func(id int, method string, params any) error {
		msg := map[string]any{"jsonrpc": "2.0", "method": method}
		if id != 0 {
			msg["id"] = id
		}
		if params != nil {
			msg["params"] = params
		}
		return encoder.Encode(msg)
	}
	receive := func(id int, into any) error {
		for scanner.Scan() {
			var reply struct {
				JSONRPC string          `json:"jsonrpc"`
				ID      *int            `json:"id"`
				Method  string          `json:"method"`
				Result  json.RawMessage `json:"result"`
				Error   json.RawMessage `json:"error"`
			}
			if err := json.Unmarshal(scanner.Bytes(), &reply); err != nil {
				return err
			}
			if reply.JSONRPC != "2.0" {
				return fmt.Errorf("invalid MCP envelope")
			}
			if reply.ID == nil && reply.Method != "" {
				continue
			}
			if reply.ID == nil || *reply.ID != id || len(reply.Error) > 0 {
				return fmt.Errorf("MCP request failed or response ID mismatch")
			}
			return json.Unmarshal(reply.Result, into)
		}
		if err := scanner.Err(); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return fmt.Errorf("MCP server closed the connection")
	}
	if err := send(1, "initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "memory-client", "version": "1.0.0"}}); err != nil {
		return result, err
	}
	var init struct {
		Version      string                     `json:"protocolVersion"`
		Capabilities map[string]json.RawMessage `json:"capabilities"`
	}
	if err := receive(1, &init); err != nil {
		return result, err
	}
	if init.Version != "2025-06-18" && init.Version != "2025-03-26" && init.Version != "2024-11-05" {
		return result, fmt.Errorf("unsupported MCP protocol version")
	}
	if _, ok := init.Capabilities["tools"]; !ok {
		return result, fmt.Errorf("MCP server lacks tools capability")
	}
	if err := send(0, "notifications/initialized", nil); err != nil {
		return result, err
	}
	if err := send(2, "tools/list", nil); err != nil {
		return result, err
	}
	var listing struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := receive(2, &listing); err != nil {
		return result, err
	}
	found := false
	for _, t := range listing.Tools {
		if t.Name == tool {
			found = true
		}
	}
	if !found {
		return result, fmt.Errorf("MCP tool %s is unavailable", tool)
	}
	if err := send(3, "tools/call", map[string]any{"name": tool, "arguments": args}); err != nil {
		return result, err
	}
	if err := receive(3, &result); err != nil {
		return result, err
	}
	if result.IsError {
		return result, fmt.Errorf("MCP tool %s failed", tool)
	}
	return result, nil
}
