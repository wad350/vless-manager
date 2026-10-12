package buf

import (
	"io"
	"sync/atomic"
	"testing"
	"time"
)

type blockingTestReader struct {
	ready chan struct{}
	reads atomic.Int32
}

func (r *blockingTestReader) ReadMultiBuffer() (MultiBuffer, error) {
	r.reads.Add(1)
	<-r.ready
	b := New()
	b.WriteString("payload")
	return MultiBuffer{b}, io.EOF
}

func TestTimeoutWrapperRetainsPendingReadAndCountsOnce(t *testing.T) {
	source := &blockingTestReader{ready: make(chan struct{})}
	counter := new(testCounter)
	r := &TimeoutWrapperReader{Reader: source, Counter: counter}
	for range 3 {
		mb, err := r.ReadMultiBufferTimeout(time.Millisecond)
		if mb != nil || err != nil {
			t.Fatalf("timeout = %v, %v", mb, err)
		}
	}
	close(source.ready)
	mb, err := r.ReadMultiBuffer()
	defer ReleaseMulti(mb)
	if err != io.EOF || mb.Len() != 7 || counter.Value() != 7 || source.reads.Load() != 1 {
		t.Fatalf("pending read: err=%v bytes=%d count=%d reads=%d", err, mb.Len(), counter.Value(), source.reads.Load())
	}
}

type nativeTimedTestReader struct {
	*blockingTestReader
}

func (*nativeTimedTestReader) ReadMultiBufferTimeout(time.Duration) (MultiBuffer, error) {
	panic("native deadline delegation changes pending-read semantics")
}

func TestTimeoutWrapperRetainsReadEvenWithNativeDeadline(t *testing.T) {
	source := &nativeTimedTestReader{&blockingTestReader{ready: make(chan struct{})}}
	counter := new(testCounter)
	r := &TimeoutWrapperReader{Reader: source, Counter: counter}
	mb, err := r.ReadMultiBufferTimeout(time.Millisecond)
	if mb != nil || err != nil || r.done == nil || counter.Value() != 0 {
		t.Fatalf("timeout = %v, %v, pending=%v", mb, err, r.done)
	}
	close(source.ready)
	mb, err = r.ReadMultiBufferTimeout(time.Second)
	defer ReleaseMulti(mb)
	if err != io.EOF || mb.Len() != 7 || counter.Value() != 7 || source.reads.Load() != 1 || r.done != nil {
		t.Fatalf("pending read: err=%v bytes=%d count=%d reads=%d", err, mb.Len(), counter.Value(), source.reads.Load())
	}
}
