// Package mcp serves use cases as MCP tools over stdio (newline-delimited JSON-RPC 2.0).
// It is implemented on the standard library only, following the MCP stdio transport.
package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// ProtocolVersion is the MCP revision this server speaks.
const ProtocolVersion = "2025-06-18"

// Tool is one callable capability exposed to an agent.
type Tool struct {
	Name        string
	Description string
	// Schema is the JSON Schema for the arguments object.
	Schema map[string]any
	// Call runs the tool. It returns the result text or an error.
	Call func(args json.RawMessage) (string, error)
}

// Server dispatches JSON-RPC requests to registered tools.
type Server struct {
	Name    string
	Version string
	tools   map[string]Tool
	order   []string
}

// NewServer returns a server with no tools.
func NewServer(name, version string) *Server {
	return &Server{Name: name, Version: version, tools: map[string]Tool{}}
}

// Register adds a tool. Duplicate names are a programming error and panic.
func (s *Server) Register(t Tool) {
	if _, dup := s.tools[t.Name]; dup {
		panic("mcp: duplicate tool " + t.Name)
	}
	s.tools[t.Name] = t
	s.order = append(s.order, t.Name)
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Serve reads requests line by line from in and writes responses to out until EOF.
func (s *Server) Serve(in io.Reader, out io.Writer) error {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	enc := json.NewEncoder(out) // Encode appends a newline: one message per line
	for sc.Scan() {
		line := sc.Bytes()
		if len(trimSpace(line)) == 0 {
			continue
		}
		resp, ok := s.handle(line)
		if !ok {
			continue // notifications get no response
		}
		if err := enc.Encode(resp); err != nil {
			return fmt.Errorf("mcp: write: %w", err)
		}
	}
	return sc.Err()
}

func (s *Server) handle(line []byte) (response, bool) {
	var req request
	if err := json.Unmarshal(line, &req); err != nil {
		return response{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{-32700, "parse error: " + err.Error()}}, true
	}
	isNotification := len(req.ID) == 0
	switch req.Method {
	case "initialize":
		return s.ok(req.ID, map[string]any{
			"protocolVersion": ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": s.Name, "version": s.Version},
		}), !isNotification
	case "ping":
		return s.ok(req.ID, map[string]any{}), !isNotification
	case "tools/list":
		return s.ok(req.ID, map[string]any{"tools": s.list()}), !isNotification
	case "tools/call":
		return s.call(req), !isNotification
	case "notifications/initialized", "notifications/cancelled":
		return response{}, false
	default:
		if isNotification {
			return response{}, false
		}
		return response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{-32601, "method not found: " + req.Method}}, true
	}
}

func (s *Server) list() []map[string]any {
	out := make([]map[string]any, 0, len(s.order))
	for _, name := range s.order {
		t := s.tools[name]
		out = append(out, map[string]any{"name": t.Name, "description": t.Description, "inputSchema": t.Schema})
	}
	return out
}

func (s *Server) call(req request) response {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{-32602, "invalid params: " + err.Error()}}
	}
	t, ok := s.tools[p.Name]
	if !ok {
		return response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{-32602, "unknown tool: " + p.Name}}
	}
	text, err := t.Call(p.Arguments)
	if err != nil {
		// Tool failures are reported in-band so the agent can read and react to them. A tool may return
		// its report together with the error (a gate listing each check, a lift listing each blocker);
		// both are sent, so the reason is never lost.
		msg := err.Error()
		if text != "" {
			msg = text + msg
			if !strings.HasSuffix(text, "\n") {
				msg = text + "\n" + err.Error()
			}
		}
		return s.ok(req.ID, map[string]any{"isError": true, "content": []map[string]any{{"type": "text", "text": msg}}})
	}
	return s.ok(req.ID, map[string]any{"content": []map[string]any{{"type": "text", "text": text}}})
}

func (s *Server) ok(id json.RawMessage, result any) response {
	return response{JSONRPC: "2.0", ID: id, Result: result}
}

func trimSpace(b []byte) []byte {
	i, j := 0, len(b)
	for i < j && (b[i] == ' ' || b[i] == '\t' || b[i] == '\r') {
		i++
	}
	for j > i && (b[j-1] == ' ' || b[j-1] == '\t' || b[j-1] == '\r') {
		j--
	}
	return b[i:j]
}
