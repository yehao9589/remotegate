package main

import (
	"encoding/json"
	"net/http"
	"strings"
)

// Decide the first visible screen with the same session validation used by the
// admin API. Only UI state is embedded; every API request still authenticates.
func (a *app) renderConsoleAuth(page string, r *http.Request) string {
	initial := struct {
		Panel     string `json:"panel"`
		EntryPath string `json:"entryPath,omitempty"`
	}{Panel: "login"}
	if a.auth != nil {
		if !a.auth.initialized() {
			initial.Panel = "firstInstall"
		} else if _, valid := a.auth.sessionUser(r); valid {
			initial.Panel = "workbench"
			initial.EntryPath = a.auth.entryPath()
		}
	}
	data, _ := json.Marshal(initial)
	page = strings.Replace(page, "/* AUTH_INITIAL_STATE */null", string(data), 1)
	if initial.Panel == "workbench" {
		page = strings.Replace(page, `<body class="auth-visible">`, `<body>`, 1)
		page = strings.Replace(page, `<div id="authScreen">`, `<div id="authScreen" hidden>`, 1)
		page = strings.Replace(page, `<div id="shell" hidden>`, `<div id="shell">`, 1)
	} else {
		page = strings.Replace(page, `<section id="`+initial.Panel+`" hidden>`, `<section id="`+initial.Panel+`">`, 1)
	}
	return page
}
