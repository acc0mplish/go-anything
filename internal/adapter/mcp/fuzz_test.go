package mcp

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// The MCP server reads whatever an agent sends. Any byte sequence must end the session cleanly,
// never panic, and every line it answers must be valid JSON.
func FuzzServe(f *testing.F) {
	f.Add(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`)
	f.Add(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"echo","arguments":{"text":"x"}}}`)
	f.Add(`not json`)
	f.Fuzz(func(t *testing.T, in string) {
		s := NewServer("go-anything", "test")
		s.Register(Tool{Name: "echo", Schema: map[string]any{"type": "object"}, Call: func(args json.RawMessage) (string, error) {
			return "ok", nil
		}})
		var out bytes.Buffer
		_ = s.Serve(strings.NewReader(in), &out)
		for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
			if line == "" {
				continue
			}
			var m map[string]any
			if err := json.Unmarshal([]byte(line), &m); err != nil {
				t.Fatalf("server wrote a line that is not JSON: %q", line)
			}
		}
	})
}
