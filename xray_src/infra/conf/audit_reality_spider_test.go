package conf

import (
	"encoding/base64"
	"reflect"
	"strings"
	"testing"

	"github.com/xtls/xray-core/transport/internet/reality"
)

func TestAuditRealitySpiderURL(t *testing.T) {
	for _, tc := range []struct {
		name, input, wantPath string
		wantRanges            []int64
		wantError             bool
	}{
		{name: "invalid percent escape", input: "/%zz", wantError: true},
		{name: "incomplete percent escape", input: "/%", wantError: true},
		{name: "default path", wantPath: "/", wantRanges: make([]int64, 10)},
		{name: "escaped path and retained query", input: "/a%20b?keep=yes&p=1-2&c=3&t=4-5&i=6&r=7-8", wantPath: "/a%20b?keep=yes", wantRanges: []int64{1, 2, 3, 3, 4, 5, 6, 6, 7, 8}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := &REALITYConfig{
				Fingerprint: "chrome",
				PublicKey:   base64.RawURLEncoding.EncodeToString(make([]byte, 32)),
				SpiderX:     tc.input,
			}
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("Build panicked: %v", recovered)
				}
			}()
			built, err := config.Build()
			if tc.wantError {
				if err == nil || !strings.Contains(err.Error(), "spiderX") {
					t.Fatalf("Build error = %v, want contextual spiderX error", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got := built.(*reality.Config)
			if got.SpiderX != tc.wantPath || !reflect.DeepEqual(got.SpiderY, tc.wantRanges) {
				t.Fatalf("spider = %q, %v; want %q, %v", got.SpiderX, got.SpiderY, tc.wantPath, tc.wantRanges)
			}
		})
	}
}
