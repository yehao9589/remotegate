//go:build !linux

package main

import (
	"net"
	"time"
)

func boundDialer(_ string, _ int) *net.Dialer {
	return &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
}
