package notify

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/smtp"
	"sync"
	"testing"
	"text/template"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/umputun/remark42/backend/app/store"
)

func TestEmailNew(t *testing.T) {
	emailParams := EmailParams{
		From:                     "test@from",
		VerificationTemplatePath: "testdata/verification.html.tmpl",
		MsgTemplatePath:          "testdata/msg.html.tmpl",
	}
	smtpParams := SMTPParams{
		Host:     "test@host",
		Port:     1000,
		TLS:      true,
		Username: "test@username",
		Password: "test@password",
		TimeOut:  time.Second,
	}

	email, err := NewEmail(emailParams, smtpParams)

	assert.NoError(t, err)
	assert.NotNil(t, email, "email returned")

	assert.NotNil(t, email.msgTmpl, "e.template is set")
	assert.Equal(t, emailParams.From, email.EmailParams.From, "emailParams.From unchanged after creation")
	if smtpParams.TimeOut == 0 {
		assert.Equal(t, defaultEmailTimeout, email.TimeOut, "empty emailParams.TimeOut changed to default")
	} else {
		assert.Equal(t, smtpParams.TimeOut, email.TimeOut, "emailParams.TimOut unchanged after creation")
	}
	assert.Equal(t, smtpParams.Host, email.Host, "emailParams.Host unchanged after creation")
	assert.Equal(t, smtpParams.Username, email.Username, "emailParams.Username unchanged after creation")
	assert.Equal(t, smtpParams.Password, email.Password, "emailParams.Password unchanged after creation")
	assert.Equal(t, smtpParams.Port, email.Port, "emailParams.Port unchanged after creation")
	assert.Equal(t, smtpParams.TLS, email.TLS, "emailParams.TLS unchanged after creation")
}

func Test_initTemplates(t *testing.T) {
	testSet := []struct {
		name        string
		errText     string
		emailParams EmailParams
	}{
		{
			name:    "with wrong path to verification template",
			errText: "can't read verification template: open notfount.tmpl: no such file or directory",
			emailParams: EmailParams{
				VerificationTemplatePath: "notfount.tmpl",
				MsgTemplatePath:          "testdata/msg.html.tmpl",
			},
		},
		{
			name:    "with wrong path to message template",
			errText: "can't read message template: open notfount.tmpl: no such file or directory",
			emailParams: EmailParams{
				VerificationTemplatePath: "testdata/verification.html.tmpl",
				MsgTemplatePath:          "notfount.tmpl",
			},
		},
		{
			name:    "with error on read verification template",
			errText: "can't parse verification template: template: verifyTmpl:1: unexpected unclosed action in command",
			emailParams: EmailParams{
				VerificationTemplatePath: "testdata/bad.html.tmpl",
				MsgTemplatePath:          "testdata/msg.html.tmpl",
			},
		},
		{
			name:    "with error on read message template",
			errText: "can't parse message template: template: msgTmpl:1: unexpected unclosed action in command",
			emailParams: EmailParams{
				VerificationTemplatePath: "testdata/verification.html.tmpl",
				MsgTemplatePath:          "testdata/bad.html.tmpl",
			},
		},
	}

	for _, d := range testSet {
		d := d
		t.Run(d.name, func(t *testing.T) {
			e := Email{EmailParams: d.emailParams}
			err := e.setTemplates()

			assert.EqualError(t, err, d.errText)
		})
	}
}

func TestEmailSendErrors(t *testing.T) {
	var err error
	e := Email{}
	e.TokenGenFn = TokenGenFn

	e.verifyTmpl, err = template.New("test").Parse("{{.Test}}")
	assert.NoError(t, err)
	assert.EqualError(t, e.Send(context.Background(), Request{Email: "bad@example.org", Verification: VerificationMetadata{Token: "some"}}),
		"error executing template to build verification message: template: test:1:2: executing \"test\" at <.Test>: can't evaluate field Test in type notify.verifyTmplData")

	e.msgTmpl, err = template.New("test").Parse("{{.Test}}")
	assert.NoError(t, err)
	assert.EqualError(t, e.Send(context.Background(), Request{Comment: store.Comment{ID: "999"}, parent: store.Comment{User: store.User{ID: "test"}}, Email: "bad@example.org"}),
		"error executing template to build comment reply message: template: test:1:2: executing \"test\" at <.Test>: can't evaluate field Test in type notify.msgTmplData")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.EqualError(t, e.Send(ctx, Request{Comment: store.Comment{ID: "999"}, parent: store.Comment{User: store.User{ID: "test"}}, Email: "bad@example.org"}),
		"sending message to \"bad@example.org\" aborted due to canceled context")

	e.smtp = &fakeTestSMTP{}
	assert.EqualError(t, e.Send(context.Background(), Request{Comment: store.Comment{ID: "999"}, parent: store.Comment{User: store.User{ID: "error"}}, Email: "bad@example.org"}),
		"error creating token for unsubscribe link: token generation error")
}

func TestEmailSend_ExitConditions(t *testing.T) {
	email, err := NewEmail(EmailParams{
		VerificationTemplatePath: "testdata/verification.html.tmpl",
		MsgTemplatePath:          "testdata/msg.html.tmpl",
	}, SMTPParams{})
	assert.NoError(t, err)
	assert.NotNil(t, email, "expecting email returned")
	// prevent triggering e.autoFlush creation
	emptyRequest := Request{Comment: store.Comment{ID: "999"}}
	assert.NoError(t, email.Send(context.Background(), emptyRequest),
		"Message without parent comment User.Email is not sent and returns nil")
	requestWithEqualUsersWithEmails := Request{Comment: store.Comment{ID: "999"}, Email: "good_example@example.org"}
	assert.NoError(t, email.Send(context.Background(), requestWithEqualUsersWithEmails),
		"Message with parent comment User equals comment User is not sent and returns nil")
}

func TestEmailSendClientError(t *testing.T) {
	var testSet = []struct {
		name string
		smtp *fakeTestSMTP
		err  string
	}{
		{name: "failed to verify receiver", smtp: &fakeTestSMTP{fail: map[string]bool{"mail": true}},
			err: "bad from address \"\": failed to verify sender"},
		{name: "failed to verify sender", smtp: &fakeTestSMTP{fail: map[string]bool{"rcpt": true}},
			err: "bad to address \"\": failed to verify receiver"},
		{name: "failed to close connection", smtp: &fakeTestSMTP{fail: map[string]bool{"quit": true, "close": true}}},
		{name: "failed to make email writer", smtp: &fakeTestSMTP{fail: map[string]bool{"data": true}},
			err: "can't make email writer: failed to send"},
	}
	for _, d := range testSet {
		d := d
		t.Run(d.name, func(t *testing.T) {
			e := Email{smtp: d.smtp}
			if d.err != "" {
				assert.EqualError(t, e.sendMessage(emailMessage{}), d.err,
					"expected error for e.sendMessage")
			} else {
				assert.NoError(t, e.sendMessage(emailMessage{}),
					"expected no error for e.sendMessage")
			}
		})
	}
	e := Email{}
	e.smtp = nil
	assert.Error(t, e.sendMessage(emailMessage{}),
		"nil e.smtp should return error")
	e.smtp = &fakeTestSMTP{}
	assert.NoError(t, e.sendMessage(emailMessage{}), "",
		"no error expected for e.sendMessage in normal flow")
	e.smtp = &fakeTestSMTP{fail: map[string]bool{"quit": true}}
	assert.NoError(t, e.sendMessage(emailMessage{}), "",
		"no error expected for e.sendMessage with failed smtpClient.Quit but successful smtpClient.Close")
	e.smtp = &fakeTestSMTP{fail: map[string]bool{"create": true}}
	assert.EqualError(t, e.sendMessage(emailMessage{}), "failed to make smtp Create: failed to create client",
		"e.send called without smtpClient set returns error")
}

func TestEmail_Send(t *testing.T) {
	email, err := NewEmail(EmailParams{
		From:                     "from@example.org",
		VerificationTemplatePath: "testdata/verification.html.tmpl",
		MsgTemplatePath:          "testdata/msg.html.tmpl",
	}, SMTPParams{})
	assert.NoError(t, err)
	assert.NotNil(t, email)
	fakeSMTP := fakeTestSMTP{}
	email.smtp = &fakeSMTP
	email.TokenGenFn = TokenGenFn
	email.UnsubscribeURL = "https://remark42.com/api/v1/email/unsubscribe"
	req := Request{
		Comment: store.Comment{ID: "999", User: store.User{ID: "1", Name: "test_user"}, ParentID: "1", PostTitle: "test_title"},
		parent:  store.Comment{ID: "1", User: store.User{ID: "999", Name: "parent_user"}},
		Email:   "test@example.org",
	}
	assert.NoError(t, email.Send(context.TODO(), req))
	assert.Equal(t, "from@example.org", fakeSMTP.readMail())
	assert.Equal(t, 1, fakeSMTP.readQuitCount())
	assert.Equal(t, "test@example.org", fakeSMTP.readRcpt())
	// test buildMessageFromRequest separately for message text
	res, err := email.buildMessageFromRequest(req, req.ForAdmin)
	assert.NoError(t, err)
	assert.Contains(t, res, `From: from@example.org
To: test@example.org
Subject: New reply to your comment for "test_title"
Content-Transfer-Encoding: quoted-printable
MIME-version: 1.0
Content-Type: text/html; charset="UTF-8"
List-Unsubscribe-Post: List-Unsubscribe=One-Click
List-Unsubscribe: <https://remark42.com/api/v1/email/unsubscribe?site=&tkn=token>
Date: `)

	// send email to admin without parent set
	req = Request{
		Comment:  store.Comment{ID: "999", User: store.User{ID: "1", Name: "test_user"}, PostTitle: "test_title"},
		Email:    "admin@example.org",
		ForAdmin: true,
	}
	assert.NoError(t, email.Send(context.TODO(), req))
	res, err = email.buildMessageFromRequest(req, req.ForAdmin)
	assert.NoError(t, err)
	assert.Contains(t, res, `From: from@example.org
To: admin@example.org
Subject: New comment to your site for "test_title"
Content-Transfer-Encoding: quoted-printable
MIME-version: 1.0
Content-Type: text/html; charset="UTF-8"
Date: `)
}

func TestEmail_SendVerification(t *testing.T) {
	email, err := NewEmail(EmailParams{
		From:                     "from@example.org",
		VerificationTemplatePath: "testdata/verification.html.tmpl",
		MsgTemplatePath:          "testdata/msg.html.tmpl",
	}, SMTPParams{})
	assert.NoError(t, err)
	assert.NotNil(t, email)
	fakeSMTP := fakeTestSMTP{}
	email.smtp = &fakeSMTP
	email.TokenGenFn = TokenGenFn
	req := Request{
		Email: "test@example.org",
		Verification: VerificationMetadata{
			SiteID: "remark",
			User:   "test_username",
			Token:  "secret_",
		},
	}
	assert.NoError(t, email.Send(context.TODO(), req))
	assert.Equal(t, "from@example.org", fakeSMTP.readMail())
	assert.Equal(t, 1, fakeSMTP.readQuitCount())
	assert.Equal(t, "test@example.org", fakeSMTP.readRcpt())
	// test buildVerificationMessage separately for message text
	res, err := email.buildVerificationMessage(req.Verification.User, req.Email, req.Verification.Token, req.Verification.SiteID)
	assert.NoError(t, err)
	assert.Contains(t, res, `From: from@example.org
To: test@example.org
Subject: Email verification
Content-Transfer-Encoding: quoted-printable
MIME-version: 1.0
Content-Type: text/html; charset="UTF-8"
Date: `)
	assert.Contains(t, res, `secret_`)
	assert.NotContains(t, res, `https://example.org/`)
	email.SubscribeURL = "https://example.org/subscribe.html?token="
	res, err = email.buildVerificationMessage(req.Verification.User, req.Email, req.Verification.Token, req.Verification.SiteID)
	assert.NoError(t, err)
	assert.Contains(t, res, `From: from@example.org
To: test@example.org
Subject: Email verification
Content-Transfer-Encoding: quoted-printable
MIME-version: 1.0
Content-Type: text/html; charset="UTF-8"
Date: `)
	assert.Contains(t, res, `https://example.org/subscribe.html?token=3Dsecret_`)
}

func Test_emailClient_Create(t *testing.T) {
	creator := emailClient{}
	client, err := creator.Create(SMTPParams{})
	assert.Error(t, err, "absence of address to connect results in error")
	assert.Nil(t, client, "no client returned in case of error")
}

type fakeTestSMTP struct {
	fail map[string]bool

	buff       bytes.Buffer
	mail, rcpt string
	auth       bool
	close      bool
	quitCount  int
	lock       sync.RWMutex
}

func (f *fakeTestSMTP) Create(SMTPParams) (smtpClient, error) {
	if f.fail["create"] {
		return nil, errors.New("failed to create client")
	}
	return f, nil
}

func (f *fakeTestSMTP) Auth(smtp.Auth) error { f.auth = true; return nil }

func (f *fakeTestSMTP) Mail(m string) error {
	f.lock.Lock()
	f.mail = m
	f.lock.Unlock()
	if f.fail["mail"] {
		return errors.New("failed to verify sender")
	}
	return nil
}

func (f *fakeTestSMTP) Rcpt(r string) error {
	f.lock.Lock()
	f.rcpt = r
	f.lock.Unlock()
	if f.fail["rcpt"] {
		return errors.New("failed to verify receiver")
	}
	return nil
}

func (f *fakeTestSMTP) Quit() error {
	f.lock.Lock()
	f.quitCount++
	f.lock.Unlock()
	if f.fail["quit"] {
		return errors.New("failed to quit")
	}
	return nil
}

func (f *fakeTestSMTP) Close() error {
	f.close = true
	if f.fail["close"] {
		return errors.New("failed to close")
	}
	return nil
}

func (f *fakeTestSMTP) Data() (io.WriteCloser, error) {
	if f.fail["data"] {
		return nil, errors.New("failed to send")
	}
	return nopCloser{&f.buff}, nil
}

func (f *fakeTestSMTP) readRcpt() string {
	f.lock.RLock()
	defer f.lock.RUnlock()
	return f.rcpt
}

func (f *fakeTestSMTP) readMail() string {
	f.lock.RLock()
	defer f.lock.RUnlock()
	return f.mail
}

func (f *fakeTestSMTP) readQuitCount() int {
	f.lock.RLock()
	defer f.lock.RUnlock()
	return f.quitCount
}

func TokenGenFn(user, _, _ string) (string, error) {
	if user == "error" {
		return "", errors.New("token generation error")
	}
	return "token", nil
}

type nopCloser struct {
	io.Writer
}

func (nopCloser) Close() error {
	return nil
}
