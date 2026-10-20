package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/local/remotegate"
	"github.com/local/remotegate/internal/buildinfo"
)

const officialReleaseAPI = "https://api.github.com/repos/yehao9589/remotegate/releases/latest"
const officialReleasePage = "https://github.com/yehao9589/remotegate/releases/tag/"

type serverRelease struct {
	Version     string `json:"version"`
	Title       string `json:"title"`
	PublishedAt string `json:"publishedAt"`
	Notes       string `json:"notes"`
	URL         string `json:"url"`
}
type serverReleaseResult struct {
	Current       buildinfo.Info  `json:"current"`
	History       []serverRelease `json:"history"`
	Latest        *serverRelease  `json:"latest,omitempty"`
	Status        string          `json:"status"`
	Message       string          `json:"message,omitempty"`
	CheckedAt     time.Time       `json:"checkedAt"`
	LastAttemptAt time.Time       `json:"lastAttemptAt"`
}
type releaseChecker struct {
	mu                     sync.Mutex
	client                 *http.Client
	endpoint               string
	latest                 *serverRelease
	checkedAt, attemptedAt time.Time
	err                    string
	flight                 chan struct{}
	now                    func() time.Time
}

func newReleaseChecker() *releaseChecker {
	return &releaseChecker{client: &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, endpoint: officialReleaseAPI, now: time.Now}
}

var stableVersion = regexp.MustCompile(`^v?(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})$`)

func releaseVersion(value string) (string, bool) {
	if !stableVersion.MatchString(value) {
		return "", false
	}
	return strings.TrimPrefix(value, "v"), true
}
func compareReleaseVersions(a, b string) int {
	left, right := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < 3; i++ {
		x, _ := strconv.Atoi(left[i])
		y, _ := strconv.Atoi(right[i])
		if x > y {
			return 1
		}
		if x < y {
			return -1
		}
	}
	return 0
}
func localReleaseHistory() []serverRelease {
	entries := []serverRelease{}
	var current *serverRelease
	for _, line := range strings.Split(strings.ReplaceAll(remotegate.ReleaseNotes, "\r\n", "\n"), "\n") {
		if strings.HasPrefix(line, "## ") {
			if current != nil {
				current.Notes = strings.TrimSpace(current.Notes)
				entries = append(entries, *current)
				current = nil
			}
			heading := strings.TrimPrefix(line, "## ")
			tag, date, _ := strings.Cut(heading, " · ")
			if version, ok := releaseVersion(tag); ok {
				current = &serverRelease{Version: version, Title: "RemoteGate v" + version, PublishedAt: date, URL: officialReleasePage + "v" + version}
			}
		} else if current != nil {
			current.Notes += line + "\n"
		}
	}
	if current != nil {
		current.Notes = strings.TrimSpace(current.Notes)
		entries = append(entries, *current)
	}
	return entries
}

func (c *releaseChecker) resultLocked() serverReleaseResult {
	result := serverReleaseResult{Current: buildinfo.Current(), History: localReleaseHistory(), Status: "unknown", Message: c.err, CheckedAt: c.checkedAt, LastAttemptAt: c.attemptedAt}
	if c.latest != nil {
		copy := *c.latest
		result.Latest = &copy
		if c.err == "" {
			switch compareReleaseVersions(copy.Version, buildinfo.Version()) {
			case 1:
				result.Status = "available"
			case 0:
				result.Status = "current"
			case -1:
				result.Status = "ahead"
			}
		}
	}
	return result
}

func (c *releaseChecker) check(ctx context.Context, force bool) (serverReleaseResult, error) {
	c.mu.Lock()
	if running := c.flight; running != nil {
		c.mu.Unlock()
		select {
		case <-running:
			c.mu.Lock()
			result := c.resultLocked()
			c.mu.Unlock()
			return result, nil
		case <-ctx.Done():
			return serverReleaseResult{}, ctx.Err()
		}
	}
	wait := 6 * time.Hour
	if c.err != "" {
		wait = 5 * time.Minute
	}
	if force {
		wait = time.Minute
	}
	if !c.attemptedAt.IsZero() && c.now().Sub(c.attemptedAt) < wait {
		result := c.resultLocked()
		c.mu.Unlock()
		return result, nil
	}
	c.flight = make(chan struct{})
	running := c.flight
	c.mu.Unlock()
	// One closed browser must not cancel a check shared by other administrators.
	requestCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 8*time.Second)
	latest, err := c.fetch(requestCtx)
	cancel()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.attemptedAt = c.now().UTC()
	if err != nil {
		c.err = err.Error()
	} else {
		c.latest = latest
		c.checkedAt = c.attemptedAt
		c.err = ""
	}
	close(running)
	c.flight = nil
	return c.resultLocked(), nil
}

func (c *releaseChecker) fetch(ctx context.Context) (*serverRelease, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint, nil)
	if err != nil {
		return nil, errors.New("无法创建版本检测请求")
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "RemoteGate/"+buildinfo.Version())
	// No administrator cookie, device token, IP or local configuration is sent.
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, errors.New("无法连接版本服务，请检查服务器网络后重试")
	}
	defer resp.Body.Close()
	if resp.StatusCode == 403 || resp.StatusCode == 429 {
		return nil, errors.New("版本服务请求受限，请稍后重试")
	}
	if resp.StatusCode == 404 {
		return nil, errors.New("暂未获取到正式发布版本，请稍后重试")
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("版本服务暂不可用（HTTP %d），请稍后重试", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (768<<10)+1))
	if err != nil || len(data) > 768<<10 {
		return nil, errors.New("版本说明读取失败，请稍后重试")
	}
	var in struct {
		Tag         string `json:"tag_name"`
		Name        string `json:"name"`
		Body        string `json:"body"`
		PublishedAt string `json:"published_at"`
		Draft       bool   `json:"draft"`
		Prerelease  bool   `json:"prerelease"`
	}
	if json.Unmarshal(data, &in) != nil {
		return nil, errors.New("版本服务返回的数据无效")
	}
	version, ok := releaseVersion(in.Tag)
	if !ok || in.Draft || in.Prerelease || !strings.HasPrefix(in.Tag, "v") {
		return nil, errors.New("未获取到有效的正式发布版本")
	}
	at, err := time.Parse(time.RFC3339, in.PublishedAt)
	if err != nil || at.IsZero() {
		return nil, errors.New("正式版本缺少有效的发布时间")
	}
	if len(in.Name) > 300 || len(in.Body) > 512<<10 {
		return nil, errors.New("版本说明超出读取范围")
	}
	return &serverRelease{Version: version, Title: in.Name, PublishedAt: at.UTC().Format(time.RFC3339), Notes: in.Body, URL: officialReleasePage + in.Tag}, nil
}

func (a *app) checkServerRelease(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	a.releaseMu.Lock()
	if a.releases == nil {
		a.releases = newReleaseChecker()
	}
	checker := a.releases
	a.releaseMu.Unlock()
	result, err := checker.check(r.Context(), r.Method == http.MethodPost)
	if err != nil {
		http.Error(w, "版本检测已取消", http.StatusRequestTimeout)
		return
	}
	writeJSON(w, result)
}

func (a *app) serverReleaseNotes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	writeJSON(w, serverReleaseResult{Current: buildinfo.Current(), History: localReleaseHistory(), Status: "unchecked"})
}
