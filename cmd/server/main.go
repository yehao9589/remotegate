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
	"github.com/local/remotegate/internal/traffic"
)

//go:embed web/*
var webFS embed.FS

type app struct {
	mailMu            sync.Mutex
	mail              *notificationMailer
	mailErr           error
	certs             *certificateManager
	certMu            sync.Mutex
	certManagers      map[string]*certificateManager
	certDir           string
	httpsAddress      string
	httpsError        string
	httpsServer       *http.Server
	httpsSettings     httpsSettings
	httpsSettingsPath string
	store             *config.Store
	hub               *hub.Hub
	traffic           *traffic.Store
	adminUser         string
	adminPass         string
	auth              *adminManager
	consoleHost       string
	publicURL         *url.URL
	updateMu          sync.Mutex
	deviceUpdates     map[string]pluginUpdateView
	updatePackage     *verifiedUpdatePackage
	operators         *operatorResolver
	remoteMu          sync.Mutex
	remote            *remoteManager
	centerMu          sync.Mutex
	center            *packageCenter
	centerErr         error
	mappingMu         sync.Mutex
	mappingAccess     *mappingAccessManager
	releaseMu         sync.Mutex
	releases          *releaseChecker
}

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "-version") {
		data, _ := json.Marshal(buildinfo.Current())
		log.Print(string(data))
		return
	}
	listen := env("LISTEN_ADDR", "127.0.0.1:18088")
	statePath := env("STATE_PATH", "./data/state.json")
	auth, err := openAdminManager(filepath.Join(filepath.Dir(statePath), "admin.json"), os.Getenv("ADMIN_USERNAME"), os.Getenv("ADMIN_PASSWORD"))
	if err != nil {
		log.Fatal(err)
	}
	store, err := config.Open(statePath)
	if err != nil {
		log.Fatal(err)
	}
	if err := store.RecoverConnections(time.Now()); err != nil {
		log.Fatal(err)
	}
	totals, err := traffic.Open(filepath.Join(filepath.Dir(statePath), "traffic.json"))
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		if err := totals.Close(); err != nil {
			log.Printf("流量统计保存失败：%v", err)
		}
	}()
	a := &app{store: store, hub: hub.New(totals.Add), traffic: totals, auth: auth, consoleHost: strings.ToLower(os.Getenv("CONSOLE_HOST"))}
	a.operators = newOperatorResolver(filepath.Join(filepath.Dir(statePath), "ip-networks.json"), !strings.EqualFold(os.Getenv("IP_OPERATOR_LOOKUP"), "false"))
	if value := strings.TrimSpace(os.Getenv("PUBLIC_URL")); value != "" {
		a.publicURL, err = parsePublicURL(value)
		if err != nil {
			log.Fatal(err)
		}
	}
	a.certDir = filepath.Join(filepath.Dir(statePath), "certificates")
	a.certManagers = make(map[string]*certificateManager)
	a.httpsSettingsPath = filepath.Join(filepath.Dir(statePath), "https.json")
	go a.maintainPackageOperations(context.Background())
	monitorContext, stopMonitors := context.WithCancel(context.Background())
	monitorsDone := make(chan struct{})
	go func() {
		var wg sync.WaitGroup
		wg.Add(3)
		go func() { defer wg.Done(); a.maintainCertificates(monitorContext) }()
		go func() { defer wg.Done(); a.maintainAlerts(monitorContext) }()
		go func() { defer wg.Done(); a.maintainMail(monitorContext) }()
		wg.Wait()
		close(monitorsDone)
	}()
	defer func() { stopMonitors(); <-monitorsDone }()
	a.startCertificateServices()
	if err := a.bootstrapCertificate(os.Getenv); err != nil {
		log.Printf("首次 HTTPS 配置失败：%v；本机初始化入口仍可使用", err)
	}
	server := &http.Server{Addr: listen, Handler: a.routes(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 75 * time.Second}
	log.Printf("RemoteGate %s (%s) server listening on %s", buildinfo.Tag(), buildinfo.Commit, listen)
	if !auth.initialized() {
		log.Print("首次启动：请打开管理后台创建管理员账号")
	}
	if err := a.serveUntilStopped(server); err != nil {
		_ = totals.Close()
		log.Fatal(err)
	}
}

func (a *app) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/auth/status", a.authStatus)
	mux.HandleFunc("/api/auth/setup", a.authSetup)
	mux.HandleFunc("/api/auth/login", a.authLogin)
	mux.HandleFunc("/api/auth/logout", a.authLogout)
	mux.HandleFunc("/api/admin/security", a.admin(a.securitySettings))
	mux.HandleFunc("/api/admin/users", a.admin(a.usersHTTP))
	mux.HandleFunc("/api/admin/mail", a.admin(a.mailHTTP))
	mux.HandleFunc("/api/admin/mail/preview", a.admin(a.mailPreviewHTTP))
	mux.HandleFunc("/api/admin/mail/templates", a.admin(a.mailTemplatesHTTP))
	mux.HandleFunc(memberPath, a.memberPage)
	mux.HandleFunc("/api/member/state", a.member(a.memberState))
	mux.HandleFunc("/api/member/devices/", a.member(a.memberDeviceHTTP))
	mux.HandleFunc("/api/member/traffic/trends", a.member(a.memberTrends))
	mux.HandleFunc("/api/member/remote/", a.member(a.remoteSessionHTTP))
	mux.HandleFunc("/api/member/remote-assets/", a.member(a.remoteAssets))
	mux.HandleFunc("/api/admin/version", a.admin(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			http.Error(w, "method not allowed", 405)
			return
		}
		writeJSON(w, buildinfo.Current())
	}))
	mux.HandleFunc("/api/admin/version/check", a.admin(a.checkServerRelease))
	mux.HandleFunc("/api/admin/version/notes", a.admin(a.serverReleaseNotes))
	mux.HandleFunc("/install", a.root)
	mux.HandleFunc("/api/admin/certificates", a.admin(a.certificates))
	mux.HandleFunc("/api/admin/https", a.admin(a.httpsSettingsHandler))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("/api/admin/domains", a.admin(a.domains))
	mux.HandleFunc("/api/admin/domains/check", a.admin(a.domainCheck))
	mux.HandleFunc("/api/admin/diagnose", a.admin(a.diagnose))
	mux.HandleFunc("/api/admin/notifications", a.admin(a.notifications))
	mux.HandleFunc("/api/admin/traffic/trends", a.admin(a.trafficTrends))
	mux.HandleFunc("/api/admin/domains/", a.admin(a.domainByID))
	mux.HandleFunc("/api/admin/package", a.admin(a.packageInfo))
	mux.HandleFunc("/api/admin/package-center", a.admin(a.packageCenterHTTP))
	mux.HandleFunc("/api/admin/package-center/", a.admin(a.packageCenterHTTP))
	mux.HandleFunc("/api/admin/install", a.admin(a.createInstall))
	mux.HandleFunc("/install/", a.installScript)
	mux.HandleFunc("/api/install/report", a.installReportHTTP)
	mux.HandleFunc("/downloads/remotegate.run", a.installerDownload)
	mux.HandleFunc("/downloads/remotegate.ipk", a.ipkDownload)
	mux.HandleFunc("/api/agent/connect", a.connectAgent)
	mux.HandleFunc("/api/agent/update", a.agentUpdateResource)
	mux.HandleFunc("/api/agent/update/package", a.agentUpdateResource)
	mux.HandleFunc("/updates/", a.bootstrapUpdateDownload)
	mux.HandleFunc("/api/admin/state", a.admin(a.state))
	mux.HandleFunc("/api/admin/devices", a.admin(a.devices))
	mux.HandleFunc("/api/admin/devices/", a.admin(a.deviceByID))
	mux.HandleFunc("/api/admin/remote/", a.admin(a.remoteSessionHTTP))
	mux.HandleFunc("/api/admin/remote-assets/", a.admin(a.remoteAssets))
	mux.HandleFunc("/api/admin/probe", a.admin(a.probe))
	mux.HandleFunc("/api/admin/mappings", a.admin(a.mappings))
	mux.HandleFunc("/api/admin/mappings/", a.admin(a.mappingByID))
	mux.HandleFunc("/", a.root)
	return a.withPublicURL(a.mappingAccessHandler(mux))
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
		clipboardJS, _ := webFS.ReadFile("web/clipboard.js")
		guideJS = append(clipboardJS, guideJS...)
		mappingJS, _ := webFS.ReadFile("web/mapping-editor.js")
		guideJS = append(guideJS, mappingJS...)
		certJS, _ := webFS.ReadFile("web/certificates.js")
		guideJS = append(guideJS, certJS...)
		domainJS, _ := webFS.ReadFile("web/domain-center.js")
		guideJS = append(guideJS, domainJS...)
		authJS, _ := webFS.ReadFile("web/auth.js")
		guideJS = append(guideJS, authJS...)
		accessJS, _ := webFS.ReadFile("web/mapping-access.js")
		guideJS = append(guideJS, accessJS...)
		securityJS, _ := webFS.ReadFile("web/security.js")
		guideJS = append(guideJS, securityJS...)
		visualJS, _ := webFS.ReadFile("web/visuals.js")
		guideJS = append(guideJS, visualJS...)
		refreshJS, _ := webFS.ReadFile("web/refresh.js")
		guideJS = append(guideJS, refreshJS...)
		deploymentJS, _ := webFS.ReadFile("web/https-deploy.js")
		guideJS = append(guideJS, deploymentJS...)
		updateJS, _ := webFS.ReadFile("web/plugin-update.js")
		guideJS = append(guideJS, updateJS...)
		deviceJS, _ := webFS.ReadFile("web/device-center.js")
		guideJS = append(guideJS, deviceJS...)
		remoteJS, _ := webFS.ReadFile("web/remote-access.js")
		guideJS = append(guideJS, remoteJS...)
		packageJS, _ := webFS.ReadFile("web/package-center.js")
		guideJS = append(guideJS, packageJS...)
		releasesJS, _ := webFS.ReadFile("web/server-version.js")
		guideJS = append(guideJS, releasesJS...)
		operationsJS, _ := webFS.ReadFile("web/operations.js")
		guideJS = append(guideJS, operationsJS...)
		accountJS, _ := webFS.ReadFile("web/account-settings.js")
		guideJS = append(guideJS, accountJS...)
		mailPreviewJS, _ := webFS.ReadFile("web/mail-preview.js")
		guideJS = append(guideJS, mailPreviewJS...)
		mailTemplatesJS, _ := webFS.ReadFile("web/mail-template-editor.js")
		guideJS = append(guideJS, mailTemplatesJS...)
		guideCSS, _ := webFS.ReadFile("web/install-guide.css")
		consoleCSS, _ := webFS.ReadFile("web/console.css")
		guideCSS = append(guideCSS, consoleCSS...)
		accessCSS, _ := webFS.ReadFile("web/mapping-access.css")
		guideCSS = append(guideCSS, accessCSS...)
		certCSS, _ := webFS.ReadFile("web/certificates.css")
		guideCSS = append(guideCSS, certCSS...)
		domainCSS, _ := webFS.ReadFile("web/domain-center.css")
		guideCSS = append(guideCSS, domainCSS...)
		authCSS, _ := webFS.ReadFile("web/auth.css")
		guideCSS = append(guideCSS, authCSS...)
		visualCSS, _ := webFS.ReadFile("web/visuals.css")
		guideCSS = append(guideCSS, visualCSS...)
		updateCSS, _ := webFS.ReadFile("web/plugin-update.css")
		guideCSS = append(guideCSS, updateCSS...)
		deviceCSS, _ := webFS.ReadFile("web/device-center.css")
		guideCSS = append(guideCSS, deviceCSS...)
		terminalCSS, _ := webFS.ReadFile("web/vendor/xterm.css")
		guideCSS = append(guideCSS, terminalCSS...)
		remoteCSS, _ := webFS.ReadFile("web/remote-access.css")
		guideCSS = append(guideCSS, remoteCSS...)
		packageCSS, _ := webFS.ReadFile("web/package-center.css")
		guideCSS = append(guideCSS, packageCSS...)
		releasesCSS, _ := webFS.ReadFile("web/server-version.css")
		guideCSS = append(guideCSS, releasesCSS...)
		operationsCSS, _ := webFS.ReadFile("web/operations.css")
		guideCSS = append(guideCSS, operationsCSS...)
		accountCSS, _ := webFS.ReadFile("web/account-settings.css")
		guideCSS = append(guideCSS, accountCSS...)
		mailPreviewCSS, _ := webFS.ReadFile("web/mail-preview.css")
		guideCSS = append(guideCSS, mailPreviewCSS...)
		mailTemplatesCSS, _ := webFS.ReadFile("web/mail-template-editor.css")
		guideCSS = append(guideCSS, mailTemplatesCSS...)
		page := strings.Replace(string(data), "/* INSTALL_GUIDE_JS */", string(guideJS), 1)
		page = strings.Replace(page, "/* INSTALL_GUIDE_CSS */", string(guideCSS), 1)
		page = a.renderConsoleAuth(page, r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Add("Vary", "Cookie")
		_, _ = w.Write([]byte(page))
		return
	}
	if !ok {
		message := "unknown host\n访问域名没有匹配到已启用的映射，请检查映射域名和反向代理的 Host 设置。"
		if loopbackPeer(r) && (netLoopbackHost(r.Host) || hostOnly(r.Host) == "localhost") {
			message = "unknown host\n反向代理把访问域名替换成了本机地址，无法识别子域名映射。请在 RemoteGate 对应站点的反代中保留原始 Host：proxy_set_header Host $http_host;"
		}
		http.Error(w, message, http.StatusNotFound)
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
	message := protocol.Message{Type: "request", ID: id, Method: r.Method, Target: mapping.Target, Path: r.URL.RequestURI(), Headers: mappingProxyHeaders(r), Body: body}
	resp, err := a.hub.Request(ctx, mapping.DeviceID, message, mapping.ID)
	if errors.Is(err, hub.ErrOffline) {
		if grace := a.mappingRecoveryGrace(mapping.DeviceID, time.Now()); grace > 0 {
			if waitErr := a.hub.WaitOnline(ctx, mapping.DeviceID, grace); waitErr == nil {
				// The mapping, session or device grants may have changed while
				// waiting. Never send a queued request using stale authorization.
				current, exists := a.store.MappingForHost(r.Host)
				if !exists || current != mapping {
					http.Error(w, "映射配置已变化，请重新访问", http.StatusConflict)
					return
				}
				if current.AccessMode != "public" && !a.requireMappingAccess(w, r, current) {
					return
				}
				resp, err = a.hub.Request(ctx, mapping.DeviceID, message, mapping.ID)
			} else if r.Context().Err() != nil {
				return
			}
		}
	}
	if err != nil {
		if a.mappingRecoveryPage(w, r, mapping.DeviceID) {
			return
		}
		if errors.Is(err, hub.ErrOffline) {
			http.Error(w, "device offline", http.StatusBadGateway)
		} else {
			http.Error(w, "tunnel request failed", http.StatusGatewayTimeout)
		}
		return
	}
	if resp.Error != "" {
		if resp.Error == "device disconnected" && a.mappingRecoveryPage(w, r, mapping.DeviceID) {
			return
		}
		http.Error(w, resp.Error, http.StatusBadGateway)
		return
	}
	rewriteLocation(resp.Headers, mapping.Target, r)
	for key, values := range resp.Headers {
		for _, value := range values {
			if strings.EqualFold(key, "Set-Cookie") && !mappingProxyCookie(value) {
				continue
			}
			w.Header().Add(key, value)
		}
	}
	if mapping.AccessMode != "public" {
		accessNoStore(w)
		// no-referrer makes browsers send Origin: null on same-origin form
		// navigations, which would fail the mapping's write-origin check.
		// Auth callbacks retain no-referrer; application pages keep their origin
		// for local forms while still hiding it from other sites.
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Add("Vary", "Cookie")
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
	info := deviceConnectionInfo(r)
	if err := info.Validate(); err != nil {
		http.Error(w, err.Error(), 400)
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
	conn.SetReadLimit(protocol.MaxMessageBytes)
	c := a.hub.Attach(id, d.Name, r.URL.Query().Get("version"), conn, r.URL.Query().Get("updater") == "1", r.URL.Query().Get("remote") == "1", r.URL.Query().Get("update_tools") == "1", r.URL.Query().Get("update_recovery") == "1")
	if err := a.store.RecordConnection(id, c.Since, info); err != nil {
		log.Printf("device connection information failed: %v", err)
	}
	log.Printf("device connected: %s (%s)", d.Name, id)
	defer func() {
		// Publish the recovery timestamp before waking pending HTTP requests.
		if err := a.store.RecordDisconnect(id, c.Since, time.Now()); err != nil {
			log.Printf("device disconnect information failed: %v", err)
		}
		a.hub.Detach(c)
		_ = conn.Close()
		log.Printf("device disconnected: %s (%s)", d.Name, id)
	}()
	if err := a.hub.ReadLoop(c, func(iface string) error {
		return a.store.RecordEgressInterface(id, c.Since, iface)
	}); err != nil {
		log.Printf("device tunnel ended: %s (%s): %v", d.Name, id, err)
	}
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

func (a *app) state(w http.ResponseWriter, r *http.Request) {
	if origin, err := a.accessConsoleFromAdmin(r); err == nil {
		if err = a.store.BindMappingAccessConsole(origin.String()); err != nil {
			http.Error(w, "认证地址保存失败，请检查数据目录写入权限", 500)
			return
		}
	}
	state := a.store.Snapshot()
	statuses := a.hub.Statuses()
	devices := make([]deviceView, 0, len(state.Devices))
	for _, d := range state.Devices {
		s, online := statuses[d.ID]
		devices = append(devices, a.viewDevice(d, s, online))
	}
	totals := traffic.Snapshot{Devices: map[string]traffic.Counter{}, Mappings: map[string]traffic.Counter{}}
	if a.traffic != nil {
		all := a.traffic.Snapshot()
		totals.Since, totals.Version = all.Since, all.Version
		for _, d := range state.Devices {
			totals.Devices[d.ID] = all.Devices[d.ID]
		}
		for _, m := range state.Mappings {
			totals.Mappings[m.ID] = all.Mappings[m.ID]
		}
	}
	writeJSON(w, map[string]any{"devices": devices, "mappings": state.Mappings, "traffic": totals})
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
	if deviceID, operation, ok := strings.Cut(id, "/"); ok {
		if operation == "status" {
			a.refreshDeviceStatus(w, r, deviceID)
			return
		}
		if operation == "events" {
			a.deviceConnectionEvents(w, r, deviceID)
			return
		}
		if operation == "remote" {
			a.deviceRemoteConnect(w, r, deviceID)
			return
		}
		if operation == "health" || operation == "reconnect" {
			a.deviceConnectionAction(w, r, deviceID, operation)
			return
		}
		if operation == "update" {
			a.devicePluginUpdate(w, r, deviceID)
			return
		}
		if operation == "update/bootstrap" {
			a.bootstrapPluginUpdate(w, r, deviceID)
			return
		}
		http.NotFound(w, r)
		return
	}
	var in struct {
		Name   *string `json:"name"`
		Note   *string `json:"note"`
		Pinned *bool   `json:"pinned"`
	}
	if r.Method != "DELETE" && r.Method != "PUT" {
		http.Error(w, "method not allowed", 405)
		return
	}
	if r.Method == "PUT" && json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in) != nil {
		http.Error(w, "bad json", 400)
		return
	}
	var err error
	if r.Method == "DELETE" {
		err = a.store.ChangeDevice(id, "", true)
	} else {
		err = a.store.UpdateDevice(id, in.Name, in.Note, in.Pinned)
	}
	if err != nil {
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
		resp, err := a.hub.Request(ctx, m.DeviceID, protocol.Message{Type: "request", ID: makeID(), Method: "HEAD", Target: m.Target, Path: "/"}, m.ID)
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
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&m) != nil {
		http.Error(w, "bad json", 400)
		return
	}
	host := hostOnly(strings.TrimSpace(m.Host))
	console, _ := a.configuredAccessConsole()
	if strings.EqualFold(host, hostOnly(r.Host)) || (console != nil && strings.EqualFold(host, console.Hostname())) {
		http.Error(w, "映射域名不能与管理后台域名相同，请使用独立子域名", 400)
		return
	}
	if m.AccessMode != "public" {
		origin, err := a.accessConsoleFromAdmin(r)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		m.AccessConsoleURL = origin.String()
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
