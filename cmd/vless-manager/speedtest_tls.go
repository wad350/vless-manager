package main

import (
	"context"
	"crypto/tls"
	"net"
	"time"
)

// A gRPC stream can receive no upstream response while its siblings work.
// Race at most one spare handshake, before sending any HTTP or measured data.
// Both attempts share the original deadline and verify the same TLS identity.
func dialSpeedTestTLS(ctx context.Context, network, address string,
	dial func(context.Context, string, string) (net.Conn, error), config *tls.Config,
	timeout, hedgeDelay time.Duration,
) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	if config == nil {
		config = &tls.Config{}
	}
	config = config.Clone()
	if config.ServerName == "" {
		config.ServerName = host
	}
	conn, err := raceSpeedTestTLS(ctx, network, address, dial, config, timeout, hedgeDelay)
	if err != nil {
		return nil, err
	}
	return conn, nil
}

func raceSpeedTestTLS(ctx context.Context, network, address string,
	dial func(context.Context, string, string) (net.Conn, error), config *tls.Config,
	timeout, hedgeDelay time.Duration,
) (*tls.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	type result struct {
		conn *tls.Conn
		err  error
	}
	results := make(chan result)
	start := func() {
		go func() {
			raw, err := dial(ctx, network, address)
			var conn *tls.Conn
			if err == nil {
				conn = tls.Client(raw, config.Clone())
				err = conn.HandshakeContext(ctx)
				if err != nil {
					raw.Close()
					conn = nil
				}
			}
			select {
			case results <- result{conn, err}:
			case <-ctx.Done():
				if conn != nil {
					conn.NetConn().Close()
				}
			}
		}()
	}
	start()
	timer := time.NewTimer(hedgeDelay)
	defer timer.Stop()
	pending, attempts := 1, 1
	var firstErr error
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
			if attempts == 1 {
				attempts++
				pending++
				start()
			}
		case r := <-results:
			pending--
			if r.err == nil {
				if err := ctx.Err(); err != nil {
					r.conn.NetConn().Close()
					return nil, err
				}
				return r.conn, nil
			}
			if !speedTestRetryableError(r.err) {
				return nil, r.err
			}
			if firstErr == nil {
				firstErr = r.err
			}
			if attempts == 1 {
				attempts++
				pending++
				start()
			}
			if pending == 0 {
				return nil, firstErr
			}
		}
	}
}
