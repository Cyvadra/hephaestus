package channel

import (
	"context"
	"testing"
	"time"

	"github.com/Cyvadra/hephaestus/internal/store"
	"github.com/Cyvadra/hephaestus/pkg/channels"
)

func TestIsApproval(t *testing.T) {
	for _, input := range []string{approvalConfirmedChinese, "YES", " y ", "1"} {
		if !isApproval(input) {
			t.Errorf("isApproval(%q) = false", input)
		}
	}
	for _, input := range []string{"好的，确认执行", "yes please", "reply: y", "no", "deny", "maybe", "10", ""} {
		if isApproval(input) {
			t.Errorf("isApproval(%q) = true", input)
		}
	}
}

func TestAutomaticApprovalCommand(t *testing.T) {
	for _, input := range []string{
		automaticApprovalEnableChinese,
		" " + automaticApprovalEnableSessionChinese + " ",
		"APPROVE ALL",
		automaticApprovalEnableSessionEnglish,
	} {
		enabled, ok := automaticApprovalCommand(input)
		if !ok || !enabled {
			t.Errorf("automaticApprovalCommand(%q) = (%v, %v), want (true, true)", input, enabled, ok)
		}
	}
	for _, input := range []string{automaticApprovalCancelChinese, "CANCEL AUTOMATIC APPROVAL"} {
		enabled, ok := automaticApprovalCommand(input)
		if !ok || enabled {
			t.Errorf("automaticApprovalCommand(%q) = (%v, %v), want (false, true)", input, enabled, ok)
		}
	}
	if _, ok := automaticApprovalCommand("全部允许吧"); ok {
		t.Error("unexpected automatic approval command match")
	}
}

func TestChannelTurnOptionsPreservesExpectedLeaf(t *testing.T) {
	leaf := uint(42)
	options := channelTurnOptions(&leaf, nil, nil)
	if options.ExpectedLeaf == nil || *options.ExpectedLeaf != leaf {
		t.Fatalf("ExpectedLeaf = %v, want %d", options.ExpectedLeaf, leaf)
	}
}

func TestCollectInboundCombinesTextThenImage(t *testing.T) {
	queue := make(chan channels.InboundMessage, 1)
	queue <- channels.InboundMessage{Attachments: []channels.Attachment{{Name: "image.png", MIME: "image/png"}}}
	message, deferred, ok := collectInbound(queue, channels.InboundMessage{Content: "describe this"}, time.Second, false)
	if !ok || deferred != nil || message.Content != "describe this" || len(message.Attachments) != 1 {
		t.Fatalf("message = %+v, deferred = %+v, ok = %v", message, deferred, ok)
	}
}

func TestCollectInboundCombinesImageThenText(t *testing.T) {
	queue := make(chan channels.InboundMessage, 1)
	queue <- channels.InboundMessage{Content: "describe this"}
	message, deferred, ok := collectInbound(queue, channels.InboundMessage{Attachments: []channels.Attachment{{Name: "image.png"}}}, time.Second, true)
	if !ok || deferred != nil || message.Content != "describe this" || len(message.Attachments) != 1 {
		t.Fatalf("message = %+v, deferred = %+v, ok = %v", message, deferred, ok)
	}
}

func TestCollectInboundAddsImageHintAfterTimeout(t *testing.T) {
	queue := make(chan channels.InboundMessage)
	message, deferred, ok := collectInbound(queue, channels.InboundMessage{Attachments: []channels.Attachment{{Name: "image.png"}}}, time.Millisecond, true)
	if !ok || deferred != nil || message.Content != "[hint: user just sent this image]" {
		t.Fatalf("message = %+v, deferred = %+v, ok = %v", message, deferred, ok)
	}
}

func TestChannelTurnOptionsClassifiesVisualInputs(t *testing.T) {
	options := channelTurnOptions(nil, []channels.Attachment{{Path: "uploads/a.png", Name: "a.png", MIME: "image/png; charset=binary"}, {Path: "uploads/a.bmp", Name: "a.bmp", MIME: ""}}, nil)
	if len(options.UploadAttachments) != 2 || options.UploadAttachments[0].Kind != store.MessageAttachmentVisualInput || options.UploadAttachments[1].Kind != store.MessageAttachmentUserUpload {
		t.Fatalf("upload attachments = %+v", options.UploadAttachments)
	}
}

func TestCollectInboundDefersFollowingText(t *testing.T) {
	queue := make(chan channels.InboundMessage, 1)
	queue <- channels.InboundMessage{Content: "second"}
	message, deferred, ok := collectInbound(queue, channels.InboundMessage{Content: "first"}, time.Second, false)
	if !ok || message.Content != "first" || deferred == nil || deferred.Content != "second" {
		t.Fatalf("message = %+v, deferred = %+v, ok = %v", message, deferred, ok)
	}
}

func TestIsStopCommand(t *testing.T) {
	for _, input := range []string{"/stop", " /stop ", "/stop now"} {
		if !isStopCommand(input) {
			t.Errorf("isStopCommand(%q) = false", input)
		}
	}
	for _, input := range []string{"stop", "/status", ""} {
		if isStopCommand(input) {
			t.Errorf("isStopCommand(%q) = true", input)
		}
	}
}

func TestStopClosesQueuesAndPreventsNewWorkers(t *testing.T) {
	svc := New(nil, nil, nil, nil, nil, nil, nil, nil)
	queue := make(chan channels.InboundMessage)
	svc.queues["qq\x00chat"] = queue
	svc.workers.Add(1)
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		svc.runStack(context.Background(), queue)
	}()

	if err := svc.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}

	select {
	case <-workerDone:
	default:
		t.Fatal("worker did not stop")
	}
	svc.handleInbound(context.Background(), channels.InboundMessage{Channel: "qq", ChatID: "after-stop"})
	if len(svc.queues) != 0 {
		t.Fatalf("queues after stopped handleInbound = %d, want 0", len(svc.queues))
	}
}
