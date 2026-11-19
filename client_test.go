package desearch

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func canary(t *testing.T) string {
	t.Helper()
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		t.Fatal("canary generation failed")
	}
	return hex.EncodeToString(b)
}

func noLeak(t *testing.T, key string, values ...any) {
	t.Helper()
	for _, value := range values {
		for _, format := range []string{"%v", "%+v", "%#v"} {
			text := fmt.Sprintf(format, value)
			for _, secret := range []string{key, url.QueryEscape(key), base64.StdEncoding.EncodeToString([]byte(key))} {
				if strings.Contains(text, secret) {
					t.Fatal("credential appeared in diagnostic output")
				}
			}
		}
	}
}

func testClient(t *testing.T, key string, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := NewClient(key, Config{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestSearchDefaultsAndMetadata(t *testing.T) {
	key := canary(t)
	client := testClient(t, key, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/search" || r.Header.Get("Authorization") != key || r.Header.Get("User-Agent") != "desearch-go/"+Version {
			t.Error("request contract mismatch")
		}
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("cannot decode request")
		}
		if _, ok := body["count"]; ok {
			t.Error("omitted count was sent")
		}
		if body["highlights"] != false || body["query"] != "climate" {
			t.Error("request fields mismatch")
		}
		w.Header().Set("X-Desearch-Cost-Usd", "0.001")
		w.Header().Set("X-Desearch-Usage-Count", "2")
		w.Header().Set("X-Desearch-Next-Cursor", "next")
		io.WriteString(w, `{"results":[{"title":"Example","url":"https://example.org","highlights":["passage"]}],"meta":{},"summary":null,"cost_usd":0.001,"usage_count":2,"service":"search","currency":"USD"}`)
	})
	response, err := client.Search(context.Background(), SearchRequest{Query: "climate", Highlights: Ptr(false)})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Data.Results) != 1 || response.Data.Results[0].URL != "https://example.org" || response.Data.Summary != nil || response.Metadata.CostUSD == nil || *response.Metadata.CostUSD != 0.001 || response.Metadata.UsageCount == nil || *response.Metadata.UsageCount != 2 || response.Metadata.NextCursor != "next" {
		t.Fatal("response contract mismatch")
	}
	noLeak(t, key, client, *client)
}

func TestPathQueryAndText(t *testing.T) {
	key := canary(t)
	client := testClient(t, key, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/desearch/tiktok/post/") {
			if r.URL.EscapedPath() != "/desearch/tiktok/post/a%2Fb%3F%23" || len(r.URL.Query()["extra"]) != 2 {
				t.Error("escaping or repeated query failed")
			}
			io.WriteString(w, `{}`)
		} else {
			io.WriteString(w, "plain page text")
		}
	})
	_, err := client.Call(context.Background(), OpTiktokPostByIDDesearchTiktokPostIDGet, Request{Path: map[string]string{"id": "a/b?#"}, Query: url.Values{"extra": {"first", "second"}}})
	if err != nil {
		t.Fatal(err)
	}
	r, err := client.Call(context.Background(), OpDesearchCrawlWebExtractGet, Request{Query: url.Values{"url": {"https://example.org"}}})
	if err != nil || string(r.Body) != "plain page text" {
		t.Fatal("text response failed")
	}
	if err := r.Decode(&map[string]any{}); err == nil {
		t.Fatal("text should fail JSON decoding")
	}
}

func TestHTTPFailuresNoRetryOrLeak(t *testing.T) {
	for _, status := range []int{301, 400, 401, 403, 422, 429, 500, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			key := canary(t)
			var calls atomic.Int32
			client := testClient(t, key, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Retry-After", "3")
				w.Header().Set("Location", "/secret-target")
				w.WriteHeader(status)
				io.WriteString(w, key)
			})
			r, err := client.Search(context.Background(), SearchRequest{Query: "test"})
			var apiErr *APIError
			if r != nil || !errors.As(err, &apiErr) || apiErr.StatusCode != status || apiErr.RetryAfter != 3*time.Second || calls.Load() != 1 {
				t.Fatal("HTTP failure contract mismatch")
			}
			noLeak(t, key, err)
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type badJSON string

func (b badJSON) MarshalJSON() ([]byte, error) { return nil, errors.New(string(b)) }

type brokenReader string

func (b brokenReader) Read([]byte) (int, error) { return 0, errors.New(string(b)) }

func TestLocalFailuresSanitized(t *testing.T) {
	key := canary(t)
	for _, base := range []string{"http://example.org", "https://user:" + key + "@example.org", "https://example.org?key=" + key, "://" + key} {
		_, err := NewClient(key, Config{BaseURL: base})
		if err == nil {
			t.Fatal("invalid base accepted")
		}
		noLeak(t, key, err)
	}
	client, _ := NewClient(key, Config{HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New(key) })}})
	for _, req := range []Request{{Body: badJSON(key)}, {Body: SearchRequest{Query: "test"}}} {
		_, err := client.Call(context.Background(), OpSearchAPISearchPost, req)
		if err == nil {
			t.Fatal("expected failure")
		}
		noLeak(t, key, err)
	}
	var typedNil *SearchRequest
	for _, row := range []struct {
		op  Operation
		req Request
	}{
		{Operation(key), Request{}}, {OpSearchAPISearchPost, Request{}}, {OpSearchAPISearchPost, Request{Body: typedNil}},
		{OpTiktokPostByIDDesearchTiktokPostIDGet, Request{Path: map[string]string{"id": ".."}}},
		{OpTiktokPostByIDDesearchTiktokPostIDGet, Request{Path: map[string]string{"wrong": key}}},
		{OpWebSearchWebGet, Request{}},
	} {
		_, err := client.Call(context.Background(), row.op, row.req)
		if err == nil {
			t.Fatal("invalid request accepted")
		}
		noLeak(t, key, err)
	}
	for _, invalid := range []string{"", "\n"} {
		if _, err := NewClient(invalid, Config{}); err == nil {
			t.Fatal("invalid key accepted")
		}
	}
	if _, err := NewClient(key, Config{MaxResponseBytes: -1}); err == nil {
		t.Fatal("invalid size accepted")
	}
}

func TestMalformedAndOversizeResponses(t *testing.T) {
	key := canary(t)
	for _, body := range []string{key, "", "{"} {
		client := testClient(t, key, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) })
		_, err := client.Search(context.Background(), SearchRequest{Query: "test"})
		if err == nil {
			t.Fatal("malformed JSON accepted")
		}
		noLeak(t, key, err)
	}
	client := testClient(t, key, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"long":"`+key+`"}`) })
	client.limit = 8
	_, err := client.Search(context.Background(), SearchRequest{Query: "test"})
	if err == nil {
		t.Fatal("oversize response accepted")
	}
	noLeak(t, key, err)
	client.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(brokenReader(key)), Header: http.Header{}}, nil
	})
	_, err = client.Search(context.Background(), SearchRequest{Query: "test"})
	if err == nil {
		t.Fatal("broken response accepted")
	}
	noLeak(t, key, err)
	r := &Response{Body: []byte(key)}
	noLeak(t, key, r.Decode(&map[string]any{}))
}

func TestCancellationAndDeadline(t *testing.T) {
	key := canary(t)
	client := testClient(t, key, func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := client.Search(ctx, SearchRequest{Query: "test"})
	if !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation lost")
	}
	ctx, cancel = context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer cancel()
	_, err = client.Search(ctx, SearchRequest{Query: "test"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("deadline lost")
	}
}

func TestStreamAndInvalidMetadata(t *testing.T) {
	key := canary(t)
	client := testClient(t, key, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "text/event-stream" {
			t.Error("missing SSE accept")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {}\n\n")
	})
	op := OpGetNetworkStatsStreamNetworkStatsStreamGet
	r, err := client.Stream(context.Background(), op, Request{})
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(r.Body)
	r.Body.Close()
	if err != nil || string(data) != "data: {}\n\n" {
		t.Fatal("stream read failed")
	}
	if _, err = client.Call(context.Background(), op, Request{}); err == nil {
		t.Fatal("Call accepted stream")
	}
	if _, err = client.Stream(context.Background(), OpSearchAPISearchPost, Request{Body: SearchRequest{Query: "test"}}); err == nil {
		t.Fatal("Stream accepted JSON operation")
	}
	m := client.metadata(http.Header{"X-Desearch-Cost-Usd": {"NaN"}, "X-Desearch-Usage-Count": {"bad"}, "X-Desearch-Service": {key}})
	if m.CostUSD != nil || m.UsageCount != nil {
		t.Fatal("malformed metadata accepted")
	}
	noLeak(t, key, m)
}

func TestConcurrentSearch(t *testing.T) {
	client := testClient(t, canary(t), func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"results":[],"meta":{},"cost_usd":0,"usage_count":0,"service":"search","currency":"USD"}`)
	})
	for range 8 {
		t.Run("request", func(t *testing.T) {
			t.Parallel()
			if _, err := client.Search(context.Background(), SearchRequest{Query: "test"}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
