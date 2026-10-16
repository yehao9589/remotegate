package main

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"time"
)

type privateHTTPDialer struct {
	lookup func(context.Context, string) ([]net.IPAddr, error)
	dial   func(context.Context, string, string) (net.Conn, error)
}

func (d privateHTTPDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	addresses, err := d.lookup(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(addresses) == 0 {
		return nil, errors.New("no addresses for mapping target")
	}
	for _, address := range addresses {
		ip := address.IP
		if !ip.IsLoopback() && !ip.IsPrivate() && !ip.IsLinkLocalUnicast() {
			return nil, errors.New("target is outside private address space")
		}
	}
	// Connect to the checked address. Resolving the hostname again here would
	// allow a changing DNS answer to bypass the private-target restriction.
	for _, address := range addresses {
		ip := address.IP.String()
		if address.Zone != "" {
			ip += "%" + address.Zone
		}
		var conn net.Conn
		conn, err = d.dial(ctx, network, net.JoinHostPort(ip, port))
		if err == nil {
			return conn, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return nil, err
}

func targetHTTPClient(cfg Config) *http.Client {
	dial := (&net.Dialer{Timeout: 10 * time.Second}).DialContext
	if !cfg.AllowPublicTargets {
		dial = (privateHTTPDialer{lookup: net.DefaultResolver.LookupIPAddr, dial: dial}).DialContext
	}
	return &http.Client{
		Transport: &http.Transport{Proxy: nil, DialContext: dial, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true}},
		Timeout:   55 * time.Second,
		// Return redirects to the browser, including the target's login cookies.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}
