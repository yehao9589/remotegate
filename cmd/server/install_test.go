package main

import (
	"encoding/json"
	"github.com/local/remotegate/internal/config"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestInstallTicketSingleUse(t *testing.T) {
	path := t.TempDir() + "/package.run"
	os.WriteFile(path, []byte("package"), 0600)
	t.Setenv("INSTALLER_PATH", path)
	s, _ := config.Open(t.TempDir() + "/state.json")
	d, token, _ := s.CreateDevice("test")
	a := &app{store: s}
	body, _ := json.Marshal(map[string]string{"deviceId": d.ID, "token": token, "server": "http://10.0.2.2:18088"})
	w := httptest.NewRecorder()
	a.createInstall(w, httptest.NewRequest("POST", "/api/admin/install", strings.NewReader(string(body))))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var result struct {
		Command string `json:"command"`
	}
	json.Unmarshal(w.Body.Bytes(), &result)
	link := strings.Split(result.Command, "'")[1]
	if strings.Contains(result.Command, token) {
		t.Fatal("long lived token in command")
	}
	for i, want := range []int{200, 410} {
		w = httptest.NewRecorder()
		a.installScript(w, httptest.NewRequest("GET", link, nil))
		if w.Code != want {
			t.Fatalf("attempt %d status %d", i, w.Code)
		}
		if i == 0 && (!strings.Contains(w.Body.String(), "sha256sum -c") || !strings.Contains(w.Body.String(), token)) {
			t.Fatal("missing integrity or config")
		}
	}
}
