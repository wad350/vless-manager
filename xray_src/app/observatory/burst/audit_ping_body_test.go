package burst

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type auditPingBody struct {
	reader     io.Reader
	closeCalls int
}

func (b *auditPingBody) Read(p []byte) (int, error) { return b.reader.Read(p) }
func (b *auditPingBody) Close() error {
	b.closeCalls++
	return nil
}

type auditPingReadError struct{}

func (auditPingReadError) Read([]byte) (int, error) {
	return 0, errors.New("fixture body read error")
}

type auditPingRoundTrip func(*http.Request) (*http.Response, error)

func (f auditPingRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPingClosesResponseBody(t *testing.T) {
	for _, failRead := range []bool{false, true} {
		name := "success"
		if failRead {
			name = "read_error"
		}
		t.Run(name, func(t *testing.T) {
			body := &auditPingBody{reader: strings.NewReader("fixture response")}
			if failRead {
				body.reader = auditPingReadError{}
			}
			client := &pingClient{
				destination: "https://example.test/probe",
				httpClient: &http.Client{Transport: auditPingRoundTrip(func(*http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body}, nil
				})},
			}
			_, err := client.MeasureDelay(http.MethodGet)
			if (err != nil) != failRead {
				t.Fatalf("read failure = %v, got error %v", failRead, err)
			}
			if body.closeCalls != 1 {
				t.Fatalf("response body closed %d times, want once", body.closeCalls)
			}
		})
	}
}
