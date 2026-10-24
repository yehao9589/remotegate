package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

const remoteIdle = 15 * time.Minute

type remoteManager struct {
	mu       sync.Mutex
	sessions map[string]*remoteSession
	pending  int
}
type remoteSession struct {
	manager            *remoteManager
	id, device, target string
	owner              [32]byte
	conn               net.Conn
	ssh                *ssh.Client
	mu                 sync.Mutex
	touched            time.Time
	terminalTicket     string
	terminalUsed       bool
	files              *sftp.Client
	home               string
	fileMu             sync.Mutex
	uploads            map[string]*remoteUpload
	downloads          map[string]string
	done               chan struct{}
	once               sync.Once
}
type remoteConnectInput struct {
	View                                                        string
	Action, Host, Username, PrivateKey, Passphrase, Fingerprint string
	Password                                                    *string
	Port                                                        int
}

func (a *app) remoteManager() *remoteManager {
	a.remoteMu.Lock()
	defer a.remoteMu.Unlock()
	if a.remote == nil {
		a.remote = &remoteManager{sessions: make(map[string]*remoteSession)}
	}
	return a.remote
}

func (a *app) remoteOwner(r *http.Request) ([32]byte, bool) {
	if a.auth == nil {
		return [32]byte{}, false
	}
	a.auth.mu.Lock()
	defer a.auth.mu.Unlock()
	for _, name := range []string{adminCookie, httpAdminCookie} {
		if cookie, err := r.Cookie(name); err == nil {
			key := sha256.Sum256([]byte(cookie.Value))
			if session, ok := a.auth.sessions[key]; ok && a.auth.validSessionLocked(session) {
				return key, true
			}
		}
	}
	return [32]byte{}, false
}

func remoteHTTPS(r *http.Request) bool {
	// An explicit browser Origin is authoritative even behind a loopback proxy.
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := parsePublicURL(origin)
		if err != nil {
			return false
		}
		return u.Scheme == "https" || netLoopbackHost(u.Host) || hostOnly(u.Host) == "localhost"
	}
	return secureAuthRequest(r) || (loopbackPeer(r) && (netLoopbackHost(r.Host) || hostOnly(r.Host) == "localhost"))
}

func (a *app) deviceRemoteConnect(w http.ResponseWriter, r *http.Request, deviceID string) {
	owner, authenticated := a.remoteOwner(r)
	if !authenticated {
		http.Error(w, "请使用当前管理员登录会话", 401)
		return
	}
	if !a.auth.ownerAllows(owner, deviceID, "remote") {
		http.Error(w, "未获授权使用此设备的远程工具", 403)
		return
	}
	if r.Method != "POST" || !sameAuthOrigin(r) {
		http.Error(w, "请求方式或来源不匹配", 403)
		return
	}
	if !remoteHTTPS(r) {
		http.Error(w, "SSH 和文件管理请通过 HTTPS 后台访问；本机 HTTP 预览除外", 400)
		return
	}
	var input remoteConnectInput
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 96<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF {
		http.Error(w, "连接参数无效", 400)
		return
	}
	if input.View != "" && input.View != "terminal" && input.View != "files" {
		http.Error(w, "远程工具类型无效", 400)
		return
	}
	capability := "ssh"
	if input.View == "files" {
		capability = "files"
	}
	if !a.auth.ownerAllows(owner, deviceID, capability) {
		http.Error(w, "未获授权使用此远程工具", 403)
		return
	}
	host := strings.TrimSpace(input.Host)
	if host == "" {
		host = "127.0.0.1"
	}
	if input.Port == 0 {
		input.Port = 22
	}
	if len(host) > 253 || strings.ContainsAny(host, "\x00\r\n /\\") || input.Port < 1 || input.Port > 65535 || (input.Action != "fingerprint" && input.Action != "connect") {
		http.Error(w, "SSH 地址、端口或操作无效", 400)
		return
	}
	found := false
	for _, device := range a.store.Snapshot().Devices {
		found = found || device.ID == deviceID
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	manager := a.remoteManager()
	manager.mu.Lock()
	if len(manager.sessions)+manager.pending >= 32 {
		manager.mu.Unlock()
		http.Error(w, "远程会话数量已达上限", 429)
		return
	}
	manager.pending++
	manager.mu.Unlock()
	defer func() { manager.mu.Lock(); manager.pending--; manager.mu.Unlock() }()
	target := net.JoinHostPort(strings.Trim(host, "[]"), strconv.Itoa(input.Port))
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	conn, err := a.hub.OpenTCP(ctx, deviceID, target)
	if err != nil {
		http.Error(w, err.Error(), 409)
		return
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	if input.Action == "fingerprint" {
		defer conn.Close()
		var fingerprint, algorithm string
		_, _, _, _ = ssh.NewClientConn(conn, target, &ssh.ClientConfig{User: "remotegate-probe", HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			fingerprint, algorithm = ssh.FingerprintSHA256(key), key.Type()
			return errors.New("host key captured before authentication")
		}})
		if fingerprint == "" {
			http.Error(w, "无法读取 SSH 主机指纹，请检查地址、端口及 SSH 服务", 502)
			return
		}
		writeJSON(w, map[string]string{"fingerprint": fingerprint, "algorithm": algorithm, "target": target})
		return
	}
	username := strings.TrimSpace(input.Username)
	if username == "" || len(username) > 128 || strings.ContainsAny(username, "\x00\r\n") || !strings.HasPrefix(input.Fingerprint, "SHA256:") {
		conn.Close()
		http.Error(w, "请填写 SSH 用户名，并先检测和确认主机指纹", 400)
		return
	}
	var auth ssh.AuthMethod
	if input.PrivateKey != "" {
		var signer ssh.Signer
		if input.Passphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(input.PrivateKey), []byte(input.Passphrase))
		} else {
			signer, err = ssh.ParsePrivateKey([]byte(input.PrivateKey))
		}
		if err != nil {
			conn.Close()
			http.Error(w, "私钥或私钥密码无效", 400)
			return
		}
		auth = ssh.PublicKeys(signer)
	} else if input.Password != nil && len(*input.Password) <= 4096 {
		// An explicitly blank password still uses the target's SSH authentication policy.
		auth = ssh.Password(*input.Password)
	} else {
		conn.Close()
		http.Error(w, "请输入 SSH 密码或私钥", 400)
		return
	}
	mismatch := false
	clientConn, channels, requests, err := ssh.NewClientConn(conn, target, &ssh.ClientConfig{User: username, Auth: []ssh.AuthMethod{auth}, HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
		if subtle.ConstantTimeCompare([]byte(ssh.FingerprintSHA256(key)), []byte(input.Fingerprint)) != 1 {
			mismatch = true
			return errors.New("host fingerprint changed")
		}
		return nil
	}})
	if err != nil {
		conn.Close()
		message := "SSH 连接或认证失败，请检查 SSH 账号、密码或私钥"
		if mismatch {
			message = "SSH 主机指纹已变化，连接已拒绝；请重新检测并核对指纹"
		}
		http.Error(w, message, 502)
		return
	}
	if current, valid := a.remoteOwner(r); !valid || current != owner || ctx.Err() != nil {
		clientConn.Close()
		http.Error(w, "登录已失效", 401)
		return
	}
	stop()
	_ = conn.SetDeadline(time.Time{})
	session := &remoteSession{manager: manager, id: makeID() + makeID(), owner: owner, device: deviceID, target: target, conn: conn, ssh: ssh.NewClient(clientConn, channels, requests), touched: time.Now(), terminalTicket: makeID() + makeID(), uploads: make(map[string]*remoteUpload), downloads: make(map[string]string), done: make(chan struct{})}
	manager.mu.Lock()
	manager.sessions[session.id] = session
	manager.mu.Unlock()
	go func() { _ = session.ssh.Wait(); session.close() }()
	go session.monitor(a)
	writeJSON(w, map[string]any{"id": session.id, "target": target, "terminalTicket": session.terminalTicket, "idleMinutes": 15})
}

func (s *remoteSession) touch() { s.mu.Lock(); s.touched = time.Now(); s.mu.Unlock() }
func (s *remoteSession) monitor(a *app) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
			a.auth.mu.Lock()
			login, valid := a.auth.sessions[s.owner]
			valid = valid && a.auth.validSessionLocked(login)
			a.auth.mu.Unlock()
			valid = valid && a.auth.ownerAllows(s.owner, s.device, "remote")
			s.mu.Lock()
			expired := time.Since(s.touched) >= remoteIdle
			s.mu.Unlock()
			if !valid || expired {
				s.close()
				return
			}
			timeout := time.AfterFunc(10*time.Second, func() { _ = s.conn.Close() })
			_, _, err := s.ssh.SendRequest("keepalive@openssh.com", true, nil)
			timeout.Stop()
			if err != nil {
				s.close()
				return
			}
		}
	}
}
func (s *remoteSession) close() {
	s.once.Do(func() {
		close(s.done)
		s.manager.mu.Lock()
		delete(s.manager.sessions, s.id)
		s.manager.mu.Unlock()
		_ = s.conn.SetDeadline(time.Now().Add(2 * time.Second))
		s.fileMu.Lock()
		for _, upload := range s.uploads {
			_ = upload.file.Close()
			if s.files != nil {
				_ = s.files.Remove(upload.temp)
			}
		}
		s.uploads = make(map[string]*remoteUpload)
		s.fileMu.Unlock()
		_ = s.ssh.Close()
	})
}
func (m *remoteManager) closeOwner(owner [32]byte) {
	m.mu.Lock()
	var sessions []*remoteSession
	for _, session := range m.sessions {
		if session.owner == owner {
			sessions = append(sessions, session)
		}
	}
	m.mu.Unlock()
	for _, session := range sessions {
		session.close()
	}
}

func (a *app) remoteSessionHTTP(w http.ResponseWriter, r *http.Request) {
	owner, valid := a.remoteOwner(r)
	if !valid {
		http.Error(w, "请重新登录", 401)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/member/remote/")
	if path == r.URL.Path {
		path = strings.TrimPrefix(r.URL.Path, "/api/admin/remote/")
	}
	parts := strings.Split(path, "/")
	if len(parts) < 1 || len(parts) > 2 {
		http.NotFound(w, r)
		return
	}
	manager := a.remoteManager()
	manager.mu.Lock()
	session := manager.sessions[parts[0]]
	manager.mu.Unlock()
	if session == nil || session.owner != owner {
		http.NotFound(w, r)
		return
	}
	capability := "remote"
	if len(parts) == 2 {
		if parts[1] == "terminal" {
			capability = "ssh"
		} else {
			capability = "files"
		}
	}
	if !a.auth.ownerAllows(owner, session.device, capability) {
		session.close()
		http.Error(w, "此设备的远程工具授权已失效", 403)
		return
	}
	session.touch()
	if len(parts) == 1 && r.Method == "DELETE" {
		if !sameAuthOrigin(r) {
			http.Error(w, "请求来源不匹配", 403)
			return
		}
		session.close()
		w.WriteHeader(204)
		return
	}
	if len(parts) != 2 {
		http.Error(w, "method not allowed", 405)
		return
	}
	switch parts[1] {
	case "terminal":
		a.remoteTerminal(w, r, session)
	case "files":
		session.fileAction(w, r)
	case "download":
		session.fileDownload(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (a *app) remoteTerminal(w http.ResponseWriter, r *http.Request, s *remoteSession) {
	if r.Method != "GET" || r.Header.Get("Origin") == "" || !sameAuthOrigin(r) {
		http.Error(w, "终端请求来源不匹配", 403)
		return
	}
	s.mu.Lock()
	ticket := r.URL.Query().Get("ticket")
	allowed := ticket != "" && subtle.ConstantTimeCompare([]byte(ticket), []byte(s.terminalTicket)) == 1 && !s.terminalUsed
	if allowed {
		s.terminalUsed = true
		s.terminalTicket = ""
	}
	s.mu.Unlock()
	if !allowed {
		http.Error(w, "终端连接已失效，请重新连接", 409)
		return
	}
	ws, err := (&websocket.Upgrader{HandshakeTimeout: 10 * time.Second, CheckOrigin: sameAuthOrigin}).Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer ws.Close()
	setupTimeout := time.AfterFunc(15*time.Second, func() { _ = s.conn.Close() })
	defer setupTimeout.Stop()
	channel, err := s.ssh.NewSession()
	if err != nil {
		return
	}
	defer channel.Close()
	input, err := channel.StdinPipe()
	if err != nil {
		return
	}
	var writeMu sync.Mutex
	output := remoteTerminalWriter{conn: ws, mu: &writeMu}
	channel.Stdout, channel.Stderr = output, output
	if err = channel.RequestPty("xterm-256color", 30, 100, ssh.TerminalModes{ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 14400, ssh.TTY_OP_OSPEED: 14400}); err != nil {
		return
	}
	if err = channel.Shell(); err != nil {
		return
	}
	setupTimeout.Stop()
	finished := make(chan struct{})
	go func() { _ = channel.Wait(); _ = ws.Close(); close(finished) }()
	defer func() { _ = channel.Close(); _ = ws.Close(); <-finished }()
	ws.SetReadLimit(64 << 10)
	_ = ws.SetReadDeadline(time.Now().Add(90 * time.Second))
	ws.SetPongHandler(func(string) error { return ws.SetReadDeadline(time.Now().Add(90 * time.Second)) })
	heartbeatDone := make(chan struct{})
	defer close(heartbeatDone)
	go func() {
		ticker := time.NewTicker(25 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatDone:
				return
			case <-s.done:
				_ = ws.Close()
				return
			case <-ticker.C:
				if ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)) != nil {
					_ = ws.Close()
					return
				}
			}
		}
	}()
	for {
		var frame struct {
			Type string
			Data string
			Cols int
			Rows int
		}
		if ws.ReadJSON(&frame) != nil {
			return
		}
		if owner, valid := a.remoteOwner(r); !valid || owner != s.owner || !a.auth.ownerAllows(owner, s.device, "ssh") {
			return
		}
		s.touch()
		switch frame.Type {
		case "input":
			if len(frame.Data) > 32<<10 {
				return
			}
			if _, err := io.WriteString(input, frame.Data); err != nil {
				return
			}
		case "resize":
			if frame.Cols >= 20 && frame.Cols <= 400 && frame.Rows >= 5 && frame.Rows <= 200 {
				_ = channel.WindowChange(frame.Rows, frame.Cols)
			}
		}
	}
}

type remoteTerminalWriter struct {
	conn *websocket.Conn
	mu   *sync.Mutex
}

func (w remoteTerminalWriter) Write(body []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.conn.SetWriteDeadline(time.Now().Add(15 * time.Second))
	if err := w.conn.WriteMessage(websocket.BinaryMessage, body); err != nil {
		return 0, err
	}
	return len(body), nil
}
