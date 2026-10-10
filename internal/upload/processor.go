// Package upload persists chat attachments and prepares their prompt context.
package upload

import (
	"bytes"
	"context"
	"crypto/md5"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Cyvadra/hephaestus/internal/media"
)

var (
	ErrTooManyFiles  = errors.New("upload: too many files")
	ErrFileTooLarge  = errors.New("upload: file exceeds size limit")
	ErrTotalTooLarge = errors.New("upload: total file size exceeds limit")
)

// Config controls attachment limits and eligible extensions.
type Config struct {
	TextExtensions     []string
	ImageExtensions    []string
	InlineTextMaxBytes int64
	// ExcerptHeadBytes and ExcerptTailBytes bound the head/tail excerpt of
	// text files larger than InlineTextMaxBytes. Zero selects the defaults.
	ExcerptHeadBytes int64
	ExcerptTailBytes int64
	FileMaxBytes     int64
	TotalMaxBytes    int64
	MaxFiles         int
	Now              func() time.Time
}

// Attachment is the persisted representation returned to the API client.
type Attachment struct {
	Path            string `json:"path"`
	Name            string `json:"name"`
	Size            int64  `json:"size"`
	MIME            string `json:"mime"`
	VisualInput     bool   `json:"visual_input"`
	ContentIncluded bool   `json:"content_included"`
	// ContentTruncated reports that only a head/tail excerpt was included.
	ContentTruncated bool `json:"content_truncated,omitempty"`
}

// Result is the prompt prefix and client-visible processing outcome.
type Result struct {
	Prefix      string       `json:"-"`
	Attachments []Attachment `json:"attachments"`
	created     []string
}

// Rollback removes files newly created for this result. Reused files are not
// removed, so callers can defer it until the containing request commits.
func (r *Result) Rollback() error {
	var rollbackErr error
	for _, path := range r.created {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) && rollbackErr == nil {
			rollbackErr = err
		}
	}
	r.created = nil
	return rollbackErr
}

// Commit releases rollback ownership after the containing request persists.
func (r *Result) Commit() {
	r.created = nil
}

// Processor stores uploaded files below a project's uploads directory.
type Processor struct {
	config          Config
	textExtensions  map[string]struct{}
	imageExtensions map[string]struct{}
	mu              sync.Mutex
}

func New(config Config) (*Processor, error) {
	if config.InlineTextMaxBytes <= 0 || config.FileMaxBytes <= 0 || config.TotalMaxBytes <= 0 || config.MaxFiles <= 0 {
		return nil, errors.New("upload: limits must be positive")
	}
	if config.FileMaxBytes > config.TotalMaxBytes {
		return nil, errors.New("upload: incompatible size limits")
	}
	if config.ExcerptHeadBytes <= 0 {
		config.ExcerptHeadBytes = defaultExcerptHeadBytes
	}
	if config.ExcerptTailBytes <= 0 {
		config.ExcerptTailBytes = defaultExcerptTailBytes
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
func (p *Processor) Process(_ context.Context, projectDir string, files []*multipart.FileHeader) (Result, error) {
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
	var prefix strings.Builder
	for _, file := range files {
		name, err := safeName(file.Filename)
		if err != nil {
			_ = result.Rollback()
			return Result{}, err
		}
		path, created, err := p.persist(directory, name, file)
		if err != nil {
			_ = result.Rollback()
			return Result{}, err
		}
		if created {
			result.created = append(result.created, path)
		}
		relativePath := filepath.ToSlash(filepath.Join("uploads", filepath.Base(directory), filepath.Base(path)))
		attachment := Attachment{Path: relativePath, Name: filepath.Base(path), Size: file.Size}
		text := p.extractText(path, extension(name), file.Size)
		attachment.MIME = detectMIME(path)
		_, imageExtension := p.imageExtensions[extension(name)]
		attachment.VisualInput = imageExtension && media.SupportedVisual(attachment.MIME)
		attachment.ContentIncluded = text.content != ""
		attachment.ContentTruncated = text.omittedBytes > 0
		meta := fileMeta{text: text}
		if text.content == "" {
			// Non-text files are never read into the prompt: only describe them.
			meta.mime = attachment.MIME
			if imageExtension {
				meta.dimensions = imageDimensions(path)
			}
		}
		result.Attachments = append(result.Attachments, attachment)
		prefix.WriteString(promptBlock(relativePath, file.Size, meta))
	}
	result.Prefix = prefix.String()
	return result, nil
}

func (p *Processor) persist(directory, name string, file *multipart.FileHeader) (string, bool, error) {
	source, err := file.Open()
	if err != nil {
		return "", false, fmt.Errorf("upload: open %q: %w", file.Filename, err)
	}
	defer source.Close()

	temporary, err := os.CreateTemp(directory, ".upload-*")
	if err != nil {
		return "", false, fmt.Errorf("upload: create temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	hash := md5.New()
	if _, err := io.Copy(io.MultiWriter(temporary, hash), source); err != nil {
		temporary.Close()
		return "", false, fmt.Errorf("upload: write %q: %w", file.Filename, err)
	}
	if err := temporary.Close(); err != nil {
		return "", false, fmt.Errorf("upload: close temporary file: %w", err)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	for index := 0; ; index++ {
		candidate := filepath.Join(directory, numberedName(name, index))
		if _, err := os.Stat(candidate); errors.Is(err, os.ErrNotExist) {
			if err := os.Rename(temporaryPath, candidate); err != nil {
				return "", false, fmt.Errorf("upload: store %q: %w", file.Filename, err)
			}
			return candidate, true, nil
		} else if err != nil {
			return "", false, fmt.Errorf("upload: inspect %q: %w", candidate, err)
		}
		if sameDigest(candidate, hash.Sum(nil)) {
			return candidate, false, nil
		}
	}
}

const (
	defaultExcerptHeadBytes = 4 << 10
	defaultExcerptTailBytes = 2 << 10
)

// textContent is the prompt-visible text of an attachment: the whole file, or
// a head/tail excerpt when omittedBytes > 0.
type textContent struct {
	content      string
	omittedBytes int64
}

// extractText reads readable text files only. Small files are inlined whole;
// larger ones contribute a line-aligned head and tail excerpt. Anything else
// (media, binaries, invalid UTF-8) yields no content.
func (p *Processor) extractText(path, ext string, size int64) textContent {
	if _, ok := p.textExtensions[ext]; !ok {
		return textContent{}
	}
	if size <= p.config.InlineTextMaxBytes || size <= p.config.ExcerptHeadBytes+p.config.ExcerptTailBytes {
		content, err := os.ReadFile(path)
		if err != nil || !readableText(content) {
			return textContent{}
		}
		return textContent{content: string(content)}
	}
	file, err := os.Open(path)
	if err != nil {
		return textContent{}
	}
	defer file.Close()
	head := make([]byte, p.config.ExcerptHeadBytes)
	if _, err := file.ReadAt(head, 0); err != nil {
		return textContent{}
	}
	tail := make([]byte, p.config.ExcerptTailBytes)
	tailStart := size - p.config.ExcerptTailBytes
	if _, err := file.ReadAt(tail, tailStart); err != nil && !errors.Is(err, io.EOF) {
		return textContent{}
	}
	// Align to line boundaries so the excerpt never splits a line or rune.
	if cut := bytes.LastIndexByte(head, '\n'); cut > 0 {
		head = head[:cut+1]
	} else {
		head = trimInvalidSuffix(head)
	}
	if cut := bytes.IndexByte(tail, '\n'); cut >= 0 && cut < len(tail)-1 {
		tailStart += int64(cut + 1)
		tail = tail[cut+1:]
	} else {
		trimmed := trimInvalidPrefix(tail)
		tailStart += int64(len(tail) - len(trimmed))
		tail = trimmed
	}
	if !readableText(head) || !readableText(tail) {
		return textContent{}
	}
	omitted := tailStart - int64(len(head))
	return textContent{
		content:      strings.TrimSuffix(string(head), "\n") + fmt.Sprintf("\n... [truncated: %s omitted] ...\n", formatSize(omitted)) + strings.TrimSuffix(string(tail), "\n"),
		omittedBytes: omitted,
	}
}

func readableText(content []byte) bool {
	return utf8.Valid(content) && bytes.IndexByte(content, 0) < 0
}

func trimInvalidSuffix(content []byte) []byte {
	for i := 0; i < utf8.UTFMax && len(content) > 0 && !utf8.Valid(content); i++ {
		content = content[:len(content)-1]
	}
	return content
}

func trimInvalidPrefix(content []byte) []byte {
	for i := 0; i < utf8.UTFMax && len(content) > 0 && !utf8.RuneStart(content[0]); i++ {
		content = content[1:]
	}
	return content
}

// imageDimensions decodes only the image header.
func imageDimensions(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	config, _, err := image.DecodeConfig(file)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%dx%d", config.Width, config.Height)
}

func detectMIME(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	var header [512]byte
	count, err := file.Read(header[:])
	if err != nil {
		return ""
	}
	return http.DetectContentType(header[:count])
}

type fileMeta struct {
	text       textContent
	mime       string
	dimensions string
}

func promptBlock(path string, size int64, meta fileMeta) string {
	block := fmt.Sprintf("[file name]: %s\n[file size]: %s\n", path, formatSize(size))
	if meta.mime != "" {
		block += "[mime type]: " + meta.mime + "\n"
	}
	if meta.dimensions != "" {
		block += "[image size]: " + meta.dimensions + "\n"
	}
	if meta.text.content != "" {
		if meta.text.omittedBytes > 0 {
			block += "[file excerpt]: head/tail only; read the file for the full content\n"
		}
		block += "[file content begin]\n" + meta.text.content + "\n[file content end]\n"
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
