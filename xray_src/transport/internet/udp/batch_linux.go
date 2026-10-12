//go:build linux

package udp

import (
	"context"
	stderrors "errors"
	"os"

	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/common/errors"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol/udp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
	"golang.org/x/sys/unix"
)

const udpReceiveBatchSize = 8

func udpPacketTruncated(flags int) bool {
	return flags&(unix.MSG_TRUNC|unix.MSG_CTRUNC) != 0
}

type batchPacketReader interface {
	ReadBatch([]ipv4.Message, int) (int, error)
}

func (h *Hub) startBatch() bool {
	if h.udpConn == nil || !h.recvOrigDest || os.Getenv("XRAY_TPROXY_UDP_BATCH") == "off" {
		return false
	}
	address, ok := h.udpConn.LocalAddr().(*net.UDPAddr)
	if !ok {
		return false
	}
	// Go's wildcard "udp" listener may use an IPv6 dual-stack socket even
	// when configured with 0.0.0.0. Select the reader by actual socket family.
	var conn batchPacketReader
	if address.IP.To4() != nil {
		conn = ipv4.NewPacketConn(h.udpConn)
	} else {
		conn = ipv6.NewPacketConn(h.udpConn)
	}
	messages := make([]ipv4.Message, udpReceiveBatchSize)
	var buffers [udpReceiveBatchSize]*buf.Buffer
	for i := range messages {
		messages[i].Buffers = make([][]byte, 1)
		messages[i].OOB = make([]byte, 256)
	}
	defer func() {
		for _, b := range buffers {
			if b != nil {
				b.Release()
			}
		}
	}()

	errors.LogInfo(context.Background(), "native UDP receive batching enabled: ", udpReceiveBatchSize)
	for {
		for i := range messages {
			if buffers[i] == nil {
				buffers[i] = buf.New()
			}
			buffers[i].Clear()
			messages[i].Buffers[0] = buffers[i].ExtendForRead(buf.Size)
		}
		// Go's nonblocking socket waits for available data, not a full batch.
		n, err := conn.ReadBatch(messages, 0)
		for i := 0; i < n; i++ {
			message, buffer := &messages[i], buffers[i]
			buffers[i] = nil
			source, valid := message.Addr.(*net.UDPAddr)
			if !valid || message.N == 0 || udpPacketTruncated(message.Flags) {
				if udpPacketTruncated(message.Flags) {
					udp.RecordTruncated()
				}
				buffer.Release()
				continue
			}
			buffer.Resize(0, int32(message.N))
			h.enqueue(buffer, source, message.OOB[:message.NN])
		}
		if err != nil {
			if n == 0 && (stderrors.Is(err, unix.ENOSYS) || stderrors.Is(err, unix.EOPNOTSUPP)) {
				errors.LogInfo(context.Background(), "UDP batch read unsupported; using single-packet reads")
				return false
			}
			errors.LogInfoInner(context.Background(), err, "failed to read UDP batch")
			return true
		}
	}
}
