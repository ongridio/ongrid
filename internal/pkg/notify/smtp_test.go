package notify

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/http/httptest"
	"net/mail"
	"net/textproto"
	"strconv"
	"strings"
	"testing"
	"time"
)

type smtpCapture struct {
	recipients    []string
	message       string
	authenticated bool
}

// 本地 SMTP 协议服务器，真实执行 STARTTLS/TLS；不连接任何外部邮箱。
func smtpFixture(t *testing.T, mode, failure string) (*smtpSender, <-chan smtpCapture) {
	t.Helper()
	certServer := httptest.NewTLSServer(nil)
	cert := certServer.TLS.Certificates[0]
	pool := x509.NewCertPool()
	pool.AddCert(certServer.Certificate())
	certServer.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() }) // 测试清理。
	host, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	sender, err := NewSMTPSender("mail", SMTPConfig{Host: host, Port: port, Username: "test-user", From: "告警 <alert@example.test>", To: []string{"one@example.test", "值班 <two@example.test>"}, TLSMode: mode}, "test-password")
	if err != nil {
		t.Fatal(err)
	}
	s := sender.(*smtpSender)
	s.tlsConfig = &tls.Config{ServerName: host, RootCAs: pool, MinVersion: tls.VersionTLS12}
	captured := make(chan smtpCapture, 1)
	done := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("SMTP fixture did not stop")
		}
	})
	go func() {
		defer close(done)
		defer func() {
			if p := recover(); p != nil {
				t.Errorf("SMTP fixture panic: %v", p)
			}
		}()
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close() // 测试清理。
		if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Error(err)
			return
		}
		if failure == "stall" {
			io.Copy(io.Discard, conn)
			return
		} // 等待取消关闭连接。
		encrypted := mode == "tls"
		if encrypted {
			conn = tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
		}
		reader := bufio.NewReader(conn)
		write := func(line string) bool { _, err := fmt.Fprint(conn, line+"\r\n"); return err == nil }
		if !write("220 localhost ESMTP") {
			return
		}
		var result smtpCapture
		defer func() { captured <- result }()
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimSpace(line)
			switch {
			case strings.HasPrefix(line, "EHLO"):
				if !write("250-localhost") {
					return
				}
				if !encrypted && failure != "no-starttls" {
					if !write("250-STARTTLS") {
						return
					}
				}
				if !write("250 AUTH PLAIN") {
					return
				}
			case line == "STARTTLS":
				if !write("220 ready") {
					return
				}
				secure := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
				if err := secure.Handshake(); err != nil {
					return
				}
				conn = secure
				reader = bufio.NewReader(conn)
				encrypted = true
			case strings.HasPrefix(line, "AUTH PLAIN "):
				if !encrypted {
					t.Error("authentication sent before TLS")
					return
				}
				decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(line, "AUTH PLAIN "))
				if err != nil {
					t.Error(err)
					return
				}
				if string(decoded) != "\x00test-user\x00test-password" {
					t.Error("unexpected authentication")
					return
				}
				if failure == "auth" {
					write("535 authentication failed")
					return
				}
				result.authenticated = true
				write("235 authenticated")
			case strings.HasPrefix(line, "MAIL FROM:"):
				write("250 sender accepted")
			case strings.HasPrefix(line, "RCPT TO:"):
				if failure == "recipient" {
					write("550 recipient rejected")
					return
				}
				result.recipients = append(result.recipients, strings.TrimPrefix(line, "RCPT TO:"))
				write("250 recipient accepted")
			case line == "DATA":
				if !write("354 send message") {
					return
				}
				data, err := textproto.NewReader(reader).ReadDotBytes()
				if err != nil {
					t.Error(err)
					return
				}
				result.message = string(data)
				if failure == "data" {
					write("554 message rejected")
					return
				}
				write("250 queued")
			case line == "QUIT":
				if failure != "quit" {
					write("221 bye")
				}
				return
			default:
				t.Errorf("unexpected SMTP command %q", line)
				return
			}
		}
	}()
	return s, captured
}

func TestSMTPSend(t *testing.T) {
	for _, mode := range []string{"starttls", "tls"} {
		t.Run(mode, func(t *testing.T) {
			sender, captured := smtpFixture(t, mode, "")
			if err := sender.Send(context.Background(), Message{Subject: "主机告警", Body: "CPU 超过阈值\n检查服务", Severity: SeverityCritical, Source: "host"}); err != nil {
				t.Fatal(err)
			}
			result := <-captured
			if !result.authenticated || len(result.recipients) != 2 {
				t.Fatalf("delivery: %+v", result)
			}
			message, err := mail.ReadMessage(strings.NewReader(result.message))
			if err != nil {
				t.Fatal(err)
			}
			subject, err := new(mime.WordDecoder).DecodeHeader(message.Header.Get("Subject"))
			if err != nil || subject != "主机告警" {
				t.Fatalf("subject=%q: %v", subject, err)
			}
			body, err := io.ReadAll(quotedprintable.NewReader(message.Body))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(body), "CPU 超过阈值") || !strings.Contains(string(body), "CRITICAL") {
				t.Fatalf("body: %s", body)
			}
		})
	}
}

func TestSMTPFailures(t *testing.T) {
	for _, failure := range []string{"no-starttls", "auth", "recipient", "data", "untrusted", "stall", "quit"} {
		t.Run(failure, func(t *testing.T) {
			sender, _ := smtpFixture(t, "starttls", failure)
			if failure == "untrusted" {
				sender.tlsConfig = nil
			}
			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
			defer cancel()
			err := sender.Send(ctx, Message{Subject: "alert", Body: "test"})
			if failure == "quit" {
				if err != nil {
					t.Fatalf("accepted DATA must not retry after QUIT failure: %v", err)
				}
			} else if err == nil {
				t.Fatal("expected delivery failure")
			}
		})
	}
}

func TestSMTPValidation(t *testing.T) {
	valid := SMTPConfig{Host: "smtp.example.test", Port: 587, From: "alerts@example.test", To: []string{"ops@example.test"}}
	for _, change := range []func(*SMTPConfig){
		func(c *SMTPConfig) { c.Host = "bad\r\nhost" }, func(c *SMTPConfig) { c.Port = 0 }, func(c *SMTPConfig) { c.Port = 65536 },
		func(c *SMTPConfig) { c.To = nil }, func(c *SMTPConfig) { c.From = "invalid" }, func(c *SMTPConfig) { c.To = []string{"a@example.test\r\nBcc: b@example.test"} }, func(c *SMTPConfig) { c.TLSMode = "plain" },
	} {
		c := valid
		change(&c)
		if _, err := NewSMTPSender("mail", c, ""); err == nil {
			t.Fatalf("accepted invalid config %+v", c)
		}
	}
	sender, err := NewSMTPSender("mail", valid, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sender.(*smtpSender).message(Message{Subject: "alert\r\nBcc: attacker@example.test"}); err == nil {
		t.Fatal("accepted header injection")
	}
}
