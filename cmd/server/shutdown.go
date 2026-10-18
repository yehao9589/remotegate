package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// Finish active HTTP requests before main flushes cumulative traffic on a normal
// Docker stop/update. WebSocket channels remain sampled until the final flush.
func (a *app) serveUntilStopped(server *http.Server) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ended := make(chan error, 1)
	go func() { ended <- server.ListenAndServe() }()
	var failure error
	select {
	case err := <-ended:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Printf("HTTP 服务退出：%v", err)
			failure = err
		}
	case <-ctx.Done():
	}
	timeout, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	a.certMu.Lock()
	https := a.httpsServer
	a.certMu.Unlock()
	done := make(chan struct{})
	go func() {
		if https != nil {
			if err := https.Shutdown(timeout); err != nil {
				_ = https.Close()
			}
		}
		close(done)
	}()
	if err := server.Shutdown(timeout); err != nil {
		_ = server.Close()
	}
	<-done
	return failure
}
