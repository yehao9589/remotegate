//go:build linux

package main

import (
	"net"
	"syscall"
	"time"
)

func boundDialer(iface string, mark int) *net.Dialer {
	d := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	if iface == "" && mark == 0 {
		return d
	}
	d.Control = func(_, _ string, c syscall.RawConn) error {
		var controlErr error
		err := c.Control(func(fd uintptr) {
			if iface != "" {
				controlErr = syscall.SetsockoptString(int(fd), syscall.SOL_SOCKET, syscall.SO_BINDTODEVICE, iface)
			}
			if controlErr == nil && mark != 0 {
				controlErr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_MARK, mark)
			}
		})
		if err != nil {
			return err
		}
		return controlErr
	}
	return d
}
