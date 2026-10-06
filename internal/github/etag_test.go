package github

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestETagTransportSkipsLargeBodies(t *testing.T) {
	cases := []struct {
		name   string
		size   int
		cached bool
	}{
		{"a small body is cached", 1024, true},
		{"a body over 256KB is not", maxCachedBody + 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := strings.Repeat("x", tc.size)
			var inm []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				inm = append(inm, r.Header.Get("If-None-Match"))
				w.Header().Set("ETag", `"v1"`)
				_, _ = io.WriteString(w, body)
			}))
			defer srv.Close()
			hc := &http.Client{Transport: newETagTransport(http.DefaultTransport, 10)}
			for i := range 2 {
				resp, err := hc.Get(srv.URL)
				if err != nil {
					t.Fatal(err)
				}
				got, _ := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				if len(got) != tc.size {
					t.Errorf("call %d: body %d bytes, want %d", i+1, len(got), tc.size)
				}
			}
			if sent := inm[1] != ""; sent != tc.cached {
				t.Errorf("second If-None-Match = %q, want cached %v", inm[1], tc.cached)
			}
		})
	}
}
