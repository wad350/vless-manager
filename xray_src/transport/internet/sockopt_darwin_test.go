package internet

import (
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func TestDarwinKeepaliveSocketOptions(t *testing.T) {
	for _, direction := range []struct {
		name  string
		apply func(uintptr, *SocketConfig) error
	}{
		{"outbound", func(fd uintptr, config *SocketConfig) error {
			return applyOutboundSocketOptions("tcp4", "127.0.0.1:1", fd, config)
		}},
		{"inbound", func(fd uintptr, config *SocketConfig) error {
			return applyInboundSocketOptions("tcp4", fd, config)
		}},
	} {
		t.Run(direction.name, func(t *testing.T) {
			fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, syscall.IPPROTO_TCP)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { syscall.Close(fd) })
			config := &SocketConfig{TcpKeepAliveIdle: 120, TcpKeepAliveInterval: 30}
			if err := direction.apply(uintptr(fd), config); err != nil {
				t.Fatal(err)
			}
			for _, option := range []struct {
				name string
				opt  int
				want int
			}{
				{"idle", unix.TCP_KEEPALIVE, 120},
				{"interval", sysTCP_KEEPINTVL, 30},
			} {
				got, err := unix.GetsockoptInt(fd, unix.IPPROTO_TCP, option.opt)
				if err != nil {
					t.Fatal(err)
				}
				if got != option.want {
					t.Errorf("%s: got %d seconds, want %d", option.name, got, option.want)
				}
			}
		})
	}
}
