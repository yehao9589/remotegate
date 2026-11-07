package hub

import "github.com/local/remotegate/internal/protocol"

type HTTPStreamLimits struct {
	WebSockets, HTTPReserve int
}

type HTTPStreamUsage struct {
	Capacity             int  `json:"capacity"`
	EffectiveWebSockets  int  `json:"effectiveWebSockets"`
	EffectiveHTTPReserve int  `json:"effectiveHTTPReserve"`
	ActiveHTTP           int  `json:"activeHTTP"`
	ActiveWebSockets     int  `json:"activeWebSockets"`
	Streaming            bool `json:"streaming"`
}

// Changes affect admission of new streams without interrupting existing ones.
func (h *Hub) SetHTTPStreamLimitsLookup(lookup func(string) HTTPStreamLimits) {
	h.mu.Lock()
	h.httpLimits = lookup
	h.mu.Unlock()
}

func effectiveHTTPStreamLimits(limits HTTPStreamLimits, capacity int) HTTPStreamLimits {
	capacity = normalizedHTTPStreamCapacity(capacity)
	if limits.WebSockets < 1 || limits.WebSockets > protocol.MaxHTTPStreamCapacity-4 || limits.HTTPReserve < 4 || limits.HTTPReserve > 64 || limits.WebSockets+limits.HTTPReserve > protocol.MaxHTTPStreamCapacity {
		limits = HTTPStreamLimits{protocol.DefaultWebSocketStreams, protocol.MaxHTTPStreams - protocol.DefaultWebSocketStreams}
	}
	// Legacy agents only have 16 slots. Preserve their ordinary page reserve.
	limits.HTTPReserve = min(limits.HTTPReserve, capacity-1)
	limits.WebSockets = min(limits.WebSockets, capacity-limits.HTTPReserve)
	return limits
}

func streamLimits(c *Client, lookup func(string) HTTPStreamLimits) HTTPStreamLimits {
	limits := HTTPStreamLimits{}
	if lookup != nil {
		limits = lookup(c.DeviceID)
	}
	return effectiveHTTPStreamLimits(limits, c.httpStreamCapacity)
}

func normalizedHTTPStreamCapacity(capacity int) int {
	if capacity < protocol.MaxHTTPStreams || capacity > protocol.MaxHTTPStreamCapacity {
		return protocol.MaxHTTPStreams
	}
	return capacity
}

func streamUsage(c *Client, lookup func(string) HTTPStreamLimits) HTTPStreamUsage {
	if !c.CanStreamHTTP {
		return HTTPStreamUsage{}
	}
	limits := streamLimits(c, lookup)
	webSockets := int(c.webSocketCount.Load())
	return HTTPStreamUsage{Capacity: normalizedHTTPStreamCapacity(c.httpStreamCapacity), EffectiveWebSockets: limits.WebSockets, EffectiveHTTPReserve: limits.HTTPReserve, ActiveHTTP: max(0, int(c.webCount.Load())-webSockets), ActiveWebSockets: max(0, webSockets), Streaming: true}
}
