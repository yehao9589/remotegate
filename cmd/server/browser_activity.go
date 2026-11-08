package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/local/remotegate/internal/config"
)

const browserActivityPath = "/.remotegate/activity/"
const browserActivityQuery = "__remotegate_activity"

type browserActivity struct {
	MappingID, DeviceID, Host string
	Touched, HiddenSince      time.Time
}

func browserHTMLRequest(r *http.Request) bool {
	destination := r.Header.Get("Sec-Fetch-Dest")
	return r.Method == http.MethodGet && (destination == "document" || destination == "iframe" || destination == "" && strings.Contains(r.Header.Get("Accept"), "text/html"))
}

func activityLive(item browserActivity, limits config.ConnectionLimits, now time.Time) bool {
	idle, background := limits.ActivityTimeouts()
	return (idle == 0 || now.Before(item.Touched.Add(time.Duration(idle)*time.Minute))) && (background == 0 || item.HiddenSince.IsZero() || now.Before(item.HiddenSince.Add(time.Duration(background)*time.Minute)))
}

func (a *app) newBrowserActivity(m config.Mapping) string {
	token := accessToken()
	if token == "" {
		return ""
	}
	now := time.Now()
	a.activityMu.Lock()
	defer a.activityMu.Unlock()
	if a.activities == nil {
		a.activities = make(map[[32]byte]browserActivity)
	}
	if len(a.activities) >= 4096 {
		// Prefer removing the oldest lease. Expired/removed pages cannot keep
		// reconnecting silently; the user can explicitly reload to resume.
		var oldest [32]byte
		var at time.Time
		for key, item := range a.activities {
			if at.IsZero() || item.Touched.Before(at) {
				oldest, at = key, item.Touched
			}
		}
		delete(a.activities, oldest)
	}
	a.activities[sha256.Sum256([]byte(token))] = browserActivity{MappingID: m.ID, DeviceID: m.DeviceID, Host: m.Host, Touched: now}
	return token
}

func (a *app) browserActivityValid(token string, m config.Mapping) bool {
	if !validAccessToken(token) {
		return false
	}
	a.activityMu.Lock()
	item, ok := a.activities[sha256.Sum256([]byte(token))]
	a.activityMu.Unlock()
	return ok && item.MappingID == m.ID && item.DeviceID == m.DeviceID && item.Host == m.Host && activityLive(item, a.store.DeviceConnectionLimits(m.DeviceID), time.Now())
}

func (a *app) browserActivityHTTP(w http.ResponseWriter, r *http.Request, m config.Mapping) {
	accessNoStore(w)
	if r.Method == http.MethodGet && (r.URL.Path == browserActivityPath+"guard.js" || r.URL.Path == browserActivityPath+"guard.css") {
		file, kind := "web/browser-activity.js", "text/javascript; charset=utf-8"
		if strings.HasSuffix(r.URL.Path, ".css") {
			file, kind = "web/browser-activity.css", "text/css; charset=utf-8"
		}
		body, _ := webFS.ReadFile(file)
		w.Header().Set("Content-Type", kind)
		_, _ = w.Write(body)
		return
	}
	if r.Method != http.MethodPost || r.URL.Path != browserActivityPath+"update" {
		http.NotFound(w, r)
		return
	}
	origin, err := mappingOrigin(m)
	actual, parseErr := parsePublicURL(r.Header.Get("Origin"))
	if err != nil || parseErr != nil || actual.String() != origin.String() || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		http.Error(w, "页面来源不匹配", http.StatusForbidden)
		return
	}
	var input struct {
		Token  string `json:"token"`
		Active bool   `json:"active"`
		Hidden bool   `json:"hidden"`
		Pause  bool   `json:"pause"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&input) != nil || !validAccessToken(input.Token) {
		http.Error(w, "无效的页面状态", 400)
		return
	}
	limits := a.store.DeviceConnectionLimits(m.DeviceID)
	now, key := time.Now(), sha256.Sum256([]byte(input.Token))
	a.activityMu.Lock()
	item, ok := a.activities[key]
	valid := ok && item.MappingID == m.ID && item.DeviceID == m.DeviceID && item.Host == m.Host && activityLive(item, limits, now)
	if valid {
		if input.Pause {
			delete(a.activities, key)
		} else {
			if input.Active && !input.Hidden {
				item.Touched = now
			}
			if input.Hidden {
				if item.HiddenSince.IsZero() {
					item.HiddenSince = now
				}
			} else {
				item.HiddenSince = time.Time{}
			}
			a.activities[key] = item
		}
	}
	a.activityMu.Unlock()
	if !valid {
		http.Error(w, "页面已暂停，请点击继续查看", http.StatusGone)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Strip only our own query argument, preserving other query bytes (including
// controller tokens and signatures) exactly as supplied by the application.
func withoutActivityQuery(r *http.Request) *http.Request {
	copy := r.Clone(r.Context())
	u := *r.URL
	parts := strings.Split(u.RawQuery, "&")
	kept := parts[:0]
	for _, part := range parts {
		key, _, _ := strings.Cut(part, "=")
		decoded, _ := url.QueryUnescape(key)
		if decoded != browserActivityQuery {
			kept = append(kept, part)
		}
	}
	u.RawQuery = strings.Join(kept, "&")
	copy.URL = &u
	return copy
}

var activityHead = regexp.MustCompile(`(?i)<head(?:\s[^>]*)?>`)
var activityScript = regexp.MustCompile(`(?i)<script(?:\s|>)`)

type activityBody struct {
	io.Reader
	io.Closer
}

type activityCapture struct {
	bytes.Buffer
	enabled bool
}

func (c *activityCapture) Write(p []byte) (int, error) {
	if c.enabled {
		return c.Buffer.Write(p)
	}
	return len(p), nil
}

func (a *app) prepareBrowserActivity(r *http.Request, m config.Mapping, resp *http.Response) {
	idle, background := a.store.DeviceConnectionLimits(m.DeviceID).ActivityTimeouts()
	if idle == 0 && background == 0 || !browserHTMLRequest(r) || resp.StatusCode != http.StatusOK || r.Header.Get("Range") != "" || strings.Contains(strings.ToLower(resp.Header.Get("Content-Disposition")), "attachment") {
		return
	}
	kind, params, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(kind, "text/html") || params["charset"] != "" && !strings.EqualFold(params["charset"], "utf-8") && !strings.EqualFold(params["charset"], "us-ascii") {
		return
	}
	original := resp.Body
	var reader io.Reader = original
	switch strings.ToLower(resp.Header.Get("Content-Encoding")) {
	case "", "identity":
	case "gzip":
		consumed := activityCapture{enabled: true}
		gz, err := gzip.NewReader(io.TeeReader(original, &consumed))
		if err != nil {
			resp.Body = &activityBody{Reader: io.MultiReader(bytes.NewReader(consumed.Bytes()), original), Closer: original}
			return
		}
		consumed.enabled = false
		consumed.Buffer = bytes.Buffer{}
		reader = gz
	default:
		return
	}
	// Read only the initial tags, not the complete page or a large response.
	buffered := bufio.NewReaderSize(reader, 4096)
	var prefix []byte
	position := -1
	for len(prefix) < 32<<10 {
		chunk, readErr := buffered.ReadSlice('>')
		prefix = append(prefix, chunk...)
		if match := activityHead.FindIndex(prefix); match != nil {
			position = match[1]
			break
		}
		if match := activityScript.FindIndex(prefix); match != nil {
			position = match[0]
			break
		}
		if readErr != nil && readErr != bufio.ErrBufferFull {
			break
		}
	}
	if position >= 0 {
		if token := a.newBrowserActivity(m); token != "" {
			guard := fmt.Sprintf(`<link rel="stylesheet" href="%sguard.css"><script src="%sguard.js" data-token="%s" data-idle="%d" data-background="%d"></script>`, browserActivityPath, browserActivityPath, token, idle*60, background*60)
			prefix = append(append(append([]byte{}, prefix[:position]...), []byte(guard)...), prefix[position:]...)
		}
	}
	resp.Body = &activityBody{Reader: io.MultiReader(bytes.NewReader(prefix), buffered), Closer: original}
	for _, name := range []string{"Content-Length", "Content-Encoding", "ETag", "Content-MD5", "Digest"} {
		resp.Header.Del(name)
	}
	resp.ContentLength = -1
	resp.Header.Set("Cache-Control", "no-store")
}
