package session

import (
	"errors"
	"sync"
)

const MaxCounter uint64 = 9007199254740991

var ErrGap = errors.New("OUTPUT_GAP")
var ErrCounter = errors.New("counter_exhausted")

// Buffer 的通知只代表有变化，数据始终从原始字节环读取。
type Buffer struct {
	mu                    sync.Mutex
	data                  []byte
	head, size, lf, lines int
	start, end            uint64
	changed               chan struct{}
}
type Replay struct {
	Requested, Start, End uint64
	Data                  []byte
}

func NewBuffer(capacity, lines int) *Buffer {
	return &Buffer{data: make([]byte, capacity), lines: lines, changed: make(chan struct{}, 1)}
}
func (b *Buffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if uint64(len(p)) > MaxCounter-b.end {
		return 0, ErrCounter
	}
	for _, v := range p {
		if b.size == len(b.data) {
			b.remove()
		}
		b.data[(b.head+b.size)%len(b.data)] = v
		b.size++
		b.end++
		if v == '\n' {
			b.lf++
		}
		for b.lineCount() > b.lines {
			for b.size > 0 {
				v := b.data[b.head]
				b.remove()
				if v == '\n' {
					break
				}
			}
		}
	}
	select {
	case b.changed <- struct{}{}:
	default:
	}
	return len(p), nil
}
func (b *Buffer) remove() {
	if b.data[b.head] == '\n' {
		b.lf--
	}
	b.head = (b.head + 1) % len(b.data)
	b.size--
	b.start++
}
func (b *Buffer) lineCount() int {
	n := b.lf
	if b.size > 0 && b.data[(b.head+b.size-1)%len(b.data)] != '\n' {
		n++
	}
	return n
}
func (b *Buffer) copy(start, end uint64) []byte {
	p := make([]byte, int(end-start))
	for i := range p {
		p[i] = b.data[(b.head+int(start-b.start)+i)%len(b.data)]
	}
	return p
}
func (b *Buffer) Snapshot(p uint64) (Replay, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.snapshot(p)
}
func (b *Buffer) snapshot(p uint64) (Replay, error) {
	if p > b.end {
		return Replay{}, errors.New("INVALID_OFFSET")
	}
	s := max(p, b.start)
	return Replay{p, s, b.end, b.copy(s, b.end)}, nil
}
func (b *Buffer) Read(cursor uint64, limit int) ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if cursor < b.start {
		return nil, ErrGap
	}
	if cursor > b.end {
		return nil, errors.New("INTERNAL_ERROR")
	}
	return b.copy(cursor, min(b.end, cursor+uint64(limit))), nil
}
func (b *Buffer) Changed() <-chan struct{} { return b.changed }
func (b *Buffer) clear() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = nil
	b.size = 0
	b.head = 0
	b.lf = 0
	b.start = b.end
}
