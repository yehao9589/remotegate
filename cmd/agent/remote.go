package main

import (
	"net"
	"net/url"
	"strings"
)

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
