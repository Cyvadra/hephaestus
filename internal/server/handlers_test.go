package server

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Cyvadra/hephaestus/internal/command"
	"github.com/Cyvadra/hephaestus/internal/registry"
	"github.com/Cyvadra/hephaestus/internal/store"
	"github.com/gin-gonic/gin"
	"gorm.io/datatypes"
)

func TestValidateGenerationOptions(t *testing.T) {
	req := sendMessageRequest{
		ReasoningEffort: "max",
		DisabledTools:   []string{"web_search", " web_fetch ", "web_search", ""},
	}
	if err := validateGenerationOptions(&req); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
	if len(req.DisabledTools) != 2 || req.DisabledTools[0] != "web_search" || req.DisabledTools[1] != "web_fetch" {
		t.Fatalf("expected normalized disabled tools, got %#v", req.DisabledTools)
	}

	req.ReasoningEffort = "low"
	if err := validateGenerationOptions(&req); err == nil {
		t.Fatal("expected low request override to be rejected")
	}
}

func TestValidateBranchSelectionRejectsAmbiguousAndZeroLeaf(t *testing.T) {
	leaf := uint(42)
	if err := validateBranchSelection(sendMessageRequest{SelectRoot: true, ActiveLeafMessageID: &leaf}); err == nil {
		t.Fatal("expected mutually exclusive branch selectors to be rejected")
	}

	zero := uint(0)
	if err := validateBranchSelection(sendMessageRequest{ActiveLeafMessageID: &zero}); err == nil {
		t.Fatal("expected zero active leaf to be rejected")
	}

	if err := validateBranchSelection(sendMessageRequest{SelectRoot: true}); err != nil {
		t.Fatalf("expected root selection to be valid: %v", err)
	}
}

func TestListConciergesUsesPublishedRegistry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	registries := registry.NewStore(&registry.Registry{
		Identities: map[string]registry.Identity{"first": {Name: "first"}},
		Concierges: map[string]registry.Concierge{"first": {Name: "first", Identity: "first"}},
	})
	server := &Server{registries: registries}

	registries.Publish(&registry.Registry{
		Identities: map[string]registry.Identity{"updated": {Name: "updated", ReasoningEffort: registry.ReasoningHigh}},
		Concierges: map[string]registry.Concierge{"updated": {Name: "updated", Identity: "updated"}},
	})
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	server.listConcierges(context)

	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"name":"updated"`) || strings.Contains(recorder.Body.String(), `"name":"first"`) {
		t.Fatalf("unexpected concierge response: status %d, body %s", recorder.Code, recorder.Body.String())
	}
}

func TestBindMessageRequestParsesMultipartGenerationOptions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for name, value := range map[string]string{
		"text":                   "hello",
		"reasoning_effort":       "high",
		"active_leaf_message_id": "42",
	} {
		if err := writer.WriteField(name, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.WriteField("disabled_tools", "web_search"); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("disabled_tools", "web_fetch"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = request
	server := &Server{}
	var req sendMessageRequest

	if _, err := server.bindMessageRequest(context, &req); err != nil {
		t.Fatalf("bind multipart request: %v", err)
	}
	if req.Text != "hello" || req.ReasoningEffort != "high" {
		t.Fatalf("unexpected request fields: %+v", req)
	}
	if len(req.DisabledTools) != 2 || req.DisabledTools[0] != "web_search" || req.DisabledTools[1] != "web_fetch" {
		t.Fatalf("unexpected disabled tools: %#v", req.DisabledTools)
	}
	if req.ActiveLeafMessageID == nil || *req.ActiveLeafMessageID != 42 {
		t.Fatalf("expected active leaf 42, got %v", req.ActiveLeafMessageID)
	}
}

func TestBindMessageRequestAllowsGenerationOnlyJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"reasoning_effort":"high","disabled_tools":["web_search"]}`))
	request.Header.Set("Content-Type", "application/json")
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = request
	server := &Server{}
	var req sendMessageRequest

	if _, err := server.bindMessageRequest(context, &req); err != nil {
		t.Fatalf("bind generation-only request: %v", err)
	}
	if req.Text != "" || req.ReasoningEffort != "high" || len(req.DisabledTools) != 1 || req.DisabledTools[0] != "web_search" {
		t.Fatalf("unexpected request fields: %+v", req)
	}
}

func TestPrepareMessageRunFromRequestExecutesSlashCommand(t *testing.T) {
	gin.SetMode(gin.TestMode)
	server := &Server{commands: command.NewService(nil, nil, nil, nil, nil, nil, nil, nil)}
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Params = gin.Params{{Key: "id", Value: "7"}}

	_, _, _, execute, _ := server.prepareMessageRunFromRequest(context, "/ping", sendMessageRequest{})
	if execute != nil {
		t.Fatal("slash command should not create a chat run executor")
	}
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"command_response":"pong"`) {
		t.Fatalf("unexpected command response: status %d body %s", recorder.Code, recorder.Body.String())
	}
}

func TestEmitRunDoneUsesStoredSendMessageResponse(t *testing.T) {
	messageID := uint(9)
	run := &store.ChatRun{Status: store.ChatRunSucceeded, Result: datatypes.JSON([]byte(`{"message":{"ID":9},"metadata":{"uploads":{"attachments":[]}},"branch_not_activated":true}`))}
	var event string
	var data any
	emitRunDone(func(name string, payload any) {
		event = name
		data = payload
	}, run)
	done, ok := data.(chatRunDone)
	if event != "done" || !ok {
		t.Fatalf("expected done sendMessageResponse, got %q %#v", event, data)
	}
	response := done.Response
	if response.Message == nil || response.Message.ID != messageID || !response.BranchNotActivated {
		t.Fatalf("unexpected done response: %+v", response)
	}
	if _, ok := response.Metadata["uploads"]; !ok {
		t.Fatalf("expected upload metadata, got %+v", response.Metadata)
	}
}
