package protocol

import "sync"

// ByteBuffer is bounded by bytes, not by the number of small TCP reads.
type ByteBuffer struct {
	mu    sync.Mutex
	data  []byte
	ready chan struct{}
}

func NewByteBuffer() *ByteBuffer { return &ByteBuffer{ready: make(chan struct{}, 1)} }
func (b *ByteBuffer) Push(p []byte) bool {
	b.mu.Lock()
	if len(b.data)+len(p) > StreamWindowBytes {
		b.mu.Unlock()
		return false
	}
	b.data = append(b.data, p...)
	b.mu.Unlock()
	select {
	case b.ready <- struct{}{}:
	default:
	}
	return true
}
func (b *ByteBuffer) Pop(p []byte) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := copy(p, b.data)
	b.data = b.data[n:]
	if len(b.data) == 0 {
		b.data = nil
	}
	return n
}
func (b *ByteBuffer) Ready() <-chan struct{} { return b.ready }
