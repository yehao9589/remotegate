package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/local/remotegate/internal/buildinfo"
	"github.com/local/remotegate/internal/config"
	"github.com/local/remotegate/internal/hub"
	"github.com/local/remotegate/internal/protocol"
)

//go:embed web/*
var webFS embed.FS

type app struct {
	certs        *certificateManager
	certMu       sync.Mutex
	certManagers map[string]*certificateManager
	certDir      string
	httpsAddress string
	httpsError   string
	store        *config.Store
	hub          *hub.Hub
	adminUser    string
	adminPass    string
	auth         *adminManager
	consoleHost  string
	publicURL    *url.URL
}

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "-version") {
		data, _ := json.Marshal(buildinfo.Current())
		log.Print(string(data))
		return
	}
	listen := env("LISTEN_ADDR", ":8080")
	statePath := env("STATE_PATH", "./data/state.json")
	auth, err := openAdminManager(filepath.Join(filepath.Dir(statePath), "admin.json"), os.Getenv("ADMIN_USERNAME"), os.Getenv("ADMIN_PASSWORD"))
	if err != nil {
		log.Fatal(err)
	}
	store, err := config.Open(statePath)
	if err != nil {
		log.Fatal(err)
	}
	a := &app{store: store, hub: hub.New(), auth: auth, consoleHost: strings.ToLower(os.Getenv("CONSOLE_HOST"))}
	if value := strings.TrimSpace(os.Getenv("PUBLIC_URL")); value != "" {
		a.publicURL, err = parsePublicURL(value)
		if err != nil {
			log.Fatal(err)
		}
	}
	a.certDir = filepath.Join(filepath.Dir(statePath), "certificates")
	a.certManagers = make(map[string]*certificateManager)
	a.startCertificateServices()
	server := &http.Server{Addr: listen, Handler: a.routes(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 75 * time.Second}
	log.Printf("RemoteGate %s (%s) server listening on %s", buildinfo.Tag(), buildinfo.Commit, listen)
	if !auth.initialized() {
		log.Print("首次启动：请打开管理后台创建管理员账号")
	}
	log.Fatal(server.ListenAndServe())
}

func (a *app) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/auth/status", a.authStatus)
	mux.HandleFunc("/api/auth/setup", a.authSetup)
	mux.HandleFunc("/api/auth/login", a.authLogin)
	mux.HandleFunc("/api/auth/logout", a.authLogout)
	mux.HandleFunc("/api/admin/security", a.admin(a.securitySettings))
	mux.HandleFunc("/api/admin/version", a.admin(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			http.Error(w, "method not allowed", 405)
			return
		}
		writeJSON(w, buildinfo.Current())
	}))
	mux.HandleFunc("/install", a.root)
	mux.HandleFunc("/api/admin/certificates", a.admin(a.certificates))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("/api/admin/domains", a.admin(a.domains))
	mux.HandleFunc("/api/admin/domains/check", a.admin(a.domainCheck))
	mux.HandleFunc("/api/admin/domains/", a.admin(a.domainByID))
	mux.HandleFunc("/api/admin/package", a.admin(a.packageInfo))
	mux.HandleFunc("/api/admin/install", a.admin(a.createInstall))
	mux.HandleFunc("/install/", a.installScript)
	mux.HandleFunc("/downloads/remotegate.run", a.installerDownload)
	mux.HandleFunc("/api/agent/connect", a.connectAgent)
	mux.HandleFunc("/api/admin/state", a.admin(a.state))
	mux.HandleFunc("/api/admin/devices", a.admin(a.devices))
	mux.HandleFunc("/api/admin/devices/", a.admin(a.deviceByID))
	mux.HandleFunc("/api/admin/probe", a.admin(a.probe))
	mux.HandleFunc("/api/admin/mappings", a.admin(a.mappings))
	mux.HandleFunc("/api/admin/mappings/", a.admin(a.mappingByID))
	mux.HandleFunc("/", a.root)
	return a.withPublicURL(mux)
}

func (a *app) root(w http.ResponseWriter, r *http.Request) {
	mapping, ok := a.store.MappingForHost(r.Host)
	showConsole := a.consoleHost != "" && hostOnly(r.Host) == a.consoleHost
	if a.consoleHost == "" && !ok {
		showConsole = true
	}
	// A local proxy may replace the external console Host with its loopback upstream.
	if publicURLForRequest(r) != nil && !ok && (hostOnly(r.Host) == "localhost" || netLoopbackHost(r.Host)) {
		showConsole = true
	}
	entryMatch := r.URL.Path == "/" || r.URL.Path == "/install"
	if a.auth != nil && a.auth.initialized() {
		entry := a.auth.entryPath()
		entryMatch = r.URL.Path == entry || (entry != "/" && r.URL.Path == entry+"/")
	}
	if showConsole && entryMatch {
		data, _ := webFS.ReadFile("web/index.html")
		guideJS, _ := webFS.ReadFile("web/install-guide.js")
		certJS, _ := webFS.ReadFile("web/certificates.js")
		guideJS = append(guideJS, certJS...)
		domainJS, _ := webFS.ReadFile("web/domain-center.js")
		guideJS = append(guideJS, domainJS...)
		authJS, _ := webFS.ReadFile("web/auth.js")
		guideJS = append(guideJS, authJS...)
		securityJS, _ := webFS.ReadFile("web/security.js")
		guideJS = append(guideJS, securityJS...)
		guideCSS, _ := webFS.ReadFile("web/install-guide.css")
		consoleCSS, _ := webFS.ReadFile("web/console.css")
		guideCSS = append(guideCSS, consoleCSS...)
		certCSS, _ := webFS.ReadFile("web/certificates.css")
		guideCSS = append(guideCSS, certCSS...)
		domainCSS, _ := webFS.ReadFile("web/domain-center.css")
		guideCSS = append(guideCSS, domainCSS...)
		authCSS, _ := webFS.ReadFile("web/auth.css")
		guideCSS = append(guideCSS, authCSS...)
		page := strings.Replace(string(data), "/* INSTALL_GUIDE_JS */", string(guideJS), 1)
		page = strings.Replace(page, "/* INSTALL_GUIDE_CSS */", string(guideCSS), 1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(page))
		return
	}
	if !ok {
		http.Error(w, "unknown host", http.StatusNotFound)
		return
	}
	if r.ContentLength > protocol.MaxBodyBytes {
		http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, protocol.MaxBodyBytes+1))
	if err != nil || len(body) > protocol.MaxBodyBytes {
		http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
		return
	}
	id := makeID()
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	resp, err := a.hub.Request(ctx, mapping.DeviceID, protocol.Message{Type: "request", ID: id, Method: r.Method, Target: mapping.Target, Path: r.URL.RequestURI(), Headers: protocol.FilterHeaders(r.Header), Body: body})
	if err != nil {
		if errors.Is(err, hub.ErrOffline) {
			http.Error(w, "device offline", http.StatusBadGateway)
		} else {
			http.Error(w, "tunnel request failed", http.StatusGatewayTimeout)
		}
		return
	}
	if resp.Error != "" {
		http.Error(w, resp.Error, http.StatusBadGateway)
		return
	}
	rewriteLocation(resp.Headers, mapping.Target, r)
	for key, values := range resp.Headers {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	status := resp.Status
	if status < 100 {
		status = http.StatusBadGateway
	}
	w.WriteHeader(status)
	_, _ = w.Write(resp.Body)
}

var upgrader = websocket.Upgrader{ReadBufferSize: 4096, WriteBufferSize: 4096, CheckOrigin: func(*http.Request) bool { return true }}

func (a *app) connectAgent(w http.ResponseWriter, r *http.Request) {
	id, token := r.URL.Query().Get("device_id"), r.URL.Query().Get("token")
	d, ok := a.store.Authenticate(id, token)
	if !ok {
		http.Error(w, "invalid device credentials", http.StatusUnauthorized)
		return
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	if err := a.store.ActivateWithPackage(id, token, r.URL.Query().Get("version"), r.URL.Query().Get("package_version")); err != nil {
		_ = conn.Close()
		log.Printf("device activation failed: %v", err)
		return
	}
	conn.SetReadLimit(protocol.MaxBodyBytes + (1 << 20))
	c := a.hub.Attach(id, d.Name, r.URL.Query().Get("version"), conn)
	log.Printf("device connected: %s (%s)", d.Name, id)
	defer func() { a.hub.Detach(c); _ = conn.Close(); log.Printf("device disconnected: %s (%s)", d.Name, id) }()
	_ = a.hub.ReadLoop(c)
}

func (a *app) admin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a.auth != nil {
			if !a.auth.initialized() {
				http.Error(w, "请先完成首次安装", http.StatusConflict)
				return
			}
			_, session := a.auth.sessionUser(r)
			if session && r.Method != http.MethodGet && r.Method != http.MethodHead && !sameAuthOrigin(r) {
				http.Error(w, "请求来源不匹配", http.StatusForbidden)
				return
			}
			if !session {
				user, pass, ok := r.BasicAuth()
				if !ok || !a.auth.verify(user, pass) {
					http.Error(w, "登录已失效，请重新登录", http.StatusUnauthorized)
					return
				}
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			next(w, r)
			return
		}
		user, pass, ok := r.BasicAuth()
		userOK := subtle.ConstantTimeCompare([]byte(user), []byte(a.adminUser)) == 1
		passOK := subtle.ConstantTimeCompare([]byte(pass), []byte(a.adminPass)) == 1
		if !ok || !userOK || !passOK {
			w.Header().Set("WWW-Authenticate", `Basic realm="RemoteGate"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		next(w, r)
	}
}

func (a *app) state(w http.ResponseWriter, _ *http.Request) {
	state := a.store.Snapshot()
	statuses := a.hub.Statuses()
	type view struct {
		config.Device
		Online      bool      `json:"online"`
		Version     string    `json:"version,omitempty"`
		ConnectedAt time.Time `json:"connectedAt,omitempty"`
	}
	devices := make([]view, 0, len(state.Devices))
	for _, d := range state.Devices {
		d.TokenHash = ""
		s, online := statuses[d.ID]
		devices = append(devices, view{Device: d, Online: online, Version: d.Version, ConnectedAt: s.Since})
	}
	writeJSON(w, map[string]any{"devices": devices, "mappings": state.Mappings})
}

func (a *app) devices(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		http.Error(w, "bad json", 400)
		return
	}
	d, token, err := a.store.CreatePendingDevice(in.Name)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	d.TokenHash = ""
	writeJSON(w, map[string]any{"device": d, "token": token})
}

func (a *app) deviceByID(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/admin/devices/")
	var in struct {
		Name string `json:"name"`
	}
	if r.Method != "DELETE" && r.Method != "PUT" {
		http.Error(w, "method not allowed", 405)
		return
	}
	if r.Method == "PUT" && json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in) != nil {
		http.Error(w, "bad json", 400)
		return
	}
	if err := a.store.ChangeDevice(id, in.Name, r.Method == "DELETE"); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if r.Method == "DELETE" {
		a.hub.Disconnect(id)
	}
	w.WriteHeader(204)
}

func (a *app) probe(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return
	}
	var in struct {
		ID string `json:"id"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in) != nil {
		http.Error(w, "bad json", 400)
		return
	}
	for _, m := range a.store.Snapshot().Mappings {
		if m.ID != in.ID {
			continue
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		start := time.Now()
		resp, err := a.hub.Request(ctx, m.DeviceID, protocol.Message{Type: "request", ID: makeID(), Method: "HEAD", Target: m.Target, Path: "/"})
		if err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
		if resp.Error != "" {
			http.Error(w, resp.Error, 502)
			return
		}
		writeJSON(w, map[string]any{"status": resp.Status, "latencyMs": time.Since(start).Milliseconds()})
		return
	}
	http.Error(w, "not found", 404)
}

func (a *app) mappings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var m config.Mapping
	if json.NewDecoder(r.Body).Decode(&m) != nil {
		http.Error(w, "bad json", 400)
		return
	}
	created, err := a.store.PutMapping(m)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	writeJSON(w, created)
}

func (a *app) mappingByID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", 405)
		return
	}
	if err := a.store.DeleteMapping(strings.TrimPrefix(r.URL.Path, "/api/admin/mappings/")); err != nil {
		http.Error(w, "not found", 404)
		return
	}
	w.WriteHeader(204)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}
func env(k, v string) string {
	if value := os.Getenv(k); value != "" {
		return value
	}
	return v
}
func makeID() string { b := make([]byte, 16); _, _ = rand.Read(b); return hex.EncodeToString(b) }
func hostOnly(v string) string {
	if u, err := url.Parse("//" + v); err == nil {
		return strings.ToLower(u.Hostname())
	}
	return strings.ToLower(v)
}

func rewriteLocation(headers map[string][]string, target string, request *http.Request) {
	values := headers["Location"]
	if len(values) == 0 {
		return
	}
	base, err := url.Parse(target)
	if err != nil {
		return
	}
	for i, value := range values {
		location, err := url.Parse(value)
		if err != nil || !location.IsAbs() || !strings.EqualFold(location.Host, base.Host) {
			continue
		}
		location.Scheme = "https"
		if request.Header.Get("X-Forwarded-Proto") == "http" {
			location.Scheme = "http"
		}
		location.Host = request.Host
		values[i] = location.String()
	}
	headers["Location"] = values
}
