package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestMailPreviewUsesDeliveryComposerWithoutSendingOrSaving(t *testing.T) {
	a := freshAuthApp(t)
	login := authCall(a, "/api/auth/setup", setupBody, "", nil)
	requireStatus(t, login, 200)
	cookie := login.Result().Cookies()[0]
	m, err := a.mailService()
	if err != nil {
		t.Fatal(err)
	}
	m.record.Settings.From = "sender@example.com"
	m.record.Settings.Password = "preview-must-not-return-secret"
	m.record.Settings.Recipients = []string{"private-recipient@example.com"}
	for _, kind := range []string{"offline", "certificate", "update", "recovery", "test"} {
		w := authCall(a, "/api/admin/mail/preview?kind="+kind, "", "", cookie)
		requireStatus(t, w, 200)
		var data struct {
			Kind, Subject, Body, From string
			RecipientCount            int
			Example                   bool
			GeneratedAt               time.Time
		}
		if err = json.Unmarshal(w.Body.Bytes(), &data); err != nil {
			t.Fatal(err)
		}
		subject, body, ok := mailExample(kind, data.GeneratedAt)
		if !ok || data.Subject != subject || data.Body != body || !data.Example || data.Kind != kind || data.RecipientCount != 1 {
			t.Fatal("preview drifted from delivery template", data)
		}
		for _, secret := range []string{"preview-must-not-return-secret", "private-recipient@example.com", "password"} {
			if strings.Contains(w.Body.String(), secret) {
				t.Fatal("preview exposed private SMTP data")
			}
		}
		if kind == "test" && (data.Subject != mailTestSubject || data.Body != mailTestBody) {
			t.Fatal("test template mismatch")
		}
	}
	if _, err = os.Stat(m.path); !os.IsNotExist(err) {
		t.Fatal("preview persisted SMTP configuration", err)
	}
	if !m.record.LastAttemptAt.IsZero() || len(m.record.Deliveries) != 0 || !m.sending.TryLock() {
		t.Fatal("preview sent or queued email")
	}
	m.sending.Unlock()
	requireStatus(t, authCall(a, "/api/admin/mail/preview?kind=unknown", "", "", cookie), 400)
	requireStatus(t, authCall(a, "/api/admin/mail/preview", `{"kind":"unknown"}`, "", cookie), 400)
	requireStatus(t, authCall(a, "/api/admin/mail/preview", "", "", nil), 401)
}
