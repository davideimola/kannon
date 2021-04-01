package mailer

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"time"

	log "github.com/sirupsen/logrus"
	"gopkg.in/mail.v2"
	"gorm.io/gorm"
	"kannon.gyozatech.dev/internal/db"
	"kannon.gyozatech.dev/internal/dkim"
	"kannon.gyozatech.dev/internal/smtp"
)

type headers map[string]string

type smtpMailer struct {
	Sender  smtp.Sender
	headers headers
	db      *gorm.DB
}

type sendData struct {
	From      string
	Sender    string
	To        string
	Subject   string
	Domain    string
	Params    map[string]interface{}
	MessageID string
}

func (m *smtpMailer) Send(email db.SendingPoolEmail) error {
	err := m.sendEmail(email)
	if err != nil {
		email.Error = err.Error()
		email.Status = db.SendingPoolStatusError
	} else {
		email.Status = db.SendingPoolStatusSent
	}
	return m.db.Save(&email).Error
}

func (m *smtpMailer) sendEmail(email db.SendingPoolEmail) error {
	pool := db.SendingPool{
		ID: email.SendingPoolID,
	}
	err := m.db.Where(&pool).First(&pool).Error
	if err != nil {
		return err
	}

	var domain db.Domain
	err = m.db.Find(&domain, "domain = ?", pool.Domain).Error
	if err != nil {
		return err
	}

	var template db.Template
	err = m.db.Find(&template, "template_id = ?", pool.TemplateID).Error
	if err != nil {
		return err
	}
	data := sendData{
		From:      pool.Sender.Email,
		Sender:    pool.Sender.GetSender(),
		To:        email.To,
		Subject:   pool.Subject,
		Domain:    domain.Domain,
		MessageID: pool.MessageID,
	}

	msg, err := m.prepareMessage(data, template.HTML)
	if err != nil {
		return err
	}

	signData := dkim.SignData{
		PrivateKey: domain.DKIMKeys.PrivateKey,
		Domain:     domain.Domain,
		Selector:   "smtp",
		Headers:    []string{"From", "To", "Subject", "Message-ID"},
	}

	signedMsg, err := dkim.SignMessage(signData, bytes.NewReader(msg))
	if err != nil {
		return err
	}

	emailBase64 := base64.URLEncoding.EncodeToString([]byte(data.To))
	returnPath := fmt.Sprintf("bump_%v-%v", emailBase64, pool.MessageID)

	err = m.Sender.Send(returnPath, data.To, signedMsg)
	if err != nil {
		return err
	}

	return nil
}

func (m *smtpMailer) prepareMessage(data sendData, html string) ([]byte, error) {
	emailBase64 := base64.URLEncoding.EncodeToString([]byte(data.To))

	headers := headers(m.headers)
	headers["Subject"] = data.Subject
	headers["From"] = data.Sender
	headers["To"] = data.To
	headers["Message-ID"] = fmt.Sprintf("<%v/%v>", emailBase64, data.MessageID)
	headers["X-Pool-Message-ID"] = data.MessageID
	return renderMsg(html, data.From, data.To, headers)
}

// NewSMTPMailer creates an SMTP mailer
func NewSMTPMailer(sender smtp.Sender, db *gorm.DB) Mailer {
	return &smtpMailer{
		Sender: sender,
		db:     db,
		headers: headers{
			"X-Mailer": "SMTP Mailer",
		},
	}
}

// ToEmailMsg render a MsgPayload to an SMTP message
func renderMsg(html string, from, to string, headers headers) ([]byte, error) {
	msg := mail.NewMessage()

	for key, value := range headers {
		msg.SetHeader(key, value)
	}
	msg.SetDateHeader("Date", time.Now())
	msg.SetBody("text/html", html)

	var buff bytes.Buffer
	if _, err := msg.WriteTo(&buff); err != nil {
		log.Warnf("🤢 Error writing message: %v\n", err)
		return nil, err
	}

	return buff.Bytes(), nil
}
