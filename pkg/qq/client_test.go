package qq

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestClientSendMarkdownAndReusesToken(t *testing.T) {
	var tokenRequests atomic.Int32
	var messageRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/app/getAppAccessToken":
			tokenRequests.Add(1)
			var payload map[string]string
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if payload["appId"] != "app" || payload["clientSecret"] != "secret" {
				t.Fatalf("unexpected token payload: %#v", payload)
			}
			_, _ = response.Write([]byte(`{"access_token":"token","expires_in":"7200"}`))
		case "/v2/users/user-openid/messages":
			messageRequests.Add(1)
			if got := request.Header.Get("Authorization"); got != "QQBot token" {
				t.Fatalf("Authorization = %q", got)
			}
			if got := request.Header.Get("X-Union-Appid"); got != "app" {
				t.Fatalf("X-Union-Appid = %q", got)
			}
			var payload struct {
				MessageType int `json:"msg_type"`
				Markdown    struct {
					Content string `json:"content"`
				} `json:"markdown"`
			}
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if payload.MessageType != 2 || payload.Markdown.Content != "# hello" {
				t.Fatalf("unexpected message payload: %#v", payload)
			}
			_, _ = response.Write([]byte(`{"id":"message-1","timestamp":"2026-08-12T12:00:00+08:00"}`))
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	client := New(Config{AppID: "app", AppSecret: "secret", UserOpenID: "user-openid", BaseURL: server.URL, HTTPClient: server.Client()})
	for range 2 {
		message, err := client.SendMarkdown(context.Background(), "# hello")
		if err != nil {
			t.Fatalf("SendMarkdown: %v", err)
		}
		if message.ID != "message-1" {
			t.Fatalf("message id = %q", message.ID)
		}
	}
	if tokenRequests.Load() != 1 || messageRequests.Load() != 2 {
		t.Fatalf("token requests = %d, message requests = %d", tokenRequests.Load(), messageRequests.Load())
	}
}

func TestClientRefreshesTokenOnceConcurrently(t *testing.T) {
	var tokenRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		if request.URL.Path == "/app/getAppAccessToken" {
			tokenRequests.Add(1)
			_, _ = response.Write([]byte(`{"access_token":"token","expires_in":7200}`))
			return
		}
		_, _ = response.Write([]byte(`{"id":"message"}`))
	}))
	defer server.Close()

	client := New(Config{AppID: "app", AppSecret: "secret", UserOpenID: "user", BaseURL: server.URL, HTTPClient: server.Client()})
	var wait sync.WaitGroup
	for range 10 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if _, err := client.SendMarkdown(context.Background(), "hello"); err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wait.Wait()
	if tokenRequests.Load() != 1 {
		t.Fatalf("token requests = %d, want 1", tokenRequests.Load())
	}
}

func TestClientRefreshesAfterUnauthorized(t *testing.T) {
	var tokenRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		if request.URL.Path == "/app/getAppAccessToken" {
			requestNumber := tokenRequests.Add(1)
			_, _ = response.Write([]byte(`{"access_token":"token-` + string(rune('0'+requestNumber)) + `","expires_in":7200}`))
			return
		}
		if request.Header.Get("Authorization") == "QQBot token-1" {
			response.WriteHeader(http.StatusUnauthorized)
			_, _ = response.Write([]byte(`{"err_code":11243,"message":"expired","trace_id":"trace-1"}`))
			return
		}
		_, _ = response.Write([]byte(`{"id":"message-after-refresh"}`))
	}))
	defer server.Close()

	client := New(Config{AppID: "app", AppSecret: "secret", UserOpenID: "user", BaseURL: server.URL, HTTPClient: server.Client()})
	message, err := client.SendMarkdown(context.Background(), "hello")
	if err != nil {
		t.Fatalf("SendMarkdown: %v", err)
	}
	if message.ID != "message-after-refresh" {
		t.Fatalf("message id = %q", message.ID)
	}
	if tokenRequests.Load() != 2 {
		t.Fatalf("token requests = %d, want 2", tokenRequests.Load())
	}
}

func TestClientNotConfigured(t *testing.T) {
	client := New(Config{})
	if client.Configured() {
		t.Fatal("expected unconfigured")
	}
	if _, err := client.SendMarkdown(context.Background(), "hello"); err == nil || !strings.Contains(err.Error(), "not initialized") {
		t.Fatalf("expected not-initialized error, got %v", err)
	}
}

func TestClientReportsAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		if request.URL.Path == "/app/getAppAccessToken" {
			_, _ = response.Write([]byte(`{"access_token":"token","expires_in":7200}`))
			return
		}
		_, _ = response.Write([]byte(`{"err_code":40054013,"message":"user rejected","trace_id":"trace-rejected"}`))
	}))
	defer server.Close()

	client := New(Config{AppID: "app", AppSecret: "secret", UserOpenID: "user", BaseURL: server.URL, HTTPClient: server.Client()})
	_, err := client.SendMarkdown(context.Background(), "hello")
	if err == nil || !strings.Contains(err.Error(), "40054013") || !strings.Contains(err.Error(), "trace-rejected") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestClientRejectsInvalidTokenResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`not-json`))
	}))
	defer server.Close()

	client := New(Config{AppID: "app", AppSecret: "secret", UserOpenID: "user", BaseURL: server.URL, HTTPClient: server.Client()})
	_, err := client.SendMarkdown(context.Background(), "hello")
	if err == nil || !strings.Contains(err.Error(), "invalid JSON response") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLiveQQNotification(t *testing.T) {
	if os.Getenv("HEPHAESTUS_RUN_LIVE_QQ_NOTIFICATION") == "" {
		t.Skip("set HEPHAESTUS_RUN_LIVE_QQ_NOTIFICATION=1 to send a live QQ notification")
	}
	client := New(Config{
		AppID:      os.Getenv("HEPHAESTUS_QQ_APP_ID"),
		AppSecret:  os.Getenv("HEPHAESTUS_QQ_APP_SECRET"),
		UserOpenID: os.Getenv("HEPHAESTUS_QQ_USER_OPENID"),
	})
	message, err := client.SendMarkdown(context.Background(), "# Hephaestus QQ notification test\n\nLive API test sent on 2026-08-12.")
	if err != nil {
		t.Fatalf("live QQ notification failed: %v", err)
	}
	t.Logf("message id: %s", message.ID)
}
