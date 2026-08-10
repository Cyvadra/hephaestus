package server

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Cyvadra/hephaestus/internal/chat"
	"github.com/Cyvadra/hephaestus/internal/command"
	"github.com/gin-gonic/gin"
)

func TestStreamTurnFlushesProgressBeforeCompletion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	release := make(chan struct{})
	server := &Server{commands: command.NewService(nil, nil, nil, nil, nil, nil, nil, nil)}
	engine := gin.New()
	engine.GET("/stream", func(c *gin.Context) {
		server.streamTurn(c, 1, func(_ context.Context, onDelta func(chat.StreamEvent)) (*chat.TurnResult, error) {
			onDelta(chat.StreamEvent{Type: "tool_output", ToolCall: &chat.StreamToolCall{
				CallIndex: 0,
				Index:     0,
				ID:        "call-1",
				Name:      "shell",
				Result:    "started\n",
				Status:    "calling",
			}})
			<-release
			return &chat.TurnResult{}, nil
		})
	})

	httpServer := httptest.NewServer(engine)
	defer httpServer.Close()

	response, err := http.Get(httpServer.URL + "/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.Header.Get("X-Accel-Buffering") != "no" {
		t.Fatalf("expected proxy buffering to be disabled, got %q", response.Header.Get("X-Accel-Buffering"))
	}

	lineCh := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(response.Body).ReadString('\n')
		lineCh <- line
	}()
	select {
	case line := <-lineCh:
		if !strings.Contains(line, "event:tool_output") {
			t.Fatalf("expected tool_output before completion, got %q", line)
		}
	case <-time.After(time.Second):
		t.Fatal("tool output was not flushed before turn completion")
	}
	close(release)
}
