//go:build with_openvpn

package include

import (
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/protocol/openvpn"
)

func registerOpenVPNOutbound(registry *outbound.Registry) {
	openvpn.RegisterOutbound(registry)
}
