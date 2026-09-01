package reader

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// MissingEnglishDefinition is one target-language headword that was present in
// the reader but absent from the imported English Wiktionary dataset.
type MissingEnglishDefinition struct {
	Language    string `json:"language"`
	Key         string `json:"key"`
	NativeGloss string `json:"native_gloss,omitempty"`
	FirstSeenAt string `json:"first_seen_at"`
}

// MissingEnglishRecorder persists a deduplicated backlog for later batch work.
type MissingEnglishRecorder interface {
	Record(ctx context.Context, language, key, nativeGloss string) error
}

// JSONMissingEnglishRecorder atomically maintains a small JSON array. One
// process owns this runtime file in current deployments; the mutex protects
// concurrent reader requests inside that process.
type JSONMissingEnglishRecorder struct {
	path string
	now  func() time.Time
	mu   sync.Mutex
}

func NewJSONMissingEnglishRecorder(path string) *JSONMissingEnglishRecorder {
	return &JSONMissingEnglishRecorder{path: path, now: time.Now}
}

func (r *JSONMissingEnglishRecorder) Record(ctx context.Context, language, key, nativeGloss string) error {
	language = strings.TrimSpace(language)
	key = strings.TrimSpace(key)
	if r == nil || r.path == "" || language == "" || key == "" {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	entries, err := r.read()
	if err != nil {
		return err
	}
	for i := range entries {
		if entries[i].Language == language && entries[i].Key == key {
			if entries[i].NativeGloss == "" && strings.TrimSpace(nativeGloss) != "" {
				entries[i].NativeGloss = strings.TrimSpace(nativeGloss)
				return r.write(entries)
			}
			return nil
		}
	}
	entries = append(entries, MissingEnglishDefinition{
		Language: language, Key: key, NativeGloss: strings.TrimSpace(nativeGloss),
		FirstSeenAt: r.now().UTC().Format(time.RFC3339),
	})
	return r.write(entries)
}

func (r *JSONMissingEnglishRecorder) read() ([]MissingEnglishDefinition, error) {
	data, err := os.ReadFile(r.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var entries []MissingEnglishDefinition
	if len(data) > 0 {
		if err := json.Unmarshal(data, &entries); err != nil {
			return nil, err
		}
	}
	return entries, nil
}

func (r *JSONMissingEnglishRecorder) write(entries []MissingEnglishDefinition) error {
	slices.SortFunc(entries, func(a, b MissingEnglishDefinition) int {
		if byLanguage := strings.Compare(a.Language, b.Language); byLanguage != 0 {
			return byLanguage
		}
		return strings.Compare(a.Key, b.Key)
	})
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	dir := filepath.Dir(r.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(r.path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, r.path)
}
