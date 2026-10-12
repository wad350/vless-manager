package encoding

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/stats"
	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/transport/internet/stat"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type ownershipEchoServer struct{ UnimplementedGRPCServiceServer }

func (*ownershipEchoServer) Tun(stream GRPCService_TunServer) error {
	for {
		h, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if err := stream.Send(h); err != nil {
			return err
		}
	}
}

func TestGRPCConcurrentStatsAndMixedReadOwnership(t *testing.T) {
	for _, pooled := range []bool{false, true} {
		t.Run(fmt.Sprintf("pooled=%t", pooled), func(t *testing.T) { testGRPCMixedOwnership(t, pooled) })
	}
}

func TestGRPCCloseReleasesStreamQuota(t *testing.T) {
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.MaxConcurrentStreams(1))
	RegisterGRPCServiceServer(server, &ownershipEchoServer{})
	t.Cleanup(server.Stop)
	go server.Serve(l)
	client, err := grpc.NewClient(l.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	for range 100 {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		stream, err := NewGRPCServiceClient(client).Tun(ctx)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		conn := NewHunkConn(stream, cancel, nil)
		if _, err := conn.Write([]byte("quota")); err != nil {
			conn.Close()
			t.Fatal(err)
		}
		var response [5]byte
		if _, err := io.ReadFull(conn, response[:]); err != nil || string(response[:]) != "quota" {
			conn.Close()
			t.Fatalf("quota stream = %q, %v", response, err)
		}
		conn.Close()
		if ctx.Err() != context.Canceled {
			t.Fatal("closed stream retained its RPC context")
		}
	}
}

func testGRPCMixedOwnership(t *testing.T, pooled bool) {
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	// The peer remains on the ordinary protobuf codec to check wire compatibility.
	server := grpc.NewServer()
	RegisterGRPCServiceServer(server, &ownershipEchoServer{})
	t.Cleanup(server.Stop)
	go server.Serve(l)
	options := []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}
	if pooled {
		options = append(options, grpc.WithDefaultCallOptions(grpc.ForceCodecV2(TunnelCodec())))
	}
	client, err := grpc.NewClient(l.Addr().String(), options...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	var wg sync.WaitGroup
	failures := make(chan error, 4)
	for worker := 0; worker < 4; worker++ {
		wg.Go(func() {
			failures <- checkOwnershipStream(client, worker)
		})
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Error(err)
		}
	}
}

func checkOwnershipStream(client *grpc.ClientConn, worker int) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	stream, err := NewGRPCServiceClient(client).Tun(ctx)
	if err != nil {
		return err
	}
	var count stats.Counter
	conn := &stat.CounterConnection{Connection: NewHunkConn(stream, cancel, nil), ReadCounter: &count}
	defer conn.Close()
	reader := buf.NewReader(conn)
	var total int64
	for message := 0; message < 100; message++ {
		size := []int{64, 1400, buf.Size, 2 * buf.Size}[message%4]
		data := make([]byte, size)
		for i := range data {
			data[i] = byte(i + worker + message)
		}
		if err := stream.Send(&Hunk{Data: data}); err != nil {
			return err
		}
		var prefix [3]byte
		if _, err := io.ReadFull(conn, prefix[:]); err != nil || !bytes.Equal(prefix[:], data[:3]) {
			return fmt.Errorf("worker %d message %d: prefix changed: %v", worker, message, err)
		}
		mb, err := reader.ReadMultiBuffer()
		if err != nil {
			buf.ReleaseMulti(mb)
			return err
		}
		at, valid := 3, true
		for _, b := range mb {
			end := at + int(b.Len())
			if end > len(data) || !bytes.Equal(b.Bytes(), data[at:end]) {
				valid = false
				break
			}
			at = end
		}
		buf.ReleaseMulti(mb)
		if !valid || at != len(data) {
			return fmt.Errorf("worker %d message %d: body changed or truncated", worker, message)
		}
		total += int64(size)
		if count.Value() != total {
			return fmt.Errorf("worker %d message %d: count = %d, want %d", worker, message, count.Value(), total)
		}
	}
	if err := stream.CloseSend(); err != nil {
		return err
	}
	mb, err := reader.ReadMultiBuffer()
	buf.ReleaseMulti(mb)
	if err != io.EOF || count.Value() != total {
		return fmt.Errorf("worker %d: terminal read = %v, count = %d, want %d", worker, err, count.Value(), total)
	}
	return nil
}
