package mailcap

import (
	"net/smtp"
	"strings"
	"testing"
)

func send(t *testing.T, addr, from string, to []string, msg string) {
	t.Helper()
	if err := smtp.SendMail(addr, nil, from, to, []byte(msg)); err != nil {
		t.Fatal(err)
	}
}

func TestReceiverRecordsSenderRecipientsAndBody(t *testing.T) {
	r, err := Start("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	send(t, r.Addr, "shop@example.com", []string{"buyer@example.com"},
		"Subject: Order 1\r\n\r\nYour order is paid.\r\n")
	got := r.Drain()
	if len(got) != 1 || got[0].Method != "shop@example.com" || got[0].Path != "buyer@example.com" {
		t.Fatalf("sender and recipient must be recorded: %+v", got)
	}
	if !strings.Contains(got[0].Body, "Your order is paid.") {
		t.Fatalf("body must be recorded: %q", got[0].Body)
	}
}

func TestVolatileHeadersAreNotBehavior(t *testing.T) {
	r, _ := Start("127.0.0.1:0")
	defer r.Close()
	send(t, r.Addr, "a@x", []string{"b@x"}, "Date: Mon, 1 Jan 2001 00:00:00 +0000\r\nMessage-ID: <1@x>\r\nSubject: s\r\n\r\nhi\r\n")
	send(t, r.Addr, "a@x", []string{"b@x"}, "Date: Tue, 2 Jan 2001 11:11:11 +0000\r\nMessage-ID: <2@x>\r\nSubject: s\r\n\r\nhi\r\n")
	got := r.Drain()
	if len(got) != 2 || got[0].Body != got[1].Body {
		t.Fatalf("the same mail sent twice must have identical behavior: %+v", got)
	}
}

func TestDrainClears(t *testing.T) {
	r, _ := Start("127.0.0.1:0")
	defer r.Close()
	send(t, r.Addr, "a@x", []string{"b@x"}, "Subject: s\r\n\r\nx\r\n")
	r.Drain()
	if left := r.Drain(); len(left) != 0 {
		t.Fatal("a second drain must be empty")
	}
}

func TestRefusesNonLoopback(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:0", "10.1.2.3:25", ":25"} {
		if r, err := Start(addr); err == nil {
			r.Close()
			t.Fatalf("%s must be refused", addr)
		}
	}
}
