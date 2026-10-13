package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

const operatorEndpoint = "https://stat.ripe.net/data/prefix-overview/data.json"
const operatorCacheLimit = 256

type deviceNetwork struct {
	IP           string    `json:"ip,omitempty"`
	Kind         string    `json:"kind"`
	Status       string    `json:"status"`
	Operator     string    `json:"operator,omitempty"`
	Organization string    `json:"organization,omitempty"`
	ASNs         []uint32  `json:"asns,omitempty"`
	Prefix       string    `json:"prefix,omitempty"`
	Source       string    `json:"source,omitempty"`
	CheckedAt    time.Time `json:"checkedAt,omitempty"`
}

type operatorCacheEntry struct {
	Network   deviceNetwork `json:"network"`
	ExpiresAt time.Time     `json:"expiresAt"`
}

type operatorResolver struct {
	mu        sync.Mutex
	client    *http.Client
	enabled   bool
	cachePath string
	cache     map[string]operatorCacheEntry
	pending   map[string]bool
	slots     chan struct{}
	now       func() time.Time
}

var nonPublicNetworks = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("240.0.0.0/4"), netip.MustParsePrefix("2001:db8::/32"),
}
var sharedAddressSpace = netip.MustParsePrefix("100.64.0.0/10")

func networkForIP(value string) deviceNetwork {
	out := deviceNetwork{IP: value, Kind: "unknown", Status: "unknown"}
	ip, err := netip.ParseAddr(value)
	if err != nil || ip.Zone() != "" {
		return out
	}
	ip = ip.Unmap()
	out.IP = ip.String()
	switch {
	case ip.IsLoopback():
		out.Kind = "loopback"
	case ip.IsPrivate() || ip.IsLinkLocalUnicast():
		out.Kind = "private"
	case sharedAddressSpace.Contains(ip):
		out.Kind = "shared"
	case !ip.IsGlobalUnicast():
		out.Kind = "reserved"
	default:
		out.Kind = "public"
		for _, prefix := range nonPublicNetworks {
			if prefix.Contains(ip) {
				out.Kind = "reserved"
				break
			}
		}
	}
	if out.Kind != "public" {
		out.Status = "local"
	}
	return out
}

func newOperatorResolver(path string, enabled bool) *operatorResolver {
	resolver := &operatorResolver{
		client:  &http.Client{Timeout: 6 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("operator lookup redirect rejected") }},
		enabled: enabled, cachePath: path, cache: make(map[string]operatorCacheEntry),
		pending: make(map[string]bool), slots: make(chan struct{}, 2), now: time.Now,
	}
	// Cached public-network metadata is independent of device credentials and state.
	if raw, err := os.ReadFile(path); err == nil && len(raw) < 1<<20 {
		var entries []operatorCacheEntry
		if json.Unmarshal(raw, &entries) == nil {
			for _, entry := range entries {
				base := networkForIP(entry.Network.IP)
				if len(resolver.cache) >= operatorCacheLimit {
					break
				}
				if base.Kind == "public" && entry.ExpiresAt.After(resolver.now()) && entry.Network.Status == "known" && len(entry.Network.Operator) <= 512 {
					entry.Network.Kind = "public"
					resolver.cache[base.IP] = entry
				}
			}
		}
	}
	return resolver
}

// Lookup returns immediately; polling never waits for the external data service.
func (resolver *operatorResolver) Lookup(value string) deviceNetwork {
	base := networkForIP(value)
	if base.Kind != "public" {
		return base
	}
	if resolver == nil || !resolver.enabled {
		base.Status = "disabled"
		return base
	}
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	now := resolver.now()
	if entry, ok := resolver.cache[base.IP]; ok && entry.ExpiresAt.After(now) {
		return entry.Network
	}
	base.Status = "pending"
	if resolver.pending[base.IP] {
		return base
	}
	select {
	case resolver.slots <- struct{}{}:
	default:
		return base
	}
	resolver.pending[base.IP] = true
	go func() {
		defer func() { <-resolver.slots }()
		result := resolver.query(base.IP)
		duration := 24 * time.Hour
		if result.Status != "known" {
			duration = 5 * time.Minute
		}
		resolver.mu.Lock()
		defer resolver.mu.Unlock()
		delete(resolver.pending, base.IP)
		resolver.cache[base.IP] = operatorCacheEntry{Network: result, ExpiresAt: resolver.now().Add(duration)}
		if len(resolver.cache) > operatorCacheLimit {
			oldest := ""
			var earliest time.Time
			for ip, entry := range resolver.cache {
				if oldest == "" || entry.ExpiresAt.Before(earliest) {
					oldest, earliest = ip, entry.ExpiresAt
				}
			}
			delete(resolver.cache, oldest)
		}
		resolver.persistLocked()
	}()
	return base
}

func (resolver *operatorResolver) persistLocked() {
	if resolver.cachePath == "" {
		return
	}
	entries := make([]operatorCacheEntry, 0, len(resolver.cache))
	for _, entry := range resolver.cache {
		if entry.Network.Status == "known" && entry.ExpiresAt.After(resolver.now()) {
			entries = append(entries, entry)
		}
	}
	raw, err := json.Marshal(entries)
	if err != nil {
		return
	}
	tmp := resolver.cachePath + ".tmp"
	if os.WriteFile(tmp, raw, 0600) == nil {
		_ = os.Rename(tmp, resolver.cachePath)
	}
}

func (resolver *operatorResolver) query(ip string) deviceNetwork {
	result := networkForIP(ip)
	result.Status = "unknown"
	result.CheckedAt = resolver.now().UTC()
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, operatorEndpoint+"?resource="+url.QueryEscape(ip)+"&max_related=0", nil)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "RemoteGate-IP-Network-Lookup")
	response, err := resolver.client.Do(request)
	if err != nil {
		return result
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return result
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 128*1024+1))
	if err != nil || len(raw) > 128*1024 {
		return result
	}
	var payload struct {
		Status string `json:"status"`
		Data   struct {
			Resource  string `json:"resource"`
			Announced bool   `json:"announced"`
			ASNs      []struct {
				ASN    uint32 `json:"asn"`
				Holder string `json:"holder"`
			} `json:"asns"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &payload) != nil || payload.Status != "ok" || !payload.Data.Announced || len(payload.Data.ASNs) == 0 || len(payload.Data.ASNs) > 8 {
		return result
	}
	prefix, err := netip.ParsePrefix(payload.Data.Resource)
	address, _ := netip.ParseAddr(ip)
	if err != nil || !prefix.Contains(address) {
		return result
	}
	operators, organizations := make(map[string]bool), make(map[string]bool)
	asns := make([]uint32, 0, len(payload.Data.ASNs))
	for _, as := range payload.Data.ASNs {
		holder := strings.Join(strings.Fields(as.Holder), " ")
		if as.ASN == 0 || holder == "" || len(holder) > 200 {
			return result
		}
		asns = append(asns, as.ASN)
		operators[operatorName(holder)] = true
		organizations[holder] = true
	}
	names := func(values map[string]bool) string {
		list := make([]string, 0, len(values))
		for value := range values {
			list = append(list, value)
		}
		sort.Strings(list)
		return strings.Join(list, " / ")
	}
	result.Operator, result.Organization = names(operators), names(organizations)
	result.ASNs = asns
	result.Prefix, result.Source, result.Status = prefix.String(), "RIPEstat", "known"
	return result
}

func operatorName(holder string) string {
	upper := strings.ToUpper(holder)
	switch {
	case strings.Contains(upper, "CHINANET"), strings.Contains(upper, "CHINA TELECOM"), strings.Contains(upper, "CHINA-TELECOM"), strings.Contains(upper, "CHINATELECOM"), strings.Contains(holder, "中国电信"):
		return "中国电信"
	case strings.Contains(upper, "CHINA MOBILE"), strings.Contains(upper, "CHINAMOBILE"), strings.Contains(upper, "CMNET"), strings.Contains(holder, "中国移动"):
		return "中国移动"
	case strings.Contains(upper, "CHINA UNICOM"), strings.Contains(upper, "CHINAUNICOM"), strings.Contains(upper, "CNCGROUP"), strings.Contains(holder, "中国联通"):
		return "中国联通"
	case strings.Contains(upper, "CHINA BROADNET"), strings.Contains(upper, "CHINABROADNET"), strings.Contains(holder, "中国广电"):
		return "中国广电"
	case strings.Contains(upper, "CERNET"):
		return "教育网"
	case strings.Contains(upper, "CSTNET"):
		return "科技网"
	default:
		return holder
	}
}
