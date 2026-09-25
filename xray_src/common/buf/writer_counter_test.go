package buf

import (
	"bytes"
	"testing"
)

type testCounter int64

func (c *testCounter) Value() int64 { return int64(*c) }
func (c *testCounter) Set(n int64) int64 {
	old := *c
	*c = testCounter(n)
	return int64(old)
}
func (c *testCounter) Add(n int64) int64 {
	old := *c
	*c += testCounter(n)
	return int64(old)
}

func TestBufferToBytesWriterCountsBufferedWrite(t *testing.T) {
	var output bytes.Buffer
	var count testCounter
	writer := NewBufferedWriter(&BufferToBytesWriter{Writer: &output, counter: &count})
	if n, err := writer.Write([]byte("VLESS header")); n != len("VLESS header") || err != nil {
		t.Fatalf("write = %d, %v", n, err)
	}
	if err := writer.Flush(); err != nil {
		t.Fatal(err)
	}
	if count.Value() != int64(output.Len()) || output.String() != "VLESS header" {
		t.Fatalf("count = %d, output = %q", count.Value(), output.String())
	}
}
