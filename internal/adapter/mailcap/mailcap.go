// Package mailcap is a loopback SMTP receiver that records the mail a program sends. Point the program's
// SMTP server at Receiver.Addr during recording or replay. It accepts plain SMTP only (no TLS, no AUTH),
// and binds to loopback only: a receiver reachable from other machines would accept real mail.
package mailcap

import (
	"bufio"
	"fmt"
	"net"
	"net/textproto"
	"strings"
	"sync"

	"github.com/go-anything/go-anything/internal/domain"
)

// maxMessage caps one message, so a runaway sender cannot exhaust memory.
const maxMessage = 1 << 20

// volatileHeaders change on every send (time and a unique id) and are not behavior.
var volatileHeaders = []string{"date:", "message-id:"}

// Receiver collects the messages sent to it until they are drained.
type Receiver struct {
	Addr string

	mu      sync.Mutex
	pending []domain.Effect
	ln      net.Listener
	done    chan struct{}
}

// Start listens on a loopback address. A non-loopback address is refused.
func Start(addr string) (*Receiver, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("mailcap: address %q: %w", addr, err)
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return nil, fmt.Errorf("mailcap: %q is not a loopback address; refusing to listen on it", addr)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("mailcap: listen: %w", err)
	}
	r := &Receiver{Addr: ln.Addr().String(), ln: ln, done: make(chan struct{})}
	go r.serve()
	return r, nil
}

func (r *Receiver) serve() {
	for {
		conn, err := r.ln.Accept()
		if err != nil {
			close(r.done)
			return
		}
		go r.session(conn)
	}
}

// Close stops accepting connections.
func (r *Receiver) Close() error {
	err := r.ln.Close()
	<-r.done
	return err
}

// Drain returns every message received since the last drain, in order, and clears them.
func (r *Receiver) Drain() []domain.Effect {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.pending
	r.pending = nil
	return out
}

func (r *Receiver) session(conn net.Conn) {
	defer conn.Close()
	tp := textproto.NewConn(conn)
	defer tp.Close()
	tp.PrintfLine("220 go-anything mail receiver")
	var from string
	var to []string
	for {
		line, err := tp.ReadLine()
		if err != nil {
			return
		}
		cmd := strings.ToUpper(strings.Fields(line + " ")[0])
		switch {
		case cmd == "EHLO" || cmd == "HELO":
			tp.PrintfLine("250 go-anything")
		case cmd == "MAIL" && strings.HasPrefix(strings.ToUpper(line), "MAIL FROM:"):
			from = address(line[len("MAIL FROM:"):])
			to = nil
			tp.PrintfLine("250 OK")
		case cmd == "RCPT" && strings.HasPrefix(strings.ToUpper(line), "RCPT TO:"):
			if from == "" {
				tp.PrintfLine("503 MAIL first")
				continue
			}
			to = append(to, address(line[len("RCPT TO:"):]))
			tp.PrintfLine("250 OK")
		case cmd == "DATA":
			if len(to) == 0 {
				tp.PrintfLine("503 RCPT first")
				continue
			}
			tp.PrintfLine("354 end with <CRLF>.<CRLF>")
			body, err := tp.ReadDotBytes()
			if err != nil {
				return
			}
			if len(body) > maxMessage {
				tp.PrintfLine("552 message too large")
				return
			}
			r.mu.Lock()
			r.pending = append(r.pending, domain.Effect{Kind: domain.EffectMail,
				Method: from, Path: strings.Join(to, ","), Body: normalize(string(body))})
			r.mu.Unlock()
			tp.PrintfLine("250 OK")
			from, to = "", nil
		case cmd == "RSET":
			from, to = "", nil
			tp.PrintfLine("250 OK")
		case cmd == "NOOP":
			tp.PrintfLine("250 OK")
		case cmd == "QUIT":
			tp.PrintfLine("221 bye")
			return
		default:
			tp.PrintfLine("502 command not supported")
		}
	}
}

// address extracts the address from "<user@host>" (with optional parameters after it).
func address(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, ">"); strings.HasPrefix(s, "<") && i > 0 {
		return s[1:i]
	}
	return strings.Fields(s + " ")[0]
}

// normalize drops the headers that change on every send, so two sends of the same mail compare equal.
func normalize(msg string) string {
	var out []string
	skipping := false
	sc := bufio.NewScanner(strings.NewReader(msg))
	inHeaders := true
	for sc.Scan() {
		line := sc.Text()
		if inHeaders && line == "" {
			inHeaders = false
		}
		if inHeaders {
			lower := strings.ToLower(line)
			skipping = false
			for _, h := range volatileHeaders {
				if strings.HasPrefix(lower, h) {
					skipping = true
				}
			}
			if skipping {
				continue
			}
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}
