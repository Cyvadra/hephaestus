package upload

import (
	"bytes"
	"context"
	"errors"
	"mime/multipart"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeRecognizer struct {
	text string
	err  error
}

func (r fakeRecognizer) Recognize(_ context.Context, _ []byte) (string, error) { return r.text, r.err }

func TestProcessInlinesTextAndOCR(t *testing.T) {
	processor := newProcessor(t, fakeRecognizer{text: "first line\nsecond line"})
	result, err := processor.Process(context.Background(), t.TempDir(), multipartFiles(t,
		filePart{name: "notes.md", content: "# Notes"},
		filePart{name: "scan.png", content: "image"},
	))
	if err != nil {
		t.Fatal(err)
	}
	want := "[file name]: uploads/2026-08-10/notes.md\n[file size]: 0.0 KB\n[file content begin]\n# Notes\n[file content end]\n\n[file name]: uploads/2026-08-10/scan.png\n[file size]: 0.0 KB\n[file content begin]\nfirst line\nsecond line\n[file content end]\n\n"
	if result.Prefix != want {
		t.Fatalf("prefix = %q, want %q", result.Prefix, want)
	}
	if len(result.Attachments) != 2 || !result.Attachments[0].ContentIncluded || !result.Attachments[1].ContentIncluded {
		t.Fatalf("unexpected attachments: %+v", result.Attachments)
	}
}

func TestProcessReusesDigestAndRenamesConflicts(t *testing.T) {
	project := t.TempDir()
	processor := newProcessor(t, nil)
	first, err := processor.Process(context.Background(), project, multipartFiles(t, filePart{name: "report.txt", content: "one"}))
	if err != nil {
		t.Fatal(err)
	}
	second, err := processor.Process(context.Background(), project, multipartFiles(t, filePart{name: "report.txt", content: "one"}))
	if err != nil {
		t.Fatal(err)
	}
	third, err := processor.Process(context.Background(), project, multipartFiles(t, filePart{name: "report.txt", content: "two"}))
	if err != nil {
		t.Fatal(err)
	}
	if first.Attachments[0].Path != second.Attachments[0].Path {
		t.Fatalf("expected digest reuse: %q != %q", first.Attachments[0].Path, second.Attachments[0].Path)
	}
	if got, want := third.Attachments[0].Path, "uploads/2026-08-10/report (1).txt"; got != want {
		t.Fatalf("conflict path = %q, want %q", got, want)
	}
	entries, err := os.ReadDir(filepath.Join(project, "uploads", "2026-08-10"))
	if err != nil || len(entries) != 2 {
		t.Fatalf("entries = %v, err = %v", entries, err)
	}
}

func TestResultRollbackRemovesOnlyNewFiles(t *testing.T) {
	project := t.TempDir()
	processor := newProcessor(t, nil)
	first, err := processor.Process(context.Background(), project, multipartFiles(t, filePart{name: "report.txt", content: "one"}))
	if err != nil {
		t.Fatal(err)
	}
	second, err := processor.Process(context.Background(), project, multipartFiles(t,
		filePart{name: "report.txt", content: "one"},
		filePart{name: "draft.txt", content: "two"},
	))
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if _, err := os.Stat(filepath.Join(project, filepath.FromSlash(first.Attachments[0].Path))); err != nil {
		t.Fatalf("reused file was removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(project, filepath.FromSlash(second.Attachments[1].Path))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new file remains after rollback: %v", err)
	}
}

func TestProcessDegradesOCRAndRejectsLimits(t *testing.T) {
	processor := newProcessor(t, nil)
	result, err := processor.Process(context.Background(), t.TempDir(), multipartFiles(t, filePart{name: "scan.jpg", content: "image"}))
	if err != nil {
		t.Fatal(err)
	}
	if result.Attachments[0].ContentIncluded || len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "OCR is not configured") {
		t.Fatalf("unexpected OCR fallback: %+v", result)
	}

	processor.config.MaxFiles = 1
	_, err = processor.Process(context.Background(), t.TempDir(), multipartFiles(t, filePart{name: "one.txt", content: "a"}, filePart{name: "two.txt", content: "b"}))
	if !errors.Is(err, ErrTooManyFiles) {
		t.Fatalf("error = %v, want ErrTooManyFiles", err)
	}
}

type filePart struct {
	name    string
	content string
}

func multipartFiles(t *testing.T, parts ...filePart) []*multipart.FileHeader {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for _, part := range parts {
		field, err := writer.CreateFormFile("files", part.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := field.Write([]byte(part.content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	reader := multipart.NewReader(&body, writer.Boundary())
	form, err := reader.ReadForm(1 << 20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { form.RemoveAll() })
	return form.File["files"]
}

func newProcessor(t *testing.T, recognizer Recognizer) *Processor {
	t.Helper()
	processor, err := New(Config{
		TextExtensions:     []string{"md", "txt"},
		ImageExtensions:    []string{"png", "jpg"},
		InlineTextMaxBytes: 10 << 10,
		OCRImageMaxBytes:   4 << 20,
		FileMaxBytes:       50 << 20,
		TotalMaxBytes:      250 << 20,
		MaxFiles:           5,
		Recognizer:         recognizer,
		Now:                func() time.Time { return time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	return processor
}
