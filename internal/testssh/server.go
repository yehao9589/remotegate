// Package testssh provides an isolated, in-memory SSH/SFTP fixture.
// Its shell echoes input and never executes operating-system commands.
package testssh

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

const Username = "fixture"
const Password = "fixture-password"

type Server struct {
	listener    net.Listener
	config      *ssh.ServerConfig
	handlers    sftp.Handlers
	mu          sync.Mutex
	connections map[net.Conn]bool
	closed      bool
	keys        map[string]bool
	wg          sync.WaitGroup
	Fingerprint string
}

func New() (*Server, error) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	config := &ssh.ServerConfig{PasswordCallback: func(metadata ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
		if metadata.User() == Username && string(password) == Password {
			return nil, nil
		}
		return nil, errors.New("fixture authentication rejected")
	}}
	config.AddHostKey(signer)
	server := &Server{listener: listener, config: config, handlers: sftp.InMemHandler(), connections: make(map[net.Conn]bool), keys: make(map[string]bool), Fingerprint: ssh.FingerprintSHA256(signer.PublicKey())}
	config.PublicKeyCallback = func(metadata ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		server.mu.Lock()
		allowed := metadata.User() == Username && server.keys[string(key.Marshal())]
		server.mu.Unlock()
		if allowed {
			return nil, nil
		}
		return nil, errors.New("fixture key rejected")
	}
	server.wg.Add(1)
	go server.accept()
	return server, nil
}

func (s *Server) Address() string { return s.listener.Addr().String() }

// WrapSFTP changes handlers for subsequently opened SFTP channels.
func (s *Server) WrapSFTP(wrap func(sftp.Handlers) sftp.Handlers) {
	s.mu.Lock()
	s.handlers = wrap(s.handlers)
	s.mu.Unlock()
}
func (s *Server) AllowKey(key ssh.PublicKey) {
	s.mu.Lock()
	s.keys[string(key.Marshal())] = true
	s.mu.Unlock()
}
func (s *Server) accept() {
	defer s.wg.Done()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			conn.Close()
			return
		}
		s.connections[conn] = true
		s.wg.Add(1)
		s.mu.Unlock()
		go s.serve(conn)
	}
}
func (s *Server) serve(raw net.Conn) {
	defer s.wg.Done()
	defer raw.Close()
	defer func() { s.mu.Lock(); delete(s.connections, raw); s.mu.Unlock() }()
	_ = raw.SetDeadline(time.Now().Add(20 * time.Second))
	conn, channels, requests, err := ssh.NewServerConn(raw, s.config)
	if err != nil {
		return
	}
	defer conn.Close()
	_ = raw.SetDeadline(time.Time{})
	go ssh.DiscardRequests(requests)
	for request := range channels {
		if request.ChannelType() != "session" {
			_ = request.Reject(ssh.UnknownChannelType, "session required")
			continue
		}
		channel, requests, err := request.Accept()
		if err != nil {
			continue
		}
		s.wg.Add(1)
		go s.serveChannel(channel, requests)
	}
}
func (s *Server) serveChannel(channel ssh.Channel, requests <-chan *ssh.Request) {
	defer s.wg.Done()
	defer channel.Close()
	shellStarted := false
	for request := range requests {
		switch request.Type {
		case "pty-req", "window-change":
			_ = request.Reply(true, nil)
		case "shell":
			if shellStarted {
				_ = request.Reply(false, nil)
				continue
			}
			shellStarted = true
			_ = request.Reply(true, nil)
			s.wg.Add(1)
			go func() { defer s.wg.Done(); echoShell(channel); channel.Close() }()
		case "subsystem":
			var payload struct{ Name string }
			if ssh.Unmarshal(request.Payload, &payload) != nil || payload.Name != "sftp" {
				_ = request.Reply(false, nil)
				continue
			}
			_ = request.Reply(true, nil)
			s.mu.Lock()
			handlers := s.handlers
			s.mu.Unlock()
			server := sftp.NewRequestServer(channel, handlers)
			_ = server.Serve()
			_ = server.Close()
			return
		default:
			_ = request.Reply(false, nil)
		}
	}
}
func echoShell(channel ssh.Channel) {
	_, _ = io.WriteString(channel, "\r\nRemoteGate SSH / SFTP 本地测试\r\n此终端仅回显，不执行系统命令。\r\nfixture# ")
	var line strings.Builder
	buffer := make([]byte, 4096)
	for {
		n, err := channel.Read(buffer)
		for _, value := range buffer[:n] {
			if value == 3 {
				line.Reset()
				_, _ = io.WriteString(channel, "^C\r\nfixture# ")
			} else if value == '\r' || value == '\n' {
				if line.Len() != 0 {
					_, _ = io.WriteString(channel, "\r\nSSH 通道已响应："+line.String()+"\r\nfixture# ")
					line.Reset()
				}
			} else {
				line.WriteByte(value)
				_, _ = channel.Write([]byte{value})
			}
		}
		if err != nil {
			return
		}
	}
}
func (s *Server) Close() {
	s.mu.Lock()
	s.closed = true
	_ = s.listener.Close()
	for conn := range s.connections {
		_ = conn.Close()
	}
	s.mu.Unlock()
	s.wg.Wait()
}
