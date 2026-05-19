package auth

import (
	"bytes"
	"fmt"
	"mime/quotedprintable"
	"net/smtp"
	"strings"
)

// Mailer sends transactional email over SMTP with STARTTLS.
type Mailer struct {
	host string
	port string
	user string
	pass string
	from string
}

// NewMailer creates a new Mailer.
func NewMailer(host, port, user, pass, from string) *Mailer {
	return &Mailer{
		host: host,
		port: port,
		user: user,
		pass: pass,
		from: from,
	}
}

// Send sends a minimal RFC 2822 email with a quoted-printable HTML body.
func (m *Mailer) Send(to, subject, bodyHTML string) error {
	auth := smtp.PlainAuth("", m.user, m.pass, m.host)

	var buf bytes.Buffer
	buf.WriteString("From: " + m.from + "\r\n")
	buf.WriteString("To: " + to + "\r\n")
	buf.WriteString("Subject: " + subject + "\r\n")
	buf.WriteString("MIME-Version: 1.0\r\n")
	buf.WriteString("Content-Type: text/html; charset=UTF-8\r\n")
	buf.WriteString("Content-Transfer-Encoding: quoted-printable\r\n")
	buf.WriteString("\r\n")

	qpWriter := quotedprintable.NewWriter(&buf)
	if _, err := qpWriter.Write([]byte(bodyHTML)); err != nil {
		return fmt.Errorf("email: encode body: %w", err)
	}
	if err := qpWriter.Close(); err != nil {
		return fmt.Errorf("email: close qp writer: %w", err)
	}

	addr := m.host + ":" + m.port
	if err := smtp.SendMail(addr, auth, m.from, []string{to}, buf.Bytes()); err != nil {
		return fmt.Errorf("email: send: %w", err)
	}
	return nil
}

// SendInvite sends the invite enrollment email to the given address.
func SendInvite(m *Mailer, to, enrollURL string) error {
	subject := "You've been invited to IssuesSyncServer"

	// Escape any special HTML in the URL just in case.
	safeURL := strings.ReplaceAll(enrollURL, "&", "&amp;")
	body := fmt.Sprintf(
		`<html><body>
<p>You have been invited to IssuesSyncServer.</p>
<p>Click the link below to complete your enrollment and register a passkey.
This link is valid for 24 hours and can only be used once.</p>
<p><a href="%s">%s</a></p>
</body></html>`,
		safeURL, safeURL,
	)

	return m.Send(to, subject, body)
}
