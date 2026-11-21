package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/go-anything/go-anything/internal/adapter/store"
	"github.com/go-anything/go-anything/internal/domain"
)

func mcpCall(t *testing.T, lines ...string) []map[string]any {
	t.Helper()
	var out bytes.Buffer
	if err := serveMCP(strings.NewReader(strings.Join(lines, "\n")+"\n"), &out); err != nil {
		t.Fatal(err)
	}
	var msgs []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("bad line %q: %v", l, err)
		}
		msgs = append(msgs, m)
	}
	return msgs
}

func TestMCPListsEveryToolItExposes(t *testing.T) {
	msgs := mcpCall(t, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	tools := msgs[0]["result"].(map[string]any)["tools"].([]any)
	names := map[string]bool{}
	for _, tl := range tools {
		names[tl.(map[string]any)["name"].(string)] = true
	}
	for _, want := range []string{"analyze", "verify", "gate", "app_replay", "fuzz", "doctor", "map", "wire_status"} {
		if !names[want] {
			t.Fatalf("tool %s is not exposed; have %v", want, names)
		}
	}
}

func TestMCPFuzzCallAgreesForIdenticalPrograms(t *testing.T) {
	msgs := mcpCall(t,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"fuzz","arguments":{"original":["cat"],"rewrite":["cat"],"n":20,"seed":4}}}`)
	res := msgs[0]["result"].(map[string]any)
	if res["isError"] == true {
		t.Fatalf("identical programs must agree: %+v", res)
	}
	text := res["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "seed 4") {
		t.Fatalf("the seed must be returned for replay: %s", text)
	}
}

func TestMCPFuzzCallReportsDisagreement(t *testing.T) {
	msgs := mcpCall(t,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"fuzz","arguments":{"original":["cat"],"rewrite":["sh","-c","echo wrong"],"n":10,"seed":4}}}`)
	res := msgs[0]["result"].(map[string]any)
	if res["isError"] != true {
		t.Fatalf("a different program must be reported as isError: %+v", res)
	}
}

func TestMCPListsRecordAndBenchTools(t *testing.T) {
	msgs := mcpCall(t, `{"jsonrpc":"2.0","id":9,"method":"tools/list"}`)
	tools := msgs[0]["result"].(map[string]any)["tools"].([]any)
	names := map[string]bool{}
	for _, tl := range tools {
		names[tl.(map[string]any)["name"].(string)] = true
	}
	for _, want := range []string{"record", "bench", "app_record"} {
		if !names[want] {
			t.Fatalf("tool %s is not exposed", want)
		}
	}
}

func TestMCPRecordWritesRecording(t *testing.T) {
	out := t.TempDir() + "/rec.json"
	msgs := mcpCall(t, `{"jsonrpc":"2.0","id":10,"method":"tools/call","params":{"name":"record","arguments":{"out":"`+out+`","argv":["cat"],"inputs":["hello","WORLD"]}}}`)
	res := msgs[0]["result"].(map[string]any)
	if res["isError"] == true {
		t.Fatalf("record failed: %+v", res)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("recording not written: %v", err)
	}
}

func TestMCPBenchRefusesTimingOnMismatch(t *testing.T) {
	dir := t.TempDir()
	rec := dir + "/rec.json"
	mcpCall(t, `{"jsonrpc":"2.0","id":11,"method":"tools/call","params":{"name":"record","arguments":{"out":"`+rec+`","argv":["tr","a-z","A-Z"],"inputs":["hello"]}}}`)
	msgs := mcpCall(t, `{"jsonrpc":"2.0","id":12,"method":"tools/call","params":{"name":"bench","arguments":{"recording":"`+rec+`","original":["tr","a-z","A-Z"],"rewrite":["cat"],"runs":1}}}`)
	if msgs[0]["result"].(map[string]any)["isError"] != true {
		t.Fatal("a rewrite with different output must not yield a valid timing")
	}
}

func TestMCPListsAnalyzeAndLift(t *testing.T) {
	msgs := mcpCall(t, `{"jsonrpc":"2.0","id":20,"method":"tools/list"}`)
	names := map[string]bool{}
	for _, tl := range msgs[0]["result"].(map[string]any)["tools"].([]any) {
		names[tl.(map[string]any)["name"].(string)] = true
	}
	if !names["lift"] {
		t.Fatal("lift must be exposed to MCP")
	}
}

func TestMCPLiftRefusesOriginalAsTarget(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) }))
	defer srv.Close()
	appFile := t.TempDir() + "/app.json"
	rec := domain.AppRecording{Subject: "svc", Cases: []domain.AppCase{{Name: "a", Kind: "http", Method: "GET", Path: "/", Status: 200, ResponseBody: "ok"}}}
	if err := store.SaveApp(appFile, rec); err != nil {
		t.Fatal(err)
	}
	args := `{"app":"` + appFile + `","base":"` + srv.URL + `","original":"` + srv.URL + `","skip_tests":true,"root":"."}`
	msgs := mcpCall(t, `{"jsonrpc":"2.0","id":21,"method":"tools/call","params":{"name":"lift","arguments":`+args+`}}`)
	res := msgs[0]["result"].(map[string]any)
	text := res["content"].([]any)[0].(map[string]any)["text"].(string)
	if res["isError"] != true || !strings.Contains(text, "replay target is the original") {
		t.Fatalf("lift must refuse when base is the original, and say so: %v / %s", res["isError"], text)
	}
}

func TestMCPToolsAreAllInTheRegistry(t *testing.T) {
	msgs := mcpCall(t, `{"jsonrpc":"2.0","id":30,"method":"tools/list"}`)
	registered := map[string]bool{}
	for _, n := range mcpToolNames() {
		registered[n] = true
	}
	for _, tl := range msgs[0]["result"].(map[string]any)["tools"].([]any) {
		name := tl.(map[string]any)["name"].(string)
		if !registered[name] {
			t.Fatalf("MCP tool %s is exposed but not described in the command registry", name)
		}
	}
	if len(mcpToolNames()) != len(msgs[0]["result"].(map[string]any)["tools"].([]any)) {
		t.Fatal("the registry declares MCP tools the server does not expose")
	}
}
