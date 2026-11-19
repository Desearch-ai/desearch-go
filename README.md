# Desearch Go SDK

A thin, standard-library-only HTTP client for the Desearch API. The operation
catalog is generated from the pinned OpenAPI snapshot: 92 operations, with typed
web search models and generic JSON, text and event-stream access.

This is unreleased source. No module version has been published. The commands
below run from a local checkout of the SDK source with **Go 1.27.4** and Python 3
(for regeneration). The module path is `github.com/Desearch-ai/desearch-go` and
the package name is `desearch`. There are no external Go dependencies;
`go.sum` is empty.

```go
import (
    "context"
    "os"
    desearch "github.com/Desearch-ai/desearch-go"
)

client, err := desearch.NewClient(os.Getenv("DESEARCH_API_KEY"), desearch.Config{})
if err != nil { /* handle err */ }
result, err := client.Search(context.Background(), desearch.SearchRequest{
    Query: "Desearch search API",
    Count: desearch.Ptr(int64(10)),
    Highlights: desearch.Ptr(false),
})
if err != nil { /* handle err */ }
// result.Data.Results contains typed results; result.Metadata contains headers.
```

Optional pointers distinguish omitted values from explicit zero/false values.
Omitted fields preserve the API's defaults. Query constraints and billing rules
remain on the server. The schema currently accepts search counts of 10, 15 or 20.

For other operations, use the constants documented in `operations_gen.go`:

```go
response, err := client.Call(ctx, desearch.OpWebSearchWebGet, desearch.Request{
    Query: url.Values{"query": {"Desearch"}},
})
// Handle err before reading response.Body or using response.Decode(&yourModel).
```

Path parameters go in `Request.Path` and are escaped as individual segments.
`Request.Query` supports repeated values; `Request.Body` is JSON-encoded.
`Call` returns successful raw bytes, validates JSON endpoints and supports text
exports. For an event-stream operation, use `Stream` with the same arguments;
close its response body, bound reads and cancel its context. The client does not
parse events or reconnect. Only search has generated typed models; other
operations use caller-defined models or `json.RawMessage`.

The default endpoint is `https://api.desearch.ai`. Set `Config.BaseURL` for a
different HTTPS endpoint; HTTP is allowed only for local tests. The default HTTP
timeout is 60 seconds and buffered responses are limited to 8 MiB. Supply
`Config.HTTPClient` or `MaxResponseBytes` to change these. A custom transport must
be trusted with the Authorization header. Redirects are disabled even with a
custom client, and requests are attempted once. No automatic retry can repeat a
billable request. `APIError` exposes status and parsed `RetryAfter`, while error
messages exclude request URLs, response bodies and underlying transport errors.
Context cancellation and deadlines remain detectable with `errors.Is`.

Supply API keys through your process environment or secret manager. The raw key
is sent in the Authorization header, without a Bearer prefix. Do not put keys in
source, URLs, command-line arguments or logs. Successful response bodies and
streams are API data: callers must decide what is appropriate to log.

Run the live examples with `DESEARCH_API_KEY` already injected in the process
environment. `DESEARCH_BASE_URL` optionally selects a development endpoint.
Search prints the raw successful JSON and a status/count receipt. Failure sends
one random invalid key (expecting 401 or 403), then an empty query using the real
key (expecting 422); it checks diagnostic formatting without logging either key.
These commands make real API calls and search may consume credits.

```sh
go run ./examples/search
go run ./examples/failure
```

Verify a clean source checkout with the pinned toolchain:

```sh
go version
go mod tidy && git diff --exit-code go.mod go.sum
test -z "$(gofmt -l .)"
python3 tools/generate.py && git diff --exit-code -- operations_gen.go models_gen.go
go vet ./...
go test -race ./...
go build ./...
GOFLAGS=-mod=mod GOPROXY=off go list -m
```

Regeneration runs offline and checks `tools/openapi.json` against the SHA-256 in
`tools/source.json`; it makes no API, model or registry requests. To update the
catalog, deliberately replace the snapshot, record its source URL, fetch time
and SHA-256 in that metadata file, regenerate and review the diff. These checks
run locally and need no credentials; only the live examples require an API key.

MIT licensed. The SDK follows the authorization, endpoint and usage-metadata
conventions of [desearch.py](https://github.com/Desearch-ai/desearch.py/tree/f03402dbf56cdca1cc2aa7f9939478d107cfe884)
and [desearch.js](https://github.com/Desearch-ai/desearch.js/tree/43b4b745fbb7eae8144135d9d718f15336321bf7).
The MIT notice is preserved in `LICENSE`. Snapshot provenance and source commits
are recorded in `tools/source.json`.
