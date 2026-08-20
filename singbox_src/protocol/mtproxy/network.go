package mtproxy

import (
	"context"
	"net"
	"net/http"

	"github.com/dolonet/mtg-multi/essentials"
)

type NetworkAdapter struct {
	ctx    context.Context
	dialer essentials.Dialer
}

func NewNetworkAdapter(ctx context.Context, dialer essentials.Dialer) *NetworkAdapter {
	return &NetworkAdapter{ctx, dialer}
}

func (a *NetworkAdapter) Dial(network, address string) (essentials.Conn, error) {
	return a.DialContext(a.ctx, network, address)
}

func (a *NetworkAdapter) DialContext(ctx context.Context, network, address string) (essentials.Conn, error) {
	conn, err := a.dialer.DialContext(ctx, network, address)
	if err != nil {
		return nil, err
	}
	return essentials.WrapNetConn(conn), nil
}

func (a *NetworkAdapter) MakeHTTPClient(func(ctx context.Context, network, address string) (essentials.Conn, error)) *http.Client {
	return &http.Client{
		Timeout: 10,
		Transport: &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return a.DialContext(ctx, network, addr)
		}},
	}
}

func (a *NetworkAdapter) NativeDialer() essentials.Dialer {
	return a.dialer
}
