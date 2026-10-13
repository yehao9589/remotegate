package main

import (
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type operatorRoundTrip func(*http.Request) (*http.Response, error)

func (transport operatorRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) {
	return transport(r)
}
func operatorResponse(body string) *http.Response {
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}
func waitOperator(t *testing.T, resolver *operatorResolver, ip string) deviceNetwork {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		result := resolver.Lookup(ip)
		if result.Status != "pending" {
			return result
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("operator lookup did not settle")
	return deviceNetwork{}
}

func TestOperatorLookupSkipsNonPublicAddressesAndDisabledQueries(t *testing.T) {
	resolver := newOperatorResolver("", true)
	resolver.client = &http.Client{Transport: operatorRoundTrip(func(*http.Request) (*http.Response, error) {
		t.Error("non-public address was sent outside")
		return operatorResponse("{}"), nil
	})}
	for _, tc := range []struct{ ip, kind string }{
		{"127.0.0.1", "loopback"}, {"::1", "loopback"}, {"::ffff:127.0.0.1", "loopback"},
		{"10.1.2.3", "private"}, {"192.168.1.1", "private"}, {"169.254.1.2", "private"}, {"fd00::1", "private"}, {"fe80::1", "private"},
		{"100.100.1.2", "shared"}, {"192.0.2.1", "reserved"}, {"203.0.113.1", "reserved"}, {"198.18.1.1", "reserved"}, {"2001:db8::1", "reserved"}, {"224.0.0.1", "reserved"},
		{"invalid", "unknown"}, {"fe80::1%eth0", "unknown"},
	} {
		got := resolver.Lookup(tc.ip)
		if got.Kind != tc.kind || got.Status == "pending" {
			t.Fatal(tc, got)
		}
	}
	resolver.enabled = false
	if got := resolver.Lookup("1.1.1.1"); got.Status != "disabled" {
		t.Fatal(got)
	}
}

func TestOperatorLookupIsAsyncDeduplicatedCachedAndPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "network.json")
	resolver := newOperatorResolver(path, true)
	now := time.Now()
	resolver.now = func() time.Time { return now }
	var calls atomic.Int32
	started, release := make(chan struct{}, 2), make(chan struct{})
	resolver.client = &http.Client{Transport: operatorRoundTrip(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		started <- struct{}{}
		if r.URL.Scheme != "https" || r.URL.Host != "stat.ripe.net" || r.URL.Query().Get("resource") != "1.1.1.1" || r.Header.Get("Authorization") != "" {
			t.Error("unexpected lookup request", r.URL)
		}
		<-release
		return operatorResponse(`{"status":"ok","data":{"resource":"1.1.1.0/24","announced":true,"asns":[{"asn":13335,"holder":"CLOUDFLARENET - Cloudflare, Inc."}]}}`), nil
	})}
	if got := resolver.Lookup("1.1.1.1"); got.Status != "pending" {
		t.Fatal(got)
	}
	<-started
	for i := 0; i < 10; i++ {
		if got := resolver.Lookup("1.1.1.1"); got.Status != "pending" {
			t.Fatal(got)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("duplicate pending lookup", calls.Load())
	}
	close(release)
	got := waitOperator(t, resolver, "1.1.1.1")
	if got.Operator != "CLOUDFLARENET - Cloudflare, Inc." || got.Prefix != "1.1.1.0/24" || got.ASNs[0] != 13335 || got.Source != "RIPEstat" {
		t.Fatal(got)
	}
	restored := newOperatorResolver(path, true)
	if got := restored.Lookup("1.1.1.1"); got.Status != "known" {
		t.Fatal("cache not restored", got)
	}
	if calls.Load() != 1 {
		t.Fatal("cache missed")
	}
	now = now.Add(25 * time.Hour)
	if got := resolver.Lookup("1.1.1.1"); got.Status != "pending" {
		t.Fatal("expired cache reused", got)
	}
	waitOperator(t, resolver, "1.1.1.1")
	if calls.Load() != 2 {
		t.Fatal("expired result not refreshed", calls.Load())
	}
}

func TestOperatorLookupIPv6AndMultipleOrigins(t *testing.T) {
	resolver := newOperatorResolver("", true)
	resolver.client = &http.Client{Transport: operatorRoundTrip(func(*http.Request) (*http.Response, error) {
		return operatorResponse(`{"status":"ok","data":{"resource":"240e::/16","announced":true,"asns":[{"asn":4134,"holder":"CHINANET-BACKBONE"},{"asn":9808,"holder":"CMNET-GD"}]}}`), nil
	})}
	got := waitOperator(t, resolver, "240e:1234::1")
	if got.Operator != "中国电信 / 中国移动" || len(got.ASNs) != 2 {
		t.Fatal("lost IPv6 origins", got)
	}
	for holder, want := range map[string]string{"CHINANET-BACKBONE": "中国电信", "China Mobile Communications Corporation": "中国移动", "CHINA169-BACKBONE CHINA UNICOM": "中国联通", "CHINA BROADNET": "中国广电", "CERNET2": "教育网", "CSTNET": "科技网", "Example Cloud": "Example Cloud"} {
		if got := operatorName(holder); got != want {
			t.Fatal(holder, got, want)
		}
	}
}

func TestOperatorFailureAndIncorrectScopeDoNotGuessAndRetryLater(t *testing.T) {
	for _, body := range []string{
		`{"status":"ok","data":{"resource":"8.8.8.0/24","announced":true,"asns":[{"asn":13335,"holder":"CLOUDFLARENET"}]}}`,
		`{"status":"ok","data":{"resource":"1.1.1.0/24","announced":false,"asns":[{"asn":13335,"holder":"CLOUDFLARENET"}]}}`,
		`{"status":"ok","data":{"resource":"1.1.1.0/24","announced":true,"asns":[{"asn":13335,"holder":""}]}}`,
		"invalid JSON",
	} {
		resolver := newOperatorResolver("", true)
		var calls atomic.Int32
		now := time.Now()
		resolver.now = func() time.Time { return now }
		resolver.client = &http.Client{Transport: operatorRoundTrip(func(*http.Request) (*http.Response, error) { calls.Add(1); return operatorResponse(body), nil })}
		got := waitOperator(t, resolver, "1.1.1.1")
		if got.Status != "unknown" || got.Operator != "" || len(got.ASNs) != 0 {
			t.Fatal("guessed operator", got)
		}
		for i := 0; i < 5; i++ {
			resolver.Lookup("1.1.1.1")
		}
		if calls.Load() != 1 {
			t.Fatal("failure retried immediately")
		}
		now = now.Add(6 * time.Minute)
		waitOperator(t, resolver, "1.1.1.1")
		if calls.Load() != 2 {
			t.Fatal("failure never retried")
		}
	}
}

func TestOperatorLookupConcurrencyAndCacheAreBounded(t *testing.T) {
	resolver := newOperatorResolver("", true)
	release := make(chan struct{})
	var active, maxActive atomic.Int32
	resolver.client = &http.Client{Transport: operatorRoundTrip(func(*http.Request) (*http.Response, error) {
		count := active.Add(1)
		for {
			previous := maxActive.Load()
			if count <= previous || maxActive.CompareAndSwap(previous, count) {
				break
			}
		}
		<-release
		active.Add(-1)
		return operatorResponse("{}"), nil
	})}
	for _, ip := range []string{"1.1.1.1", "8.8.8.8", "9.9.9.9", "1.0.0.1"} {
		resolver.Lookup(ip)
	}
	deadline := time.Now().Add(time.Second)
	for active.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if active.Load() != 2 {
		t.Fatal("unexpected concurrency", active.Load())
	}
	close(release)
	waitOperator(t, resolver, "1.1.1.1")
	waitOperator(t, resolver, "8.8.8.8")
	if maxActive.Load() > 2 {
		t.Fatal("too many parallel queries")
	}
}
