package capture

import (
	"net/http"
	"strings"
	"testing"
)

func TestReceiverRecordsCallsInOrderAndDrains(t *testing.T) {
	r, err := Start()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for _, body := range []string{"first", "second"} {
		resp, err := http.Post(r.URL+"/hook", "text/plain", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	got := r.Drain()
	if len(got) != 2 || got[0].Body != "first" || got[1].Body != "second" {
		t.Fatalf("calls must be kept in order: %+v", got)
	}
	if left := r.Drain(); len(left) != 0 {
		t.Fatalf("drain must clear the pending calls: %+v", left)
	}
}

func TestReceiverBindsLoopbackOnly(t *testing.T) {
	r, err := Start()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if !strings.HasPrefix(r.URL, "http://127.0.0.1:") {
		t.Fatalf("receiver must bind to loopback, got %s", r.URL)
	}
}

func TestReceiverRecordsMethodAndPath(t *testing.T) {
	r, _ := Start()
	defer r.Close()
	req, _ := http.NewRequest(http.MethodPut, r.URL+"/a/b", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	got := r.Drain()
	if len(got) != 1 || got[0].Method != "PUT" || got[0].Path != "/a/b" {
		t.Fatalf("method and path must be kept: %+v", got)
	}
}

func TestStartAtRefusesNonLoopback(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:0", "192.168.1.5:0", ":0"} {
		if r, err := StartAt(addr); err == nil {
			r.Close()
			t.Fatalf("%s must be refused", addr)
		}
	}
}

func TestStartAtAcceptsLoopbackAddress(t *testing.T) {
	r, err := StartAt("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	r.Close()
}
