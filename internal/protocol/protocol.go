package protocol

import "net/http"

const MaxBodyBytes = 16 << 20

// JSON encodes Body as base64; the wire frame needs more space than the body.
const MaxMessageBytes = ((MaxBodyBytes + 2) / 3 * 4) + (1 << 20)
const MaxStreamBytes = 32 << 10
const MaxDeviceStreams = 4
const StreamWindowBytes = 4 * MaxStreamBytes

type Message struct {
	Type            string              `json:"type"`
	ID              string              `json:"id,omitempty"`
	Method          string              `json:"method,omitempty"`
	Target          string              `json:"target,omitempty"`
	Path            string              `json:"path,omitempty"`
	Headers         map[string][]string `json:"headers,omitempty"`
	Body            []byte              `json:"body,omitempty"`
	Status          int                 `json:"status,omitempty"`
	Error           string              `json:"error,omitempty"`
	DeviceName      string              `json:"deviceName,omitempty"`
	Version         string              `json:"version,omitempty"`
	Deadline        int64               `json:"deadline,omitempty"`
	EgressInterface string              `json:"egressInterface,omitempty"`
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
