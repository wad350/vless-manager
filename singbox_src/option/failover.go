package option

import "github.com/sagernet/sing/common/json/badoption"

type FailoverInboundOptions struct {
	Inbounds []Inbound `json:"inbounds"`
}

type FailoverOutboundOptions struct {
	Strategy  string             `json:"strategy,omitempty"`
	Delay     badoption.Duration `json:"delay,omitempty"`
	Outbounds []Outbound         `json:"outbounds"`
}
