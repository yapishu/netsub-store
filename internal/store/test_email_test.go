package store

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestEmailPreviewAndSending(t *testing.T) {
	s := testServer(t)
	s.cfg.SendGridKey = "test-key"
	s.cfg.EmailFrom = "store@example.com"
	calls := 0
	s.http = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.String() != "https://api.sendgrid.com/v3/mail/send" || r.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatal("incorrect mail request")
		}
		var payload struct {
			Subject          string
			Content          []struct{ Value string }
			Personalizations []struct{ To []struct{ Email string } }
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(payload.Subject, "[TEST]") || !strings.Contains(payload.Content[0].Value, "TEST EMAIL") || len(payload.Personalizations) != 1 || payload.Personalizations[0].To[0].Email != "test@example.com" {
			t.Fatal("incorrect test email payload")
		}
		return &http.Response{StatusCode: 202, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}
	body := `{"email":"test@example.com","kind":"delivery"}`
	for _, session := range []string{"", "buyer"} {
		if w := request(s, "POST", "/api/admin/test-email", body, session); w.Code < 400 {
			t.Fatal("unauthorized test mail")
		}
	}
	if calls != 0 {
		t.Fatal("unauthorized mail sent")
	}
	w := request(s, "GET", "/api/admin/email-preview", "", "admin")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "SAMPLE-CODE-NOT-REDEEMABLE") {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, kind := range []string{"delivery", "receipt", "delivery"} {
		w = request(s, "POST", "/api/admin/test-email", `{"email":"test@example.com","kind":"`+kind+`"}`, "admin")
		if w.Code != 200 || !strings.Contains(w.Body.String(), "accepted") {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if w = request(s, "POST", "/api/admin/test-email", body, "admin"); w.Code != 429 {
		t.Fatal("test mail not rate limited")
	}
	if calls != 3 {
		t.Fatal(calls)
	}
}

func TestEmailValidationDemoAndProviderFailure(t *testing.T) {
	s := testServer(t)
	s.cfg.SendGridKey = "test-key"
	for _, body := range []string{`{"email":"bad","kind":"delivery"}`, `{"email":"test@example.com","kind":"unknown"}`} {
		if request(s, "POST", "/api/admin/test-email", body, "admin").Code != 400 {
			t.Fatal("invalid email accepted")
		}
	}
	body := `{"email":"test@example.com","kind":"delivery"}`
	s.cfg.Demo = true
	if request(s, "POST", "/api/admin/test-email", body, "admin").Code != 409 {
		t.Fatal("preview allows email")
	}
	s.cfg.Demo = false
	s.http = &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 403, Body: io.NopCloser(strings.NewReader("provider detail"))}, nil
	})}
	w := request(s, "POST", "/api/admin/test-email", body, "admin")
	if w.Code != 502 || !strings.Contains(w.Body.String(), "403") || strings.Contains(w.Body.String(), "provider detail") {
		t.Fatal(w.Code, w.Body.String())
	}
}
