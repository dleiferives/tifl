package document

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

var (
	ErrUnsupportedFormat      = errors.New("document extraction: unsupported format")
	ErrUnsupportedContentType = errors.New("document extraction: unsupported content type")
	ErrInvalidContentType     = errors.New("document extraction: invalid content type")
	ErrTooLarge               = errors.New("document extraction: file too large")
	ErrInvalidEncoding        = errors.New("document extraction: invalid encoding")
	ErrEmptyOutput            = errors.New("document extraction: extracted text is required")
)

type Metadata struct {
	Filename    string
	ContentType string
}

type Extractor interface {
	Extract(io.Reader, int64) (string, error)
}

type Format struct {
	Extension    string
	ContentTypes []string
	Extractor    Extractor
}

type Registry struct {
	byExtension   map[string]registeredFormat
	byContentType map[string]registeredFormat
	extensions    []string
}

type registeredFormat struct {
	key       string
	extension string
	extractor Extractor
}

func DefaultRegistry() Registry {
	return NewRegistry(Format{
		Extension:    ".txt",
		ContentTypes: []string{"text/plain"},
		Extractor:    TextExtractor{},
	})
}

func NewRegistry(formats ...Format) Registry {
	registry := Registry{
		byExtension:   make(map[string]registeredFormat, len(formats)),
		byContentType: make(map[string]registeredFormat),
	}
	for _, format := range formats {
		ext := normalizeExtension(format.Extension)
		if ext == "" || format.Extractor == nil {
			continue
		}
		registered := registeredFormat{
			key:       ext,
			extension: ext,
			extractor: format.Extractor,
		}
		registry.byExtension[ext] = registered
		registry.extensions = append(registry.extensions, ext)
		for _, contentType := range format.ContentTypes {
			mediaType, err := normalizeContentType(contentType)
			if err != nil || mediaType == "" {
				continue
			}
			registry.byContentType[mediaType] = registered
		}
	}
	sort.Strings(registry.extensions)
	return registry
}

func (r Registry) Extract(reader io.Reader, meta Metadata, maxBytes int64) (string, error) {
	extractor, err := r.Select(meta)
	if err != nil {
		return "", err
	}
	return extractor.Extract(reader, maxBytes)
}

func (r Registry) Select(meta Metadata) (Extractor, error) {
	ext := normalizeExtension(filepath.Ext(meta.Filename))
	contentType, err := normalizeContentType(meta.ContentType)
	if err != nil {
		return nil, fmt.Errorf("%w: %q", ErrInvalidContentType, meta.ContentType)
	}

	format, ok := r.byExtension[ext]
	if !ok {
		return nil, fmt.Errorf("%w: extension %q is not supported; supported extensions: %s", ErrUnsupportedFormat, ext, strings.Join(r.extensions, ", "))
	}
	if contentType == "" {
		return format.extractor, nil
	}
	contentFormat, ok := r.byContentType[contentType]
	if !ok || contentFormat.key != format.key {
		return nil, fmt.Errorf("%w: content type %q is not supported for %s uploads", ErrUnsupportedContentType, contentType, format.extension)
	}
	return format.extractor, nil
}

type TextExtractor struct{}

func (TextExtractor) Extract(reader io.Reader, maxBytes int64) (string, error) {
	data, err := io.ReadAll(io.LimitReader(reader, maxBytes+1))
	if err != nil {
		return "", fmt.Errorf("read text file: %w", err)
	}
	if int64(len(data)) > maxBytes {
		return "", fmt.Errorf("%w: text file must be %d bytes or smaller", ErrTooLarge, maxBytes)
	}
	if !utf8.Valid(data) {
		return "", fmt.Errorf("%w: text file must be UTF-8", ErrInvalidEncoding)
	}
	text := NormalizeText(string(data))
	if text == "" {
		return "", ErrEmptyOutput
	}
	return text, nil
}

func NormalizeText(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return strings.TrimSpace(text)
}

func normalizeExtension(ext string) string {
	ext = strings.ToLower(strings.TrimSpace(ext))
	if ext == "" {
		return ""
	}
	if !strings.HasPrefix(ext, ".") {
		return "." + ext
	}
	return ext
}

func normalizeContentType(header string) (string, error) {
	if strings.TrimSpace(header) == "" {
		return "", nil
	}
	mediaType, _, err := mime.ParseMediaType(header)
	if err != nil {
		return "", err
	}
	return strings.ToLower(strings.TrimSpace(mediaType)), nil
}
