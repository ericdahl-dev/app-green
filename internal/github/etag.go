// Copied from git-green internal/github/etag.go (2026-10-06); Pool, fromCache and
// maxCachedResponses dropped, cache header renamed, bodies over
// maxCachedBody not cached.

package github

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"sync"
)

// maxCachedBody is the largest body the cache keeps; a bigger response
// passes through uncached so a few large ones cannot pin megabytes.
const maxCachedBody = 256 << 10

// cacheHeader marks a response replayed from the ETag cache.
const cacheHeader = "X-App-Green-Cache"

// etagTransport makes unchanged GET responses free. It sends the ETag from the
// last response for a URL as If-None-Match; when GitHub answers 304 Not
// Modified, it replays the stored body as a 200 so callers see ordinary data.
//
// One transport serves one token, so cached responses never cross tokens.
type etagTransport struct {
	base http.RoundTripper
	max  int

	mu      sync.Mutex
	entries map[string]*cachedResponse
	clock   uint64 // bumped on every use, for least-recently-used eviction
}

type cachedResponse struct {
	etag   string
	header http.Header
	body   []byte
	used   uint64
}

func newETagTransport(base http.RoundTripper, max int) *etagTransport {
	return &etagTransport{base: base, max: max, entries: make(map[string]*cachedResponse)}
}

func (t *etagTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodGet {
		return t.base.RoundTrip(req)
	}
	key := req.URL.String()

	t.mu.Lock()
	cached := t.entries[key]
	if cached != nil {
		t.clock++
		cached.used = t.clock
	}
	t.mu.Unlock()

	if cached != nil {
		req = req.Clone(req.Context())
		req.Header.Set("If-None-Match", cached.etag)
	}

	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode == http.StatusNotModified && cached != nil {
		_ = resp.Body.Close()
		return replay(req, resp, cached), nil
	}

	etag := resp.Header.Get("ETag")
	if resp.StatusCode != http.StatusOK || etag == "" {
		return resp, nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxCachedBody+1))
	if err != nil {
		_ = resp.Body.Close()
		return nil, err
	}
	if len(body) > maxCachedBody {
		// Too big to cache: hand back what was read followed by the rest.
		resp.Body = struct {
			io.Reader
			io.Closer
		}{io.MultiReader(bytes.NewReader(body), resp.Body), resp.Body}
		return resp, nil
	}
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(body))
	t.store(key, &cachedResponse{etag: etag, header: resp.Header.Clone(), body: body})
	return resp, nil
}

// replay builds a 200 from the cached body, taking the rate-limit headers from
// the fresh 304 so they stay current.
func replay(req *http.Request, notModified *http.Response, cached *cachedResponse) *http.Response {
	header := cached.header.Clone()
	for k, v := range notModified.Header {
		if strings.HasPrefix(strings.ToLower(k), "x-ratelimit-") {
			header[k] = v
		}
	}
	header.Set(cacheHeader, "1")
	return &http.Response{
		Status:        "200 OK",
		StatusCode:    http.StatusOK,
		Proto:         notModified.Proto,
		ProtoMajor:    notModified.ProtoMajor,
		ProtoMinor:    notModified.ProtoMinor,
		Header:        header,
		Body:          io.NopCloser(bytes.NewReader(cached.body)),
		ContentLength: int64(len(cached.body)),
		Request:       req,
	}
}

func (t *etagTransport) store(key string, c *cachedResponse) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.clock++
	c.used = t.clock
	t.entries[key] = c
	if len(t.entries) <= t.max {
		return
	}
	oldest, oldestUse := "", ^uint64(0)
	for k, e := range t.entries {
		if e.used < oldestUse {
			oldest, oldestUse = k, e.used
		}
	}
	delete(t.entries, oldest)
}
