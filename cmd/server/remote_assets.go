package main

import (
	"net/http"
	"strings"
)

func (a *app) remoteAssets(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "method not allowed", 405)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/api/admin/remote-assets/")
	if name != "xterm.js" && name != "addon-fit.js" {
		http.NotFound(w, r)
		return
	}
	body, err := webFS.ReadFile("web/vendor/" + name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(body)
}
