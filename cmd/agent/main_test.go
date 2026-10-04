package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/local/remotegate/internal/protocol"
)

func TestHandlePreservesLoginRedirectAndCookie(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" {
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "ok", Path: "/"})
			http.Redirect(w, r, "/admin", http.StatusFound)
			return
		}
		http.Error(w, "redirect was followed", http.StatusForbidden)
	}))
	defer backend.Close()

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	got := handle(client, Config{AllowPublicTargets: true}, protocol.Message{Method: http.MethodPost, Target: backend.URL, Path: "/login"})
	if got.Status != http.StatusFound {
		t.Fatalf("status = %d, want %d", got.Status, http.StatusFound)
	}
	if got.Headers["Set-Cookie"][0] != "session=ok; Path=/" {
		t.Fatalf("cookie was not preserved: %#v", got.Headers)
	}
	if got.Headers["Location"][0] != "/admin" {
		t.Fatalf("location was not preserved: %#v", got.Headers)
	}
}
