package notify

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// SMTPConfig 是邮件渠道的非敏感配置；密码单独传入，不参与 API 回显。
type SMTPConfig struct {
	Host     string   `json:"host"`
	Port     int      `json:"port"`
	Username string   `json:"username"`
	From     string   `json:"from"`
	To       []string `json:"to"`
	TLSMode  string   `json:"tls_mode"`
}

// Validate 拒绝无效地址和头部注入，只允许经过证书验证的加密传输。
func (c SMTPConfig) Validate() error {
	if strings.TrimSpace(c.Host) == "" || strings.ContainsAny(c.Host, "\r\n /\\\t") {
		return fmt.Errorf("smtp: invalid host")
	}
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("smtp: port must be between 1 and 65535")
	}
	if c.TLSMode != "" && c.TLSMode != "starttls" && c.TLSMode != "tls" {
		return fmt.Errorf("smtp: tls_mode must be starttls or tls")
	}
	if strings.ContainsAny(c.Username, "\r\n\x00") {
		return fmt.Errorf("smtp: invalid username")
	}
	if len(c.To) == 0 || len(c.To) > 100 {
		return fmt.Errorf("smtp: provide between 1 and 100 recipients")
	}
	for _, value := range append([]string{c.From}, c.To...) {
		if strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("smtp: invalid email address")
		}
		address, err := mail.ParseAddress(value)
		if err != nil || !strings.Contains(address.Address, "@") || strings.ContainsAny(address.Address, "\r\n") {
			return fmt.Errorf("smtp: invalid email address")
		}
	}
	return nil
}

type smtpSender struct {
	name      string
	config    SMTPConfig
	password  string
	tlsConfig *tls.Config
}

// NewSMTPSender 构造 SMTP 发送器；默认强制 STARTTLS，不回退到明文连接。
func NewSMTPSender(name string, config SMTPConfig, password string) (Sender, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	config.To = append([]string(nil), config.To...)
	return &smtpSender{name: name, config: config, password: password}, nil
}
func (s *smtpSender) Name() string { return s.name }

func (s *smtpSender) Send(ctx context.Context, msg Message) error {
	payload, err := s.message(msg)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(s.config.Host, strconv.Itoa(s.config.Port)))
	if err != nil {
		return fmt.Errorf("smtp: connect: %w", err)
	}
	// 关闭连接也负责打断 smtp.Client 不支持 context 的读写操作。
	defer conn.Close() // 最佳努力清理，实际投递错误由下方返回。
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		return fmt.Errorf("smtp: deadline: %w", err)
	}
	tlsConfig := &tls.Config{ServerName: s.config.Host, MinVersion: tls.VersionTLS12}
	if s.tlsConfig != nil {
		tlsConfig = s.tlsConfig.Clone()
	} // 测试可注入本地 CA，生产使用系统信任链。
	connection := conn
	if s.config.TLSMode == "tls" {
		secured := tls.Client(conn, tlsConfig)
		if err := secured.HandshakeContext(ctx); err != nil {
			return fmt.Errorf("smtp: TLS: %w", err)
		}
		connection = secured
	}
	client, err := smtp.NewClient(connection, s.config.Host)
	if err != nil {
		return fmt.Errorf("smtp: greeting: %w", err)
	}
	defer client.Close() // 连接清理失败不覆盖已完成的投递结果。
	if s.config.TLSMode != "tls" {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return fmt.Errorf("smtp: server does not support STARTTLS")
		}
		if err := client.StartTLS(tlsConfig); err != nil {
			return fmt.Errorf("smtp: STARTTLS required: %w", err)
		}
	}
	if s.config.Username != "" {
		if err := client.Auth(smtp.PlainAuth("", s.config.Username, s.password, s.config.Host)); err != nil {
			return fmt.Errorf("smtp: authentication: %w", err)
		}
	}
	return s.deliver(ctx, client, payload)
}

func (s *smtpSender) deliver(ctx context.Context, client *smtp.Client, payload []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	from, err := mail.ParseAddress(s.config.From)
	if err != nil {
		return err
	}
	if err := client.Mail(from.Address); err != nil {
		return fmt.Errorf("smtp: sender rejected: %w", err)
	}
	for _, value := range s.config.To {
		to, err := mail.ParseAddress(value)
		if err != nil {
			return err
		}
		if err := client.Rcpt(to.Address); err != nil {
			return fmt.Errorf("smtp: recipient rejected: %w", err)
		}
	}
	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp: DATA: %w", err)
	}
	if _, err := writer.Write(payload); err != nil {
		return fmt.Errorf("smtp: body: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("smtp: message rejected: %w", err)
	}
	// DATA 已被服务端确认接收；QUIT 失败不能触发重试而造成重复邮件。
	if err := client.Quit(); err != nil {
		return nil
	}
	return nil
}

func (s *smtpSender) message(msg Message) ([]byte, error) {
	if strings.ContainsAny(msg.Subject, "\r\n") {
		return nil, fmt.Errorf("smtp: subject must not contain newlines")
	}
	from, err := mail.ParseAddress(s.config.From)
	if err != nil {
		return nil, err
	}
	recipients := make([]string, 0, len(s.config.To))
	for _, value := range s.config.To {
		a, err := mail.ParseAddress(value)
		if err != nil {
			return nil, err
		}
		recipients = append(recipients, a.String())
	}
	var out bytes.Buffer
	fmt.Fprintf(&out, "From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n", from.String(), strings.Join(recipients, ",\r\n "), mime.QEncoding.Encode("UTF-8", msg.Subject), time.Now().Format(time.RFC1123Z))
	writer := quotedprintable.NewWriter(&out)
	if _, err := writer.Write([]byte(formatText(msg))); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
