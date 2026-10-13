package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/local/remotegate/internal/protocol"
	"github.com/local/remotegate/internal/tcpbridge"
	"github.com/local/remotegate/internal/testssh"
	"golang.org/x/crypto/ssh"
)

type remoteFixture struct {
	app           *app
	device, token string
	cookie        *http.Cookie
	serverURL     string
	ssh           *testssh.Server
	host          string
	port          int
}

func newRemoteFixture(t *testing.T) *remoteFixture {
	t.Helper()
	a := freshAuthApp(t)
	login := authCall(a, "/api/auth/setup", setupBody, "", nil)
	requireStatus(t, login, 200)
	cookie := login.Result().Cookies()[0]
	device, token, err := a.store.CreateDevice("SSH fixture")
	if err != nil {
		t.Fatal(err)
	}
	sshServer, err := testssh.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sshServer.Close)
	server := agentTestServer(t, a)
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/api/agent/connect?device_id="+device.ID+"&token="+token+"&remote=1", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var writer sync.Mutex
	bridge := tcpbridge.New(ctx, true, func(msg protocol.Message) error { writer.Lock(); defer writer.Unlock(); return conn.WriteJSON(msg) })
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			var msg protocol.Message
			if conn.ReadJSON(&msg) != nil {
				return
			}
			bridge.Handle(msg)
		}
	}()
	t.Cleanup(func() {
		a.remoteMu.Lock()
		manager := a.remote
		a.remoteMu.Unlock()
		if manager != nil {
			manager.closeOwner(sha256.Sum256([]byte(cookie.Value)))
		}
		cancel()
		conn.Close()
		bridge.Close()
		<-done
	})
	waitDevice(t, a, device.ID, true)
	host, portText, _ := net.SplitHostPort(sshServer.Address())
	port, _ := strconv.Atoi(portText)
	return &remoteFixture{a, device.ID, token, cookie, server.URL, sshServer, host, port}
}

func remoteTestCall(a *app, cookie *http.Cookie, url string, body any) *httptest.ResponseRecorder {
	var data io.Reader
	method := "GET"
	if body != nil {
		raw, _ := json.Marshal(body)
		data = bytes.NewReader(raw)
		method = "POST"
	}
	r := httptest.NewRequest(method, "http://console.localhost"+url, data)
	r.RemoteAddr = "127.0.0.1:3456"
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://console.localhost")
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, r)
	return w
}
func (f *remoteFixture) connect(t *testing.T) map[string]any {
	t.Helper()
	url := "/api/admin/devices/" + f.device + "/remote"
	probe := remoteTestCall(f.app, f.cookie, url, map[string]any{"action": "fingerprint", "host": f.host, "port": f.port})
	requireStatus(t, probe, 200)
	var fingerprint map[string]string
	if err := json.Unmarshal(probe.Body.Bytes(), &fingerprint); err != nil {
		t.Fatal(err)
	}
	if fingerprint["fingerprint"] != f.ssh.Fingerprint {
		t.Fatal("fingerprint changed in transit")
	}
	connected := remoteTestCall(f.app, f.cookie, url, map[string]any{"action": "connect", "host": f.host, "port": f.port, "username": testssh.Username, "password": testssh.Password, "fingerprint": fingerprint["fingerprint"]})
	requireStatus(t, connected, 200)
	if strings.Contains(connected.Body.String(), testssh.Password) || strings.Contains(connected.Body.String(), f.token) {
		t.Fatal("connection secret leaked")
	}
	var session map[string]any
	if err := json.Unmarshal(connected.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	return session
}

func TestRemoteSSHHostPinningSessionOwnershipAndTerminal(t *testing.T) {
	f := newRemoteFixture(t)
	url := "/api/admin/devices/" + f.device + "/remote"
	requireStatus(t, remoteTestCall(f.app, nil, url, map[string]string{"action": "fingerprint"}), 401)
	wrongKey := remoteTestCall(f.app, f.cookie, url, map[string]any{"action": "connect", "host": f.host, "port": f.port, "username": testssh.Username, "password": testssh.Password, "fingerprint": "SHA256:untrusted"})
	requireStatus(t, wrongKey, 502)
	if !strings.Contains(wrongKey.Body.String(), "指纹") {
		t.Fatal(wrongKey.Body.String())
	}
	session := f.connect(t)
	id := session["id"].(string)
	ticket := session["terminalTicket"].(string)
	newLogin := httptest.NewRecorder()
	f.app.auth.issueSession(newLogin, httptest.NewRequest("POST", "http://console.localhost/login", nil), "owner")
	otherCookie := newLogin.Result().Cookies()[0]
	requireStatus(t, remoteTestCall(f.app, otherCookie, "/api/admin/remote/"+id+"/files", map[string]string{"action": "list"}), 404)
	headers := http.Header{"Cookie": {f.cookie.Name + "=" + f.cookie.Value}, "Origin": {f.serverURL}}
	conn, response, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(f.serverURL, "http")+"/api/admin/remote/"+id+"/terminal?ticket="+ticket, headers)
	if err != nil {
		t.Fatal(err, response)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	if err = conn.WriteJSON(map[string]any{"type": "resize", "cols": 120, "rows": 35}); err != nil {
		t.Fatal(err)
	}
	if err = conn.WriteJSON(map[string]string{"type": "input", "data": "测试 SSH\r"}); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	for !strings.Contains(output.String(), "SSH 通道已响应：测试 SSH") {
		_, body, err := conn.ReadMessage()
		if err != nil {
			t.Fatal("terminal did not respond", err, output.String())
		}
		output.Write(body)
	}
	replay, reply, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(f.serverURL, "http")+"/api/admin/remote/"+id+"/terminal?ticket="+ticket, headers)
	if err == nil {
		replay.Close()
		t.Fatal("terminal ticket replay accepted")
	}
	if reply.StatusCode != 409 {
		t.Fatal(reply.StatusCode)
	}
	reply.Body.Close()
	requireStatus(t, remoteTestCall(f.app, f.cookie, "/api/auth/logout", map[string]any{}), 204)
	requireStatus(t, remoteTestCall(f.app, otherCookie, "/api/admin/remote/"+id+"/files", map[string]string{"action": "list"}), 404)
}

func TestRemoteSFTPChunkedUploadDownloadAndMutations(t *testing.T) {
	f := newRemoteFixture(t)
	session := f.connect(t)
	id := session["id"].(string)
	url := "/api/admin/remote/" + id + "/files"
	list := remoteTestCall(f.app, f.cookie, url, map[string]string{"action": "list"})
	requireStatus(t, list, 200)
	// Exceed the old HTTP mapping's 16 MB body limit through streamed SSH data.
	data := bytes.Repeat([]byte("远程文件内容-0123456789\n"), 750000)
	start := remoteTestCall(f.app, f.cookie, url, map[string]any{"action": "upload-start", "path": "/", "name": "upload.bin", "size": len(data)})
	requireStatus(t, start, 200)
	var upload struct{ UploadID string }
	json.Unmarshal(start.Body.Bytes(), &upload)
	for offset := 0; offset < len(data); {
		end := offset + remoteUploadChunk
		if end > len(data) {
			end = len(data)
		}
		input := map[string]any{"action": "upload-part", "uploadId": upload.UploadID, "offset": offset, "data": data[offset:end]}
		reply := remoteTestCall(f.app, f.cookie, url, input)
		requireStatus(t, reply, 200)
		if offset == 0 {
			requireStatus(t, remoteTestCall(f.app, f.cookie, url, input), 200)
		}
		offset = end
	}
	requireStatus(t, remoteTestCall(f.app, f.cookie, url, map[string]string{"action": "upload-finish", "uploadId": upload.UploadID}), 200)
	prepared := remoteTestCall(f.app, f.cookie, url, map[string]string{"action": "download", "path": "/upload.bin"})
	requireStatus(t, prepared, 200)
	var download map[string]string
	json.Unmarshal(prepared.Body.Bytes(), &download)
	got := remoteTestCall(f.app, f.cookie, download["url"], nil)
	requireStatus(t, got, 200)
	if sha256.Sum256(got.Body.Bytes()) != sha256.Sum256(data) {
		t.Fatal("file bytes changed across SSH tunnel")
	}
	requireStatus(t, remoteTestCall(f.app, f.cookie, download["url"], nil), 410)
	requireStatus(t, remoteTestCall(f.app, f.cookie, url, map[string]string{"action": "mkdir", "path": "/", "name": "测试目录"}), 200)
	requireStatus(t, remoteTestCall(f.app, f.cookie, url, map[string]string{"action": "rename", "path": "/upload.bin", "name": "重命名.bin"}), 200)
	requireStatus(t, remoteTestCall(f.app, f.cookie, url, map[string]string{"action": "delete", "path": "/重命名.bin"}), 200)
	requireStatus(t, remoteTestCall(f.app, f.cookie, url, map[string]string{"action": "delete", "path": "/测试目录"}), 200)
	requireStatus(t, remoteTestCall(f.app, f.cookie, url, map[string]string{"action": "delete", "path": "/"}), 400)
	requireStatus(t, remoteTestCall(f.app, f.cookie, url, map[string]string{"action": "mkdir", "path": "/", "name": "../escape"}), 400)
}

func TestRemotePrivateKeyAuthAndPublicHTTPRejection(t *testing.T) {
	f := newRemoteFixture(t)
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	f.ssh.AllowKey(signer.PublicKey())
	block, err := ssh.MarshalPrivateKeyWithPassphrase(private, "fixture", []byte("fixture-key-password"))
	if err != nil {
		t.Fatal(err)
	}
	input := map[string]any{"action": "connect", "host": f.host, "port": f.port, "username": testssh.Username, "privateKey": string(pem.EncodeToMemory(block)), "passphrase": "fixture-key-password", "fingerprint": f.ssh.Fingerprint}
	url := "/api/admin/devices/" + f.device + "/remote"
	requireStatus(t, remoteTestCall(f.app, f.cookie, url, input), 200)
	snapshot, _ := json.Marshal(f.app.store.Snapshot())
	if bytes.Contains(snapshot, []byte("PRIVATE KEY")) || bytes.Contains(snapshot, []byte("fixture-key-password")) {
		t.Fatal("SSH credentials persisted")
	}
	input["passphrase"] = "wrong"
	requireStatus(t, remoteTestCall(f.app, f.cookie, url, input), 400)
	raw, _ := json.Marshal(input)
	request := httptest.NewRequest("POST", "http://console.example.com"+url, bytes.NewReader(raw))
	request.Header.Set("Origin", "http://console.example.com")
	request.AddCookie(f.cookie)
	response := httptest.NewRecorder()
	f.app.routes().ServeHTTP(response, request)
	requireStatus(t, response, 400)
}

func TestCanceledSFTPUploadKeepsOriginalFile(t *testing.T) {
	f := newRemoteFixture(t)
	session := f.connect(t)
	url := "/api/admin/remote/" + session["id"].(string) + "/files"
	create := func(name string, data []byte, replace bool) string {
		t.Helper()
		started := remoteTestCall(f.app, f.cookie, url, map[string]any{"action": "upload-start", "path": "/", "name": name, "size": len(data), "replace": replace})
		requireStatus(t, started, 200)
		var result struct{ UploadID string }
		json.Unmarshal(started.Body.Bytes(), &result)
		requireStatus(t, remoteTestCall(f.app, f.cookie, url, map[string]any{"action": "upload-part", "uploadId": result.UploadID, "offset": 0, "data": data}), 200)
		return result.UploadID
	}
	first := create("original.txt", []byte("original contents"), false)
	requireStatus(t, remoteTestCall(f.app, f.cookie, url, map[string]string{"action": "upload-finish", "uploadId": first}), 200)
	second := create("original.txt", []byte("replacement"), true)
	requireStatus(t, remoteTestCall(f.app, f.cookie, url, map[string]string{"action": "upload-cancel", "uploadId": second}), 200)
	prepared := remoteTestCall(f.app, f.cookie, url, map[string]string{"action": "download", "path": "/original.txt"})
	requireStatus(t, prepared, 200)
	var link map[string]string
	json.Unmarshal(prepared.Body.Bytes(), &link)
	download := remoteTestCall(f.app, f.cookie, link["url"], nil)
	requireStatus(t, download, 200)
	if download.Body.String() != "original contents" {
		t.Fatal("cancelled upload replaced original")
	}
	list := remoteTestCall(f.app, f.cookie, url, map[string]string{"action": "list", "path": "/"})
	requireStatus(t, list, 200)
	if strings.Contains(list.Body.String(), ".remotegate-upload-") {
		t.Fatal("cancelled upload left a temporary file")
	}
}
