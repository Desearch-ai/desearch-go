package desearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const Version = "0.1.0"
const DefaultBaseURL = "https://api.desearch.ai"
const defaultMaxResponseBytes int64 = 8 << 20

// Config sets the endpoint, HTTP transport and response size limit.
// HTTPClient is copied; redirects are always disabled to protect credentials.
// A custom transport must be trusted with the Authorization header.
type Config struct {
	BaseURL          string
	HTTPClient       *http.Client
	MaxResponseBytes int64
}

// Client is safe for concurrent requests if its supplied transport is safe.
type Client struct {
	key   string
	base  string
	http  *http.Client
	limit int64
}

func (c Client) String() string   { return "desearch.Client{credentials:redacted}" }
func (c Client) GoString() string { return c.String() }

// NewClient creates a client without making a network request. HTTP is permitted
// only for localhost/loopback tests; use HTTPS for the Desearch API.
func NewClient(apiKey string, config Config) (*Client, error) {
	if strings.TrimSpace(apiKey) == "" || strings.ContainsAny(apiKey, "\r\n") {
		return nil, errors.New("desearch: invalid API key")
	}
	base := config.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return nil, errors.New("desearch: invalid base URL")
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || ip != nil && ip.IsLoopback())) {
		return nil, errors.New("desearch: base URL requires HTTPS")
	}
	client := http.Client{Timeout: 60 * time.Second}
	if config.HTTPClient != nil {
		client = *config.HTTPClient
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	limit := config.MaxResponseBytes
	if limit == 0 {
		limit = defaultMaxResponseBytes
	}
	if limit < 1 || limit == math.MaxInt64 {
		return nil, errors.New("desearch: invalid response size limit")
	}
	return &Client{key: apiKey, base: strings.TrimRight(base, "/"), http: &client, limit: limit}, nil
}

// Ptr distinguishes an omitted optional field from an explicit zero/false.
func Ptr[T any](value T) *T { return &value }

// Request supplies path segments, query values and an optional JSON body.
// Path values are escaped as single segments. Query supports repeated values.
type Request struct {
	Path  map[string]string
	Query url.Values
	Body  any
}

// Metadata contains selected response headers. Invalid numeric values are nil.
type Metadata struct {
	CostUSD    *float64
	UsageCount *int64
	Service    string
	Currency   string
	NextCursor string
}

// Response contains response bytes, including text/CSV export responses.
// Decode parses JSON into a caller-chosen model without exposing body errors.
type Response struct {
	StatusCode int
	Body       []byte
	Metadata   Metadata
}

func (r *Response) Decode(target any) error {
	if err := json.Unmarshal(r.Body, target); err != nil {
		return errors.New("desearch: cannot decode JSON response")
	}
	return nil
}

// APIError exposes only the HTTP status and parsed retry delay. It deliberately
// excludes request URLs, keys, server error bodies and raw transport errors.
type APIError struct {
	StatusCode int
	RetryAfter time.Duration
}

func (e *APIError) Error() string { return fmt.Sprintf("desearch: HTTP %d", e.StatusCode) }

type operationSpec struct {
	method, path                       string
	pathParams, requiredQuery          []string
	bodyRequired, stream, jsonResponse bool
}

func (c *Client) prepare(ctx context.Context, op Operation, input Request) (*http.Request, operationSpec, error) {
	spec, ok := operations[op]
	if !ok {
		return nil, spec, errors.New("desearch: unknown operation")
	}
	path := spec.path
	if len(input.Path) != len(spec.pathParams) {
		return nil, spec, errors.New("desearch: incorrect path parameters")
	}
	for _, name := range spec.pathParams {
		value := input.Path[name]
		if value == "" || value == "." || value == ".." {
			return nil, spec, errors.New("desearch: invalid path parameter")
		}
		path = strings.ReplaceAll(path, "{"+name+"}", url.PathEscape(value))
	}
	for _, name := range spec.requiredQuery {
		if input.Query.Get(name) == "" {
			return nil, spec, errors.New("desearch: missing required query parameter")
		}
	}
	if spec.bodyRequired && input.Body == nil {
		return nil, spec, errors.New("desearch: missing JSON request body")
	}
	var body io.Reader
	if input.Body != nil {
		data, err := json.Marshal(input.Body)
		if err != nil {
			return nil, spec, errors.New("desearch: cannot encode JSON request body")
		}
		if spec.bodyRequired && string(data) == "null" {
			return nil, spec, errors.New("desearch: missing JSON request body")
		}
		body = bytes.NewReader(data)
	}
	endpoint := c.base + path
	if query := input.Query.Encode(); query != "" {
		endpoint += "?" + query
	}
	req, err := http.NewRequestWithContext(ctx, spec.method, endpoint, body)
	if err != nil {
		return nil, spec, errors.New("desearch: cannot construct request")
	}
	req.Header.Set("Authorization", c.key)
	req.Header.Set("User-Agent", "desearch-go/"+Version)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if spec.stream {
		req.Header.Set("Accept", "text/event-stream")
	} else if spec.jsonResponse {
		req.Header.Set("Accept", "application/json")
	}
	return req, spec, nil
}

func (c *Client) send(req *http.Request) (*http.Response, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, context.Canceled
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, context.DeadlineExceeded
		}
		return nil, errors.New("desearch: HTTP transport failed")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		delay := time.Duration(0)
		if seconds, err := strconv.ParseInt(resp.Header.Get("Retry-After"), 10, 32); err == nil && seconds > 0 {
			delay = time.Duration(seconds) * time.Second
		} else if date, err := http.ParseTime(resp.Header.Get("Retry-After")); err == nil {
			delay = time.Until(date)
			if delay < 0 {
				delay = 0
			}
		}
		return nil, &APIError{StatusCode: resp.StatusCode, RetryAfter: delay}
	}
	return resp, nil
}

func (c *Client) metadata(header http.Header) Metadata {
	clean := func(s string) string { return strings.ReplaceAll(s, c.key, "[REDACTED]") }
	m := Metadata{Service: clean(header.Get("X-Desearch-Service")), Currency: clean(header.Get("X-Desearch-Currency")), NextCursor: clean(header.Get("X-Desearch-Next-Cursor"))}
	if v, err := strconv.ParseFloat(header.Get("X-Desearch-Cost-Usd"), 64); err == nil && !math.IsNaN(v) && !math.IsInf(v, 0) {
		m.CostUSD = &v
	}
	if v, err := strconv.ParseInt(header.Get("X-Desearch-Usage-Count"), 10, 64); err == nil {
		m.UsageCount = &v
	}
	return m
}

// Call invokes one generated operation. JSON endpoints reject malformed JSON;
// text endpoints preserve raw bytes. Server errors never return body bytes.
// The client makes exactly one HTTP attempt and never follows redirects.
func (c *Client) Call(ctx context.Context, op Operation, input Request) (*Response, error) {
	req, spec, err := c.prepare(ctx, op, input)
	if err != nil {
		return nil, err
	}
	if spec.stream {
		return nil, errors.New("desearch: use Stream for event streams")
	}
	resp, err := c.send(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, c.limit+1))
	if err != nil {
		return nil, errors.New("desearch: cannot read response")
	}
	if int64(len(data)) > c.limit {
		return nil, errors.New("desearch: response size limit exceeded")
	}
	if spec.jsonResponse && resp.StatusCode != http.StatusNoContent && !json.Valid(data) {
		return nil, errors.New("desearch: malformed JSON response")
	}
	return &Response{StatusCode: resp.StatusCode, Body: data, Metadata: c.metadata(resp.Header)}, nil
}

// Stream opens an event stream. The caller must close Body, bound reads and
// cancel the request context. The HTTP client's timeout still applies.
func (c *Client) Stream(ctx context.Context, op Operation, input Request) (*http.Response, error) {
	req, spec, err := c.prepare(ctx, op, input)
	if err != nil {
		return nil, err
	}
	if !spec.stream {
		return nil, errors.New("desearch: operation is not an event stream")
	}
	return c.send(req)
}

// SearchResultResponse combines typed web search data and response headers.
type SearchResultResponse struct {
	Data     SearchResponse
	Metadata Metadata
}

// Search calls POST /search. Optional request pointers preserve API defaults.
// Field constraints are validated by the API, rather than duplicated here.
func (c *Client) Search(ctx context.Context, input SearchRequest) (*SearchResultResponse, error) {
	resp, err := c.Call(ctx, OpSearchAPISearchPost, Request{Body: input})
	if err != nil {
		return nil, err
	}
	var data SearchResponse
	if err := resp.Decode(&data); err != nil {
		return nil, err
	}
	return &SearchResultResponse{Data: data, Metadata: resp.Metadata}, nil
}
