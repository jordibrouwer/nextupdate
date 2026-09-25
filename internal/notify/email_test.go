package notify

import (
	"bufio"
	"context"
	"net"
	"strings"
	"testing"
)

// fakeSMTP accepts one message and returns what it received.
func fakeSMTP(t *testing.T) (addr string, got chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	got = make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		r := bufio.NewReader(conn)
		w := func(s string) { conn.Write([]byte(s + "\r\n")) }
		w("220 fake ESMTP")
		var data strings.Builder
		inData := false
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			if inData {
				if line == "." {
					inData = false
					w("250 queued")
					continue
				}
				data.WriteString(line + "\n")
				continue
			}
			switch cmd := strings.ToUpper(line); {
			case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
				w("250 fake")
			case strings.HasPrefix(cmd, "MAIL"), strings.HasPrefix(cmd, "RCPT"):
				w("250 ok")
			case cmd == "DATA":
				inData = true
				w("354 go ahead")
			case cmd == "QUIT":
				w("221 bye")
				got <- data.String()
				return
			default:
				w("250 ok")
			}
		}
	}()
	return ln.Addr().String(), got
}

func TestEmail(t *testing.T) {
	addr, got := fakeSMTP(t)
	host, port, _ := net.SplitHostPort(addr)
	s, err := Build("email", map[string]string{"host": host, "port": port, "from": "nextupdate@home.lan", "to": "jordi@example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send(context.Background(), Message{Title: "Update available: app", Body: "1.0.0 to 2.0.0", URL: "https://nu.example"}); err != nil {
		t.Fatal(err)
	}
	mail := <-got
	for _, want := range []string{"From: nextupdate@home.lan", "To: jordi@example.com", "Subject: Update available: app", "1.0.0 to 2.0.0", "https://nu.example"} {
		if !strings.Contains(mail, want) {
			t.Errorf("mail lacks %q:\n%s", want, mail)
		}
	}
}

func TestEmailRejectsHeaderInjection(t *testing.T) {
	s, _ := Build("email", map[string]string{"host": "127.0.0.1", "port": "1", "from": "a@b.c", "to": "d@e.f"}, nil)
	err := s.Send(context.Background(), Message{Title: "hi\r\nBcc: evil@x.y", Body: "b"})
	if err == nil || !strings.Contains(err.Error(), "line break") {
		t.Fatalf("want a line-break error, got %v", err)
	}
}
