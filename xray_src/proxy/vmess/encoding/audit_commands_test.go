package encoding

import (
	"encoding/binary"
	"testing"
)

type auditCommandWriter struct{}

func (auditCommandWriter) Write([]byte) (int, error) {
	panic("unknown command must not write payload")
}

func TestUnsupportedCommandRejection(t *testing.T) {
	for _, command := range []any{nil, struct{}{}, (*int)(nil), "unknown"} {
		if err := MarshalCommand(command, auditCommandWriter{}); err != ErrUnknownCommand {
			t.Fatalf("command %T: error = %v", command, err)
		}
	}
	for _, id := range []byte{0, 1, 255} {
		for _, data := range [][]byte{nil, {0}, {0, 0, 0, 0}} {
			if command, err := UnmarshalCommand(id, data); command != nil || err != ErrInsufficientLength {
				t.Fatalf("short command %d: value = %v, error = %v", id, command, err)
			}
		}
		data := []byte{0, 0, 0, 0, 42}
		if command, err := UnmarshalCommand(id, data); command != nil || err != ErrInvalidAuth {
			t.Fatalf("unauthenticated command %d: value = %v, error = %v", id, command, err)
		}
		binary.BigEndian.PutUint32(data, Authenticate(data[4:]))
		if command, err := UnmarshalCommand(id, data); command != nil || err != ErrUnknownCommand {
			t.Fatalf("authenticated unknown command %d: value = %v, error = %v", id, command, err)
		}
	}
}
