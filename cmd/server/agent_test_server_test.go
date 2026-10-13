package main

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// httptest.Server.Close does not wait for hijacked WebSocket handlers. Join
// those handlers before TempDir cleanup removes the lifecycle record files.
func agentTestServer(t *testing.T, a *app) *httptest.Server {
	t.Helper()
	var handlers sync.WaitGroup
	routes := a.routes()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlers.Add(1)
		defer handlers.Done()
		routes.ServeHTTP(w, r)
	}))
	t.Cleanup(func() {
		server.Close()
		for id := range a.hub.Statuses() {
			a.hub.Disconnect(id)
		}
		done := make(chan struct{})
		go func() { handlers.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("agent handlers did not finish before test cleanup")
		}
	})
	return server
}
