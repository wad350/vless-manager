package buf

import (
	"time"

	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/features/stats"
)

// Preserve packet ownership and UDP destinations hidden by CounterConnection.
type countedReader struct {
	Reader
	counter stats.Counter
}

func newCountedReader(reader Reader, counter stats.Counter) Reader {
	if counter == nil {
		return reader
	}
	r := &countedReader{Reader: reader, counter: counter}
	if timed, ok := reader.(TimeoutReader); ok {
		return &countedTimeoutReader{countedReader: r, timed: timed}
	}
	return r
}

func (r *countedReader) ReadMultiBuffer() (MultiBuffer, error) {
	mb, err := r.Reader.ReadMultiBuffer()
	r.counter.Add(int64(mb.Len()))
	return mb, err
}

func (r *countedReader) Close() error { return common.Close(r.Reader) }

func (r *countedReader) Interrupt() { common.Interrupt(r.Reader) }

type countedTimeoutReader struct {
	*countedReader
	timed TimeoutReader
}

func (r *countedTimeoutReader) ReadMultiBufferTimeout(timeout time.Duration) (MultiBuffer, error) {
	mb, err := r.timed.ReadMultiBufferTimeout(timeout)
	r.counter.Add(int64(mb.Len()))
	return mb, err
}
