package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func newTestServer() *Server {
	s := NewServer("go-anything", "test")
	s.Register(Tool{
		Name:   "echo",
		Schema: map[string]any{"type": "object"},
		Call: func(args json.RawMessage) (string, error) {
			var a struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return "", err
			}
			return a.Text, nil
		},
	})
	s.Register(Tool{Name: "fail", Schema: map[string]any{"type": "object"}, Call: func(json.RawMessage) (string, error) {
		return "", errors.New("boom")
	}})
	return s
}

func roundTrip(t *testing.T, s *Server, in string) []map[string]any {
	t.Helper()
	var out bytes.Buffer
	if err := s.Serve(strings.NewReader(in), &out); err != nil {
		t.Fatal(err)
	}
	var msgs []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("bad output line %q: %v", line, err)
		}
		msgs = append(msgs, m)
	}
	return msgs
}

func TestInitializeAndNotificationGetsNoReply(t *testing.T) {
	msgs := roundTrip(t, newTestServer(),
		`{"jsonrpc":"2.0","id":1,"method":"initialize"}`+"\n"+
			`{"jsonrpc":"2.0","method":"notifications/initialized"}`+"\n")
	if len(msgs) != 1 {
		t.Fatalf("want 1 reply (notification has none), got %d", len(msgs))
	}
	res := msgs[0]["result"].(map[string]any)
	if res["protocolVersion"] != ProtocolVersion {
		t.Fatalf("protocol version: %v", res["protocolVersion"])
	}
}

func TestToolsListAndCall(t *testing.T) {
	msgs := roundTrip(t, newTestServer(),
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`+"\n"+
			`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"echo","arguments":{"text":"hi"}}}`+"\n")
	tools := msgs[0]["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 2 {
		t.Fatalf("want 2 tools, got %d", len(tools))
	}
	content := msgs[1]["result"].(map[string]any)["content"].([]any)[0].(map[string]any)
	if content["text"] != "hi" {
		t.Fatalf("echo result: %v", content)
	}
}

func TestToolErrorIsInBand(t *testing.T) {
	msgs := roundTrip(t, newTestServer(),
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"fail","arguments":{}}}`+"\n")
	res := msgs[0]["result"].(map[string]any)
	if res["isError"] != true {
		t.Fatalf("tool failure must set isError: %v", res)
	}
}

func TestUnknownMethodAndParseError(t *testing.T) {
	msgs := roundTrip(t, newTestServer(),
		`{"jsonrpc":"2.0","id":5,"method":"nope"}`+"\n"+
			`{not json`+"\n")
	if msgs[0]["error"] == nil || msgs[1]["error"] == nil {
		t.Fatalf("want errors, got %v", msgs)
	}
}

func TestDuplicateToolPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("duplicate registration must panic")
		}
	}()
	s := newTestServer()
	s.Register(Tool{Name: "echo", Call: func(json.RawMessage) (string, error) { return "", nil }})
}

func TestToolErrorKeepsItsReport(t *testing.T) {
	s := NewServer("go-anything", "test")
	s.Register(Tool{Name: "check", Schema: map[string]any{"type": "object"}, Call: func(json.RawMessage) (string, error) {
		return "[FAIL] gofmt   not formatted: a.go\n", errors.New("gate: not complete")
	}})
	msgs := roundTrip(t, s, `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"check","arguments":{}}}`+"\n")
	res := msgs[0]["result"].(map[string]any)
	text := res["content"].([]any)[0].(map[string]any)["text"].(string)
	if res["isError"] != true || !strings.Contains(text, "not formatted: a.go") || !strings.Contains(text, "gate: not complete") {
		t.Fatalf("both the report and the error must reach the caller: %+v", res)
	}
}
