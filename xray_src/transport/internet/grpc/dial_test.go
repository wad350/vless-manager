package grpc

import (
	"context"
	"testing"
	"time"

	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/transport/internet"
	"google.golang.org/grpc/connectivity"
)

func TestScopedGRPCClientClosesWithInstance(t *testing.T) {
	base, cancel := context.WithCancel(context.Background())
	ctx, _ := internet.NewSystemDialerContext(base)
	settings := &internet.MemoryStreamConfig{ProtocolName: "grpc", ProtocolSettings: &Config{ServiceName: "test"}}
	dest := net.TCPDestination(net.ParseAddress("127.0.0.1"), 1)
	client, err := getGrpcClient(ctx, dest, settings)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	key := dialerConf{dest, settings}
	cancel()
	deadline := time.After(2 * time.Second)
	for {
		globalDialerAccess.Lock()
		_, cached := globalDialerMap[key]
		globalDialerAccess.Unlock()
		if !cached && client.GetState() == connectivity.Shutdown {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("closed instance retained gRPC client: cached=%v state=%v", cached, client.GetState())
		case <-time.After(10 * time.Millisecond):
		}
	}
}
