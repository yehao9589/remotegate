package main

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/url"
)

func dialHTTPTarget(ctx context.Context, cfg Config, target string) (net.Conn, error) {
	u, err := url.Parse(target)
	if err != nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, errors.New("invalid HTTP mapping target")
	}
	port := u.Port()
	if port == "" {
		port = "80"
		if u.Scheme == "https" {
			port = "443"
		}
	}
	dial := (&net.Dialer{}).DialContext
	if !cfg.AllowPublicTargets {
		dial = (privateHTTPDialer{lookup: net.DefaultResolver.LookupIPAddr, dial: dial}).DialContext
	}
	conn, err := dial(ctx, "tcp", net.JoinHostPort(u.Hostname(), port))
	if err != nil {
		return nil, err
	}
	if u.Scheme == "https" {
		// Same private-service certificate policy as targetHTTPClient.
		secure := tls.Client(conn, &tls.Config{ServerName: u.Hostname(), MinVersion: tls.VersionTLS12, InsecureSkipVerify: true})
		if err := secure.HandshakeContext(ctx); err != nil {
			conn.Close()
			return nil, err
		}
		return secure, nil
	}
	return conn, nil
}
