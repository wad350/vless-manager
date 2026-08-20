package bandwidth

import (
	"context"
	"net"

	"github.com/sagernet/sing-box/common/onclose"
)

type connWithDownloadBandwidthLimiter struct {
	net.Conn
	ctx     context.Context
	limiter BandwidthLimiter
}

func NewConnWithDownloadBandwidthLimiter(ctx context.Context, conn net.Conn, limiter BandwidthLimiter) *connWithDownloadBandwidthLimiter {
	return &connWithDownloadBandwidthLimiter{conn, ctx, limiter}
}

func (conn *connWithDownloadBandwidthLimiter) Write(p []byte) (n int, err error) {
	err = conn.limiter.WaitN(conn.ctx, len(p))
	if err != nil {
		return
	}
	return conn.Conn.Write(p)
}

type connWithUploadBandwidthLimiter struct {
	net.Conn
	ctx     context.Context
	limiter BandwidthLimiter
}

func NewConnWithUploadBandwidthLimiter(ctx context.Context, conn net.Conn, limiter BandwidthLimiter) *connWithUploadBandwidthLimiter {
	return &connWithUploadBandwidthLimiter{conn, ctx, limiter}
}

func (conn *connWithUploadBandwidthLimiter) Read(p []byte) (n int, err error) {
	n, err = conn.Conn.Read(p)
	if err != nil {
		return
	}
	err = conn.limiter.WaitN(conn.ctx, n)
	if err != nil {
		return
	}
	return n, err
}

type connWithCloseHandler struct {
	net.Conn
	onClose onclose.CloseHandlerFunc
}

func NewConnWithCloseHandler(conn net.Conn, onClose onclose.CloseHandlerFunc) *connWithCloseHandler {
	return &connWithCloseHandler{conn, onClose}
}

func (conn *connWithCloseHandler) Close() error {
	conn.onClose()
	return conn.Conn.Close()
}

type packetConnWithDownloadBandwidthLimiter struct {
	net.PacketConn
	ctx     context.Context
	limiter BandwidthLimiter
}

func NewPacketConnWithDownloadBandwidthLimiter(ctx context.Context, conn net.PacketConn, limiter BandwidthLimiter) *packetConnWithDownloadBandwidthLimiter {
	return &packetConnWithDownloadBandwidthLimiter{conn, ctx, limiter}
}

func (conn *packetConnWithDownloadBandwidthLimiter) WriteTo(p []byte, addr net.Addr) (n int, err error) {
	err = conn.limiter.WaitN(conn.ctx, len(p))
	if err != nil {
		return
	}
	return conn.PacketConn.WriteTo(p, addr)
}

type packetConnWithUploadBandwidthLimiter struct {
	net.PacketConn
	ctx     context.Context
	limiter BandwidthLimiter
}

func NewPacketConnWithUploadBandwidthLimiter(ctx context.Context, conn net.PacketConn, limiter BandwidthLimiter) *packetConnWithUploadBandwidthLimiter {
	return &packetConnWithUploadBandwidthLimiter{conn, ctx, limiter}
}

func (conn *packetConnWithUploadBandwidthLimiter) ReadFrom(p []byte) (n int, addr net.Addr, err error) {
	n, addr, err = conn.PacketConn.ReadFrom(p)
	if err != nil {
		return
	}
	err = conn.limiter.WaitN(conn.ctx, n)
	if err != nil {
		return
	}
	return
}

type packetConnWithCloseHandler struct {
	net.PacketConn
	onClose onclose.CloseHandlerFunc
}

func NewPacketConnWithCloseHandler(conn net.PacketConn, onClose onclose.CloseHandlerFunc) *packetConnWithCloseHandler {
	return &packetConnWithCloseHandler{conn, onClose}
}

func (conn *packetConnWithCloseHandler) Close() error {
	conn.onClose()
	return conn.PacketConn.Close()
}

func connWithDownloadBandwidthWrapper(ctx context.Context, conn net.Conn, limiter BandwidthLimiter, reverse bool) net.Conn {
	if reverse {
		return NewConnWithUploadBandwidthLimiter(ctx, conn, limiter)
	}
	return NewConnWithDownloadBandwidthLimiter(ctx, conn, limiter)
}

func connWithUploadBandwidthWrapper(ctx context.Context, conn net.Conn, limiter BandwidthLimiter, reverse bool) net.Conn {
	if reverse {
		return NewConnWithDownloadBandwidthLimiter(ctx, conn, limiter)
	}
	return NewConnWithUploadBandwidthLimiter(ctx, conn, limiter)
}

func connWithBidirectionalBandwidthWrapper(ctx context.Context, conn net.Conn, limiter BandwidthLimiter, reverse bool) net.Conn {
	return NewConnWithUploadBandwidthLimiter(ctx, NewConnWithDownloadBandwidthLimiter(ctx, conn, limiter), limiter)
}

func packetConnWithDownloadBandwidthWrapper(ctx context.Context, conn net.PacketConn, limiter BandwidthLimiter, reverse bool) net.PacketConn {
	if reverse {
		return NewPacketConnWithUploadBandwidthLimiter(ctx, conn, limiter)
	}
	return NewPacketConnWithDownloadBandwidthLimiter(ctx, conn, limiter)
}

func packetConnWithUploadBandwidthWrapper(ctx context.Context, conn net.PacketConn, limiter BandwidthLimiter, reverse bool) net.PacketConn {
	if reverse {
		return NewPacketConnWithDownloadBandwidthLimiter(ctx, conn, limiter)
	}
	return NewPacketConnWithUploadBandwidthLimiter(ctx, conn, limiter)
}

func packetConnWithBidirectionalBandwidthWrapper(ctx context.Context, conn net.PacketConn, limiter BandwidthLimiter, reverse bool) net.PacketConn {
	return NewPacketConnWithUploadBandwidthLimiter(ctx, NewPacketConnWithDownloadBandwidthLimiter(ctx, conn, limiter), limiter)
}
