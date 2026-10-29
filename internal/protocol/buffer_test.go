package protocol

import (
	"bytes"
	"testing"
)

func TestHTTPBufferUsesByteWindowInsteadOfFrameCount(t *testing.T) {
	b := NewByteBuffer()
	for i := 0; i < 1000; i++ {
		if !b.Push([]byte("x")) {
			t.Fatal("small valid fragments exhausted a frame-count queue")
		}
	}
	p := make([]byte, 1000)
	if n := b.Pop(p); n != 1000 || !bytes.Equal(p, bytes.Repeat([]byte("x"), 1000)) {
		t.Fatal(n)
	}
	if !b.Push(make([]byte, StreamWindowBytes)) || b.Push([]byte("overflow")) {
		t.Fatal("byte window not enforced")
	}
}
