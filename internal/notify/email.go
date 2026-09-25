package notify

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"
)

type emailSender struct{ cfg map[string]string }

func (s emailSender) Send(ctx context.Context, m Message) error {
	c := s.cfg
	for _, v := range []string{m.Title, c["from"], c["to"]} {
		if strings.ContainsAny(v, "\r\n") {
			return errors.New("email: a header contains a line break")
		}
	}
	port := c["port"]
	if port == "" {
		port = "587"
	}
	addr := net.JoinHostPort(c["host"], port)
	var auth smtp.Auth
	if c["username"] != "" {
		auth = smtp.PlainAuth("", c["username"], c["password"], c["host"])
	}
	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s\r\n",
		c["from"], c["to"], m.Title, time.Now().Format(time.RFC1123Z), strings.ReplaceAll(strings.TrimSpace(m.Body+"\n\n"+m.URL), "\n", "\r\n"))
	done := make(chan error, 1)
	go func() { done <- smtp.SendMail(addr, auth, c["from"], []string{c["to"]}, []byte(msg)) }()
	select {
	case err := <-done:
		if err != nil {
			return errors.New("email: could not send the message")
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(30 * time.Second):
		return errors.New("email: timed out")
	}
}
