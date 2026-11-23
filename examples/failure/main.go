// Failure checks live authorization and request-validation errors without
// printing or persisting the randomly generated canary or the real API key.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	desearch "github.com/Desearch-ai/desearch-go"
)

func check(key string, query string, expected ...int) error {
	client, err := desearch.NewClient(key, desearch.Config{BaseURL: os.Getenv("DESEARCH_BASE_URL")})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err = client.Search(ctx, desearch.SearchRequest{Query: query})
	if err == nil {
		return errors.New("expected request rejection")
	}
	for _, format := range []string{"%v", "%+v", "%#v"} {
		if strings.Contains(fmt.Sprintf(format, err), key) {
			return errors.New("credential detected in error; output suppressed")
		}
	}
	var apiErr *desearch.APIError
	if !errors.As(err, &apiErr) {
		return err
	}
	for _, status := range expected {
		if apiErr.StatusCode == status {
			fmt.Printf("status=%d credential_leak=false\n", status)
			return nil
		}
	}
	return fmt.Errorf("unexpected HTTP status: %d", apiErr.StatusCode)
}

func run() error {
	key := os.Getenv("DESEARCH_API_KEY")
	if key == "" {
		return errors.New("DESEARCH_API_KEY is required for the validation check")
	}
	canary := make([]byte, 24)
	if _, err := rand.Read(canary); err != nil {
		return errors.New("cannot generate canary")
	}
	if err := check(hex.EncodeToString(canary), "Desearch", 401, 403); err != nil {
		return err
	}
	return check(key, "", 422)
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
