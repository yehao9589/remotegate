//go:build !linux

package main

import (
	"context"
	"errors"
	"net"
	"time"
)

type directWANDialer struct{}

func newDirectWANDialer(Config) *directWANDialer { return &directWANDialer{} }
func (*directWANDialer) DialContext(context.Context, string, string) (net.Conn, error) {
	return nil, errors.New("WAN 直连保护仅适用于 Linux 路由器")
}
func maintainDirectRoute(context.Context, Config) {}

func boundDialer(_ string, _ int) *net.Dialer {
	return &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
}
