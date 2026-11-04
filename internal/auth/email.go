package auth

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"net/smtp"
	"os"
	"strconv"
	"strings"
	"time"
)

type Mailer struct {
	Host, Port, User, Pass, From, AppName string
	// Secure selects implicit TLS (port 465). Otherwise STARTTLS is used when offered.
	Secure bool
}

func MailerFromEnv() *Mailer {
	m := &Mailer{
		Host: os.Getenv("SMTP_HOST"), Port: os.Getenv("SMTP_PORT"), User: os.Getenv("SMTP_USER"),
		Pass: os.Getenv("SMTP_PASS"), From: os.Getenv("SMTP_FROM"), AppName: "Estro",
	}
	if m.Port == "" {
		m.Port = "587"
	}
	secure, err := strconv.ParseBool(os.Getenv("SMTP_SECURE"))
	m.Secure = (err == nil && secure) || (err != nil && m.Port == "465")
	if m.From == "" {
		m.From = "Estro <noreply@localhost>"
	}
	if m.User == "" {
		m.User = m.fromAddress()
	}
	return m
}

func (m *Mailer) fromAddress() string {
	from := m.From
	if i := strings.Index(from, "<"); i >= 0 {
		from = strings.Trim(from[i:], "<>")
	}
	return strings.TrimSpace(from)
}

// sendTLS speaks SMTP over an implicit TLS connection (SMTPS, port 465).
func (m *Mailer) sendTLS(from string, to []string, msg []byte) error {
	addr := m.Host + ":" + m.Port
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 8 * time.Second}, "tcp", addr, &tls.Config{ServerName: m.Host})
	if err != nil {
		return err
	}
	defer conn.Close()
	client, err := smtp.NewClient(conn, m.Host)
	if err != nil {
		return err
	}
	defer client.Close()
	if m.Pass != "" {
		if err := client.Auth(smtp.PlainAuth("", m.User, m.Pass, m.Host)); err != nil {
			return err
		}
	}
	if err := client.Mail(from); err != nil {
		return err
	}
	for _, rcpt := range to {
		if err := client.Rcpt(rcpt); err != nil {
			return err
		}
	}
	wc, err := client.Data()
	if err != nil {
		return err
	}
	if _, err := wc.Write(msg); err != nil {
		return err
	}
	if err := wc.Close(); err != nil {
		return err
	}
	return client.Quit()
}

// Enabled needs a host and a password: a host alone (the shipped default)
// would otherwise block registration on a failed unauthenticated send.
func (m *Mailer) Enabled() bool { return m.Host != "" && m.Pass != "" }

// Send delivers a plain-text mail. Without SMTP configured, the body is logged so
// local development can complete the verification flow from the server log.
func (m *Mailer) Send(to, subject, body string) error {
	if !m.Enabled() {
		slog.Info("mail (dev mode, not sent)", "to", to, "subject", subject, "body", body)
		return nil
	}
	msg := strings.Join([]string{
		"From: " + m.From, "To: " + to, "Subject: " + subject,
		"MIME-Version: 1.0", "Content-Type: text/plain; charset=utf-8", "", body,
	}, "\r\n")
	from := m.fromAddress()
	if m.Secure {
		return m.sendTLS(from, []string{to}, []byte(msg))
	}
	var auth smtp.Auth
	if m.Pass != "" {
		auth = smtp.PlainAuth("", m.User, m.Pass, m.Host)
	}
	return smtp.SendMail(m.Host+":"+m.Port, auth, from, []string{to}, []byte(msg))
}

func (m *Mailer) SendVerification(to, code string) error {
	return m.Send(to, m.AppName+" verification code",
		fmt.Sprintf("Your %s verification code is %s\n\nIt expires in 15 minutes.", m.AppName, code))
}

func (m *Mailer) SendPasswordReset(to, code string) error {
	return m.Send(to, m.AppName+" password reset",
		fmt.Sprintf("Your %s password reset code is %s\n\nIt expires in 15 minutes. Ignore this if you did not request a reset.", m.AppName, code))
}

// CanSendEmail rate-limits outbound mail per address and per IP.
func (s *Store) CanSendEmail(ctx context.Context, email, ip string) bool {
	var perEmail, perIP int
	s.DB.QueryRow(ctx, `SELECT count(*) FROM email_log WHERE email=$1 AND created > NOW() - interval '1 hour'`, email).Scan(&perEmail)
	s.DB.QueryRow(ctx, `SELECT count(*) FROM email_log WHERE ip=$1 AND created > NOW() - interval '1 hour'`, ip).Scan(&perIP)
	return perEmail < 5 && perIP < 20
}

func (s *Store) LogEmail(ctx context.Context, email, ip, kind string) {
	s.DB.Exec(ctx, `INSERT INTO email_log (email, ip, kind) VALUES ($1,$2,$3)`, email, ip, kind)
}

const codeLifetime = 15 * time.Minute
