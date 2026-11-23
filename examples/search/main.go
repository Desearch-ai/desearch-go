// Search prints the successful API JSON after checking it for credentials.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	desearch "github.com/Desearch-ai/desearch-go"
)

func run() error {
	key := os.Getenv("DESEARCH_API_KEY")
	client, err := desearch.NewClient(key, desearch.Config{BaseURL: os.Getenv("DESEARCH_BASE_URL")})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	response, err := client.Call(ctx, desearch.OpSearchAPISearchPost, desearch.Request{Body: desearch.SearchRequest{
		Query: "Desearch search API", Mode: desearch.Ptr("fast"), Count: desearch.Ptr(int64(10)), Highlights: desearch.Ptr(false),
	}})
	if err != nil {
		return err
	}
	var data desearch.SearchResponse
	if err := response.Decode(&data); err != nil {
		return err
	}
	if len(data.Results) == 0 {
		return fmt.Errorf("search returned no results")
	}
	if strings.Contains(string(response.Body), key) {
		return fmt.Errorf("credential detected in response; output suppressed")
	}
	// Preserve the raw successful body for reproducible live-call receipts.
	if _, err := os.Stdout.Write(append(response.Body, '\n')); err != nil {
		return fmt.Errorf("cannot write response")
	}
	receipt := struct {
		Status   int               `json:"status"`
		Results  int               `json:"results"`
		Metadata desearch.Metadata `json:"metadata"`
	}{response.StatusCode, len(data.Results), response.Metadata}
	if err := json.NewEncoder(os.Stderr).Encode(receipt); err != nil {
		return fmt.Errorf("cannot write receipt")
	}
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
