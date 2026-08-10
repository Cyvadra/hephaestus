// Package upload persists chat attachments and prepares their prompt context.
package upload

import (
	"context"
	"crypto/md5"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

var (
	ErrTooManyFiles  = errors.New("upload: too many files")
	ErrFileTooLarge  = errors.New("upload: file exceeds size limit")
	ErrTotalTooLarge = errors.New("upload: total file size exceeds limit")
)

// Recognizer extracts text from an image.
type Recognizer interface {
	Recognize(context.Context, []byte) (string, error)
}

// Config controls attachment limits and eligible extensions.
type Config struct {
	TextExtensions     []string
	ImageExtensions    []string
	InlineTextMaxBytes int64
	OCRImageMaxBytes   int64
	FileMaxBytes       int64
	TotalMaxBytes      int64
	MaxFiles           int
	Recognizer         Recognizer
	Now                func() time.Time
}

// Attachment is the persisted representation returned to the API client.
type Attachment struct {
	Path            string `json:"path"`
	Size            int64  `json:"size"`
	ContentIncluded bool   `json:"content_included"`
}

// Result is the prompt prefix and client-visible processing outcome.
type Result struct {
	Prefix      string       `json:"-"`
	Attachments []Attachment `json:"attachments"`
	Warnings    []string     `json:"warnings,omitempty"`
}

// Processor stores uploaded files below a project's uploads directory.
type Processor struct {
	config          Config
	textExtensions  map[string]struct{}
	imageExtensions map[string]struct{}
	mu              sync.Mutex
}

func New(config Config) (*Processor, error) {
	if config.InlineTextMaxBytes <= 0 || config.OCRImageMaxBytes <= 0 || config.FileMaxBytes <= 0 || config.TotalMaxBytes <= 0 || config.MaxFiles <= 0 {
		return nil, errors.New("upload: limits must be positive")
	}
	if config.OCRImageMaxBytes > config.FileMaxBytes || config.FileMaxBytes > config.TotalMaxBytes {
		return nil, errors.New("upload: incompatible size limits")
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	return &Processor{
		config:          config,
		textExtensions:  extensionSet(config.TextExtensions),
		imageExtensions: extensionSet(config.ImageExtensions),
	}, nil
}

// MaxRequestBytes returns the maximum multipart request body size, including
// room for form boundaries and field metadata.
func (p *Processor) MaxRequestBytes() int64 {
	return p.config.TotalMaxBytes + (1 << 20)
}

// Process saves files and creates prompt blocks in upload order.
func (p *Processor) Process(ctx context.Context, projectDir string, files []*multipart.FileHeader) (Result, error) {
	if len(files) > p.config.MaxFiles {
		return Result{}, ErrTooManyFiles
	}
	var total int64
	for _, file := range files {
		if file == nil || file.Size < 0 || file.Size > p.config.FileMaxBytes {
			return Result{}, ErrFileTooLarge
		}
		total += file.Size
		if total > p.config.TotalMaxBytes {
			return Result{}, ErrTotalTooLarge
		}
	}

	directory := filepath.Join(projectDir, "uploads", p.config.Now().Format("2006-01-02"))
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return Result{}, fmt.Errorf("upload: create directory: %w", err)
	}

	result := Result{Attachments: make([]Attachment, 0, len(files))}
	for _, file := range files {
		name, err := safeName(file.Filename)
		if err != nil {
			return Result{}, err
		}
		path, err := p.persist(directory, name, file)
		if err != nil {
			return Result{}, err
		}
		relativePath := filepath.ToSlash(filepath.Join("uploads", filepath.Base(directory), filepath.Base(path)))
		attachment := Attachment{Path: relativePath, Size: file.Size}
		content, warning := p.extract(ctx, path, extension(name), file.Size)
		if warning != "" {
			result.Warnings = append(result.Warnings, fmt.Sprintf("%s: %s", relativePath, warning))
		}
		if content != "" {
			attachment.ContentIncluded = true
		}
		result.Attachments = append(result.Attachments, attachment)
		result.Prefix += promptBlock(relativePath, file.Size, content)
	}
	return result, nil
}

func (p *Processor) persist(directory, name string, file *multipart.FileHeader) (string, error) {
	source, err := file.Open()
	if err != nil {
		return "", fmt.Errorf("upload: open %q: %w", file.Filename, err)
	}
	defer source.Close()

	temporary, err := os.CreateTemp(directory, ".upload-*")
	if err != nil {
		return "", fmt.Errorf("upload: create temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	hash := md5.New()
	if _, err := io.Copy(io.MultiWriter(temporary, hash), source); err != nil {
		temporary.Close()
		return "", fmt.Errorf("upload: write %q: %w", file.Filename, err)
	}
	if err := temporary.Close(); err != nil {
		return "", fmt.Errorf("upload: close temporary file: %w", err)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	for index := 0; ; index++ {
		candidate := filepath.Join(directory, numberedName(name, index))
		if _, err := os.Stat(candidate); errors.Is(err, os.ErrNotExist) {
			if err := os.Rename(temporaryPath, candidate); err != nil {
				return "", fmt.Errorf("upload: store %q: %w", file.Filename, err)
			}
			return candidate, nil
		} else if err != nil {
			return "", fmt.Errorf("upload: inspect %q: %w", candidate, err)
		}
		if sameDigest(candidate, hash.Sum(nil)) {
			return candidate, nil
		}
	}
}

func (p *Processor) extract(ctx context.Context, path, ext string, size int64) (string, string) {
	if _, ok := p.textExtensions[ext]; ok && size <= p.config.InlineTextMaxBytes {
		content, err := os.ReadFile(path)
		if err != nil {
			return "", "could not read text content"
		}
		if !utf8.Valid(content) {
			return "", "text content is not valid UTF-8"
		}
		return string(content), ""
	}
	if _, ok := p.imageExtensions[ext]; ok {
		if size > p.config.OCRImageMaxBytes {
			return "", "image exceeds OCR size limit"
		}
		if p.config.Recognizer == nil {
			return "", "OCR is not configured"
		}
		image, err := os.ReadFile(path)
		if err != nil {
			return "", "could not read image content"
		}
		content, err := p.config.Recognizer.Recognize(ctx, image)
		if err != nil || strings.TrimSpace(content) == "" {
			return "", "OCR could not extract text"
		}
		return content, ""
	}
	return "", ""
}

func promptBlock(path string, size int64, content string) string {
	block := fmt.Sprintf("[file name]: %s\n[file size]: %s\n", path, formatSize(size))
	if content != "" {
		block += "[file content begin]\n" + content + "\n[file content end]\n"
	}
	return block + "\n"
}

func formatSize(size int64) string {
	if size >= 1<<20 {
		return fmt.Sprintf("%.1f MB", float64(size)/(1<<20))
	}
	return fmt.Sprintf("%.1f KB", float64(size)/(1<<10))
}

func safeName(name string) (string, error) {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	if name == "" || name == "." || name == ".." || name == string(filepath.Separator) || strings.ContainsRune(name, 0) {
		return "", errors.New("upload: invalid file name")
	}
	return name, nil
}

func numberedName(name string, index int) string {
	if index == 0 {
		return name
	}
	ext := extension(name)
	base := strings.TrimSuffix(name, "."+ext)
	if ext == "" {
		base = name
		return fmt.Sprintf("%s (%d)", base, index)
	}
	return fmt.Sprintf("%s (%d).%s", base, index, ext)
}

func extension(name string) string {
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(name)), ".")
	return ext
}

func extensionSet(extensions []string) map[string]struct{} {
	set := make(map[string]struct{}, len(extensions))
	for _, ext := range extensions {
		if ext = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(ext)), "."); ext != "" {
			set[ext] = struct{}{}
		}
	}
	return set
}

func sameDigest(path string, want []byte) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	hash := md5.New()
	if _, err := io.Copy(hash, file); err != nil {
		return false
	}
	return string(hash.Sum(nil)) == string(want)
}
