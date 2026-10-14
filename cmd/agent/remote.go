package main

import (
	"encoding/json"
	"net"
	"net/url"
	"strings"
)

// Missing settings use the default; explicit false survives an update round trip.
func (cfg *Config) UnmarshalJSON(data []byte) error {
	type plainConfig Config
	decoded := plainConfig{RemoteAccess: true}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*cfg = Config(decoded)
	return nil
}

func remoteTransportAllowed(cfg Config) bool {
	if !cfg.RemoteAccess || cfg.InsecureTLS {
		return false
	}
	server, err := url.Parse(cfg.ServerURL)
	if err != nil {
		return false
	}
	if server.Scheme == "https" {
		return true
	}
	if server.Scheme != "http" {
		return false
	}
	if strings.EqualFold(server.Hostname(), "localhost") {
		return true
	}
	ip := net.ParseIP(server.Hostname())
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate())
}
