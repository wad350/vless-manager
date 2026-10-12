//go:build !linux

package udp

func (*Hub) startBatch() bool { return false }

func udpPacketTruncated(int) bool { return false }
