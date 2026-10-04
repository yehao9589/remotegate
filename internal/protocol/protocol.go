package protocol

import "net/http"

const MaxBodyBytes = 16 << 20

type Message struct {
	Type       string              `json:"type"`
	ID         string              `json:"id,omitempty"`
	Method     string              `json:"method,omitempty"`
	Target     string              `json:"target,omitempty"`
	Path       string              `json:"path,omitempty"`
	Headers    map[string][]string `json:"headers,omitempty"`
	Body       []byte              `json:"body,omitempty"`
	Status     int                 `json:"status,omitempty"`
	Error      string              `json:"error,omitempty"`
	DeviceName string              `json:"deviceName,omitempty"`
	Version    string              `json:"version,omitempty"`
}

func FilterHeaders(src http.Header) map[string][]string {
	dst := make(map[string][]string, len(src))
	for key, values := range src {
		switch http.CanonicalHeaderKey(key) {
		case "Connection", "Proxy-Connection", "Keep-Alive", "Transfer-Encoding", "Upgrade":
			continue
		}
		dst[key] = append([]string(nil), values...)
	}
	return dst
}
