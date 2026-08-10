package ocr

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRecognizeImageAndCacheToken(t *testing.T) {
	var tokenRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/token":
			tokenRequests.Add(1)
			assertFormValue(t, request, "client_id", "api key")
			assertFormValue(t, request, "client_secret", "secret")
			io.WriteString(response, `{"access_token":"token value","expires_in":3600}`)
		case "/ocr":
			if got := request.URL.Query().Get("access_token"); got != "token value" {
				t.Errorf("access_token = %q", got)
			}
			assertFormValue(t, request, "image", base64.StdEncoding.EncodeToString([]byte("image")))
			assertFormValue(t, request, "probability", "true")
			io.WriteString(response, `{"log_id":1,"words_result_num":1,"words_result":[{"words":"hello"}]}`)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	client := testClient(server.URL)
	client.Init("api key", "secret")
	for range 2 {
		result, err := client.RecognizeImage(context.Background(), []byte("image"), Options{Probability: true})
		if err != nil {
			t.Fatal(err)
		}
		if result.WordsResult[0].Words != "hello" {
			t.Fatalf("unexpected result: %#v", result)
		}
	}
	if got := tokenRequests.Load(); got != 1 {
		t.Fatalf("token requests = %d, want 1", got)
	}
}

func TestRecognizeURLReturnsAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/token" {
			io.WriteString(response, `{"access_token":"token","expires_in":3600}`)
			return
		}
		io.WriteString(response, `{"error_code":216201,"error_msg":"image format error"}`)
	}))
	defer server.Close()

	client := testClient(server.URL)
	client.Init("key", "secret")
	_, err := client.RecognizeURL(context.Background(), "https://example.com/image.png", Options{})
	var apiError *APIError
	if !errors.As(err, &apiError) || apiError.Code != 216201 {
		t.Fatalf("error = %v, want APIError 216201", err)
	}
}

func TestRecognizeRequiresInit(t *testing.T) {
	client := testClient("http://unused")
	_, err := client.RecognizeURL(context.Background(), "https://example.com/image.png", Options{})
	if err == nil || !strings.Contains(err.Error(), "not initialized") {
		t.Fatalf("error = %v", err)
	}
}

func testClient(serverURL string) *Client {
	return &Client{
		httpClient:    http.DefaultClient,
		tokenEndpoint: serverURL + "/token",
		ocrEndpoint:   serverURL + "/ocr",
	}
}

func assertFormValue(t *testing.T, request *http.Request, key, want string) {
	t.Helper()
	if request.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
		t.Errorf("Content-Type = %q", request.Header.Get("Content-Type"))
	}
	if err := request.ParseForm(); err != nil {
		t.Fatal(err)
	}
	if got := request.PostForm.Get(key); got != want {
		t.Errorf("%s = %q, want %q", key, got, want)
	}
}
