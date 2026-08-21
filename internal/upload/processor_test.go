package upload

import (
	"bytes"
	"context"
	"errors"
	"mime/multipart"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestProcessInlinesTextAndClassifiesVisualImages(t *testing.T) {
	processor := newProcessor(t)
	result, err := processor.Process(context.Background(), t.TempDir(), multipartFiles(t,
		filePart{name: "notes.md", content: "# Notes"},
		filePart{name: "scan.png", content: string(validPNG)},
	))
	if err != nil {
		t.Fatal(err)
	}
	want := "[file name]: uploads/2026-08-10/notes.md\n[file size]: 0.0 KB\n[file content begin]\n# Notes\n[file content end]\n\n[file name]: uploads/2026-08-10/scan.png\n[file size]: 0.0 KB\n\n"
	if result.Prefix != want {
		t.Fatalf("prefix = %q, want %q", result.Prefix, want)
	}
	if len(result.Attachments) != 2 || !result.Attachments[0].ContentIncluded || !result.Attachments[1].VisualInput || result.Attachments[1].MIME != "image/png" {
		t.Fatalf("unexpected attachments: %+v", result.Attachments)
	}
}

func TestProcessReusesDigestAndRenamesConflicts(t *testing.T) {
	project := t.TempDir()
	processor := newProcessor(t)
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
	processor := newProcessor(t)
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

func TestProcessDoesNotTreatUnsupportedImagesAsVisualInput(t *testing.T) {
	processor := newProcessor(t)
	result, err := processor.Process(context.Background(), t.TempDir(), multipartFiles(t, filePart{name: "scan.bmp", content: "image"}))
	if err != nil {
		t.Fatal(err)
	}
	if result.Attachments[0].VisualInput || result.Attachments[0].ContentIncluded {
		t.Fatalf("unexpected non-visual attachment: %+v", result.Attachments[0])
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

var validPNG = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
	0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
	0x89,
}

func newProcessor(t *testing.T) *Processor {
	t.Helper()
	processor, err := New(Config{
		TextExtensions:     []string{"md", "txt"},
		ImageExtensions:    []string{"png", "jpg", "jpeg", "gif", "webp"},
		InlineTextMaxBytes: 10 << 10,
		FileMaxBytes:       50 << 20,
		TotalMaxBytes:      250 << 20,
		MaxFiles:           5,
		Now:                func() time.Time { return time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	return processor
}
