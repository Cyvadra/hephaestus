package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/Cyvadra/ds4"
)

func TestRawCallRetriesWithProviderMaxTokens(t *testing.T) {
	var received []int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request ds4.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		received = append(received, request.MaxTokens)
		w.Header().Set("Content-Type", "application/json")

		if request.MaxTokens > 100 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"Invalid max_tokens value, the valid range of max_tokens is [1, 100]","param":"max_tokens"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"compressed"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	client := &Client{ds4: ds4.New("test").WithBaseURL(server.URL)}
	content, err := client.RawCall(context.Background(), "system", "user", 101)
	if err != nil {
		t.Fatalf("RawCall() error = %v", err)
	}
	if content != "compressed" {
		t.Errorf("RawCall() content = %q, want %q", content, "compressed")
	}
	if want := []int{101, 100}; !reflect.DeepEqual(received, want) {
		t.Errorf("max_tokens requests = %v, want %v", received, want)
	}
}

func TestMaxTokensUpperBoundRejectsUnrelatedErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{"non-range message", &ds4.APIError{StatusCode: http.StatusBadRequest, Message: "invalid request"}},
		{"non-client error", &ds4.APIError{StatusCode: http.StatusInternalServerError, Message: "valid range of max_tokens is [1, 100]"}},
		{"non-lowering bound", &ds4.APIError{StatusCode: http.StatusBadRequest, Message: "valid range of max_tokens is [1, 200]"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, ok := maxTokensUpperBound(test.err, 100); ok {
				t.Fatal("maxTokensUpperBound() unexpectedly accepted error")
			}
		})
	}
}
