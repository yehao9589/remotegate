package hub

import (
	"testing"

	"github.com/local/remotegate/internal/protocol"
)

func TestHTTPStreamLimitsRespectConfiguredReserveAndAgentCapacity(t *testing.T) {
	for _, tc := range []struct {
		capacity         int
		configured, want HTTPStreamLimits
	}{
		{16, HTTPStreamLimits{}, HTTPStreamLimits{8, 8}},
		{128, HTTPStreamLimits{24, 8}, HTTPStreamLimits{24, 8}},
		{16, HTTPStreamLimits{24, 8}, HTTPStreamLimits{8, 8}},
		{16, HTTPStreamLimits{12, 4}, HTTPStreamLimits{12, 4}},
		{16, HTTPStreamLimits{1, 64}, HTTPStreamLimits{1, 15}},
		{128, HTTPStreamLimits{124, 4}, HTTPStreamLimits{124, 4}},
		{0, HTTPStreamLimits{24, 8}, HTTPStreamLimits{8, 8}},
	} {
		if got := effectiveHTTPStreamLimits(tc.configured, tc.capacity); got != tc.want {
			t.Errorf("capacity %d, configured %v: got %v, want %v", tc.capacity, tc.configured, got, tc.want)
		}
	}
	for _, advertised := range []int{-1, 0, 8, 129, 1 << 30} {
		h := New()
		c := h.AttachWithHTTPCapacity("router", "test", "", nil, advertised, false, false, false, false, true)
		if c.httpStreamCapacity != protocol.MaxHTTPStreams || h.Statuses()["router"].Connections.Capacity != protocol.MaxHTTPStreams {
			t.Fatal("invalid capacity accepted", advertised)
		}
	}
}
