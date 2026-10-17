package protocol

import (
	"encoding/json"
	"testing"
)

func TestMaximumBodyFitsWithinMessageFrame(t *testing.T) {
	encoded, err := json.Marshal(Message{Type: "response", ID: "fixture", Status: 200, Body: make([]byte, MaxBodyBytes), Headers: map[string][]string{"Content-Type": {"application/octet-stream"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) <= MaxBodyBytes+(1<<20) || len(encoded) > MaxMessageBytes {
		t.Fatalf("body=%d encoded=%d wire limit=%d", MaxBodyBytes, len(encoded), MaxMessageBytes)
	}
}
