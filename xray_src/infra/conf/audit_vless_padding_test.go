package conf_test

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/xtls/xray-core/common/net"
	. "github.com/xtls/xray-core/infra/conf"
	"github.com/xtls/xray-core/proxy/vless"
	"github.com/xtls/xray-core/proxy/vless/inbound"
	"github.com/xtls/xray-core/proxy/vless/outbound"
)

func TestVLESSPaddingRequiresKey(t *testing.T) {
	for _, suffix := range []string{"", "abc", "abc.def", strings.Repeat("a", 19)} {
		for _, side := range []string{"inbound", "outbound"} {
			t.Run(side+"/"+suffix, func(t *testing.T) {
				defer func() {
					if recovered := recover(); recovered != nil {
						t.Fatalf("invalid padding-only configuration panicked: %v", recovered)
					}
				}()
				var err error
				if side == "inbound" {
					_, err = (&VLessInboundConfig{Decryption: "mlkem768x25519plus.native.600s." + suffix}).Build()
				} else {
					_, err = (&VLessOutboundConfig{
						Address: &Address{Address: net.DomainAddress("example.test")}, Port: 443,
						Id:         "11111111-1111-4111-8111-111111111111",
						Encryption: "mlkem768x25519plus.native.1rtt." + suffix,
					}).Build()
				}
				if err == nil || !strings.Contains(err.Error(), "unsupported") {
					t.Fatalf("padding-only configuration error = %v, want unsupported validation error", err)
				}
			})
		}
	}
}

func TestVLESSPaddingPreservesValidKeyContract(t *testing.T) {
	for _, tc := range []struct {
		name, mode, padding     string
		inKeyBytes, outKeyBytes int
		xorMode                 uint32
	}{
		{"x25519", "native", "", 32, 32, 0},
		{"single_padding", "xorpub", "abc", 64, 1184, 1},
		{"multiple_padding", "random", "abc.def", 32, 32, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inKey := base64.RawURLEncoding.EncodeToString(make([]byte, tc.inKeyBytes))
			outKey := base64.RawURLEncoding.EncodeToString(make([]byte, tc.outKeyBytes))
			prefix := "mlkem768x25519plus." + tc.mode + "."
			padding := tc.padding
			if padding != "" {
				padding += "."
			}
			builtIn, err := (&VLessInboundConfig{Decryption: prefix + "600-900s." + padding + inKey}).Build()
			if err != nil {
				t.Fatal(err)
			}
			in := builtIn.(*inbound.Config)
			if in.Decryption != inKey || in.Padding != tc.padding || in.XorMode != tc.xorMode || in.SecondsFrom != 600 || in.SecondsTo != 900 {
				t.Fatalf("inbound key/padding/mode/time contract changed: %v", in)
			}
			for _, rtt := range []string{"1rtt", "0rtt"} {
				builtOut, err := (&VLessOutboundConfig{
					Address: &Address{Address: net.DomainAddress("example.test")}, Port: 443,
					Id: "11111111-1111-4111-8111-111111111111", Encryption: prefix + rtt + "." + padding + outKey,
				}).Build()
				if err != nil {
					t.Fatal(err)
				}
				message, err := builtOut.(*outbound.Config).Vnext.User.Account.GetInstance()
				if err != nil {
					t.Fatal(err)
				}
				account := message.(*vless.Account)
				wantSeconds := uint32(0)
				if rtt == "0rtt" {
					wantSeconds = 1
				}
				if account.Encryption != outKey || account.Padding != tc.padding || account.XorMode != tc.xorMode || account.Seconds != wantSeconds {
					t.Fatalf("outbound key/padding/mode/rtt contract changed: %v", account)
				}
			}
		})
	}
}
