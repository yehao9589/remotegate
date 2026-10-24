package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/local/remotegate/internal/buildinfo"
)

func releaseFixture(t *testing.T, handler http.HandlerFunc) *releaseChecker {
	t.Helper()
	s := httptest.NewServer(handler)
	t.Cleanup(s.Close)
	c := newReleaseChecker()
	c.endpoint = s.URL
	c.client = s.Client()
	return c
}
func writeRelease(w http.ResponseWriter, version string) {
	fmt.Fprintf(w, `{"tag_name":%q,"name":"更新说明","body":"- 修复连接问题\n### 升级说明\n保留数据目录。","published_at":"2026-10-19T12:00:00Z"}`, version)
}

func TestReleaseChecksShareCacheForceRefreshAndKeepLastKnownNotes(t *testing.T) {
	var calls atomic.Int32
	var unavailable atomic.Bool
	c := releaseFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" || r.URL.RawQuery != "" {
			t.Error("private credentials forwarded")
		}
		if unavailable.Load() {
			http.Error(w, "limited", 429)
			return
		}
		writeRelease(w, "v9.10.0")
	})
	now := time.Now()
	c.now = func() time.Time { return now }
	first, err := c.check(context.Background(), false)
	if err != nil || first.Status != "available" || first.Latest.Version != "9.10.0" || len(first.History) == 0 {
		t.Fatal("invalid first result", first, err)
	}
	for i := 0; i < 10; i++ {
		c.check(context.Background(), false)
		c.check(context.Background(), true)
	}
	if calls.Load() != 1 {
		t.Fatal("cache not shared", calls.Load())
	}
	first.Latest.Version = "mutated"
	got, _ := c.check(context.Background(), false)
	if got.Latest.Version != "9.10.0" {
		t.Fatal("mutable cache alias")
	}
	now = now.Add(time.Minute)
	unavailable.Store(true)
	got, _ = c.check(context.Background(), true)
	if got.Status != "unknown" || !strings.Contains(got.Message, "受限") || got.Latest.Notes == "" || !got.CheckedAt.Equal(first.CheckedAt) || len(got.History) == 0 {
		t.Fatal("error claimed up to date or lost offline notes", got)
	}
	c.check(context.Background(), false)
	if calls.Load() != 2 {
		t.Fatal("error retry not cached")
	}
	now = now.Add(5 * time.Minute)
	unavailable.Store(false)
	got, _ = c.check(context.Background(), false)
	if got.Status != "available" || got.Message != "" || calls.Load() != 3 {
		t.Fatal("retry did not recover", got)
	}
}

func TestReleaseValidationAndNumericComparison(t *testing.T) {
	if compareReleaseVersions("0.1.10", "0.1.9") != 1 || compareReleaseVersions("1.0.0", "0.99.99") != 1 || compareReleaseVersions("0.1.15", "0.1.15") != 0 {
		t.Fatal("lexicographic version comparison")
	}
	for _, bad := range []string{"v1.2.3-beta", "v01.2.3", "v1.2", "v1.2.3/<script>", "v10000000000000.1.0"} {
		if _, ok := releaseVersion(bad); ok {
			t.Fatal("invalid version", bad)
		}
	}
	for _, body := range []string{
		`{"tag_name":"v9.0.0","draft":true,"published_at":"2026-10-19T12:00:00Z"}`,
		`{"tag_name":"v9.0.0","prerelease":true,"published_at":"2026-10-19T12:00:00Z"}`,
		`{"tag_name":"v9.0.0"}`,
		`{"tag_name":"javascript:alert(1)"}`,
		`<html>bad gateway</html>`,
		strings.Repeat("x", (768<<10)+1),
	} {
		c := releaseFixture(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) })
		got, _ := c.check(context.Background(), false)
		if got.Status != "unknown" || got.Latest != nil || got.Message == "" {
			t.Fatal("untrusted response accepted", got)
		}
	}
	for _, test := range []struct{ tag, want string }{{"v" + buildinfo.Version(), "current"}, {"v0.0.0", "ahead"}} {
		c := releaseFixture(t, func(w http.ResponseWriter, r *http.Request) { writeRelease(w, test.tag) })
		got, _ := c.check(context.Background(), false)
		if got.Status != test.want {
			t.Fatal("wrong release status", got.Status, test.want)
		}
	}
}

func TestReleaseCheckConcurrentRequestsUseOneFetch(t *testing.T) {
	var calls atomic.Int32
	c := releaseFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		time.Sleep(30 * time.Millisecond)
		writeRelease(w, "v9.0.0")
	})
	var workers sync.WaitGroup
	for i := 0; i < 12; i++ {
		workers.Go(func() {
			result, err := c.check(context.Background(), true)
			if err != nil || result.Status != "available" {
				t.Error("concurrent check failed", err)
			}
		})
	}
	workers.Wait()
	if calls.Load() != 1 {
		t.Fatal("duplicate remote checks", calls.Load())
	}
}

func TestReleaseEndpointRequiresAdminAndValidOrigin(t *testing.T) {
	a := freshAuthApp(t)
	a.releases = releaseFixture(t, func(w http.ResponseWriter, r *http.Request) { writeRelease(w, "v9.0.0") })
	login := authCall(a, "/api/auth/setup", setupBody, "", nil)
	requireStatus(t, login, 200)
	cookie := login.Result().Cookies()[0]
	requireStatus(t, authCall(a, "/api/admin/version/check", "", "", nil), 401)
	requireStatus(t, authCall(a, "/api/admin/version/check", "{}", "https://evil.example", cookie), 403)
	requireStatus(t, authCall(a, "/api/admin/version/notes", "", "", nil), 401)
	requireStatus(t, authCall(a, "/api/admin/version/notes", "{}", "", cookie), 405)
	notes := authCall(a, "/api/admin/version/notes", "", "", cookie)
	requireStatus(t, notes, 200)
	if !strings.Contains(notes.Body.String(), `"status":"unchecked"`) || a.releases.latest != nil {
		t.Fatal("offline notes performed a release check")
	}
	result := authCall(a, "/api/admin/version/check", "", "", cookie)
	requireStatus(t, result, 200)
	var data serverReleaseResult
	if json.Unmarshal(result.Body.Bytes(), &data) != nil || data.Status != "available" || data.Current.Version != buildinfo.Version() {
		t.Fatal("invalid endpoint result")
	}
	changelog, err := os.ReadFile("../../CHANGELOG.md")
	if err != nil {
		t.Fatal(err)
	}
	_, section, exists := strings.Cut(strings.ReplaceAll(string(changelog), "\r\n", "\n"), "## v"+buildinfo.Version()+" · ")
	_, expectedNotes, hasDate := strings.Cut(section, "\n")
	expectedNotes = strings.TrimSpace(strings.SplitN(expectedNotes, "\n## ", 2)[0])
	if !exists || !hasDate || expectedNotes == "" {
		t.Fatal("current release is missing its changelog entry")
	}
	found := false
	for _, entry := range data.History {
		if entry.Version == buildinfo.Version() && entry.Notes == expectedNotes {
			found = true
		}
	}
	if !found {
		t.Fatal("current notes not embedded from changelog")
	}
	r := httptest.NewRequest(http.MethodDelete, "http://console.localhost/api/admin/version/check", nil)
	r.AddCookie(cookie)
	r.Header.Set("Origin", "http://console.localhost")
	r.Header.Set("X-Admin-Entry", a.auth.entryPath())
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, r)
	requireStatus(t, w, 405)
}
