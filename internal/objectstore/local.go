package objectstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

const metadataDir = ".objectstore/meta"

type LocalOption func(*LocalStore)

// WithPublicBaseURL makes URL return public URLs under base instead of local
// filesystem paths. The base should point at a handler that serves this store's
// root after doing its own authorization checks.
func WithPublicBaseURL(base string) LocalOption {
	return func(s *LocalStore) {
		s.publicBaseURL = base
	}
}

// LocalStore stores objects below one local filesystem root.
type LocalStore struct {
	rootPath      string
	publicBaseURL string
	root          *os.Root
}

// NewLocalStore opens a local object store rooted at rootPath. The directory is
// created when missing. Object operations are resolved relative to an os.Root so
// valid keys cannot traverse outside the configured root through ".." or symlinks.
func NewLocalStore(rootPath string, opts ...LocalOption) (*LocalStore, error) {
	if rootPath == "" {
		return nil, fmt.Errorf("objectstore: local root is required")
	}
	abs, err := filepath.Abs(rootPath)
	if err != nil {
		return nil, fmt.Errorf("objectstore: resolve local root: %w", err)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, fmt.Errorf("objectstore: create local root: %w", err)
	}
	root, err := os.OpenRoot(abs)
	if err != nil {
		return nil, fmt.Errorf("objectstore: open local root: %w", err)
	}
	store := &LocalStore{rootPath: abs, root: root}
	for _, opt := range opts {
		opt(store)
	}
	return store, nil
}

func (s *LocalStore) Close() error {
	if s == nil || s.root == nil {
		return nil
	}
	return s.root.Close()
}

func (s *LocalStore) Put(ctx context.Context, key string, r io.Reader, contentType string) (ObjectRef, error) {
	localKey, err := localName(key)
	if err != nil {
		return ObjectRef{}, err
	}
	if err := ctx.Err(); err != nil {
		return ObjectRef{}, err
	}
	if contentType == "" {
		contentType = defaultContentType
	}
	dir := filepath.Dir(localKey)
	if dir != "." {
		if err := s.root.MkdirAll(dir, 0o755); err != nil {
			return ObjectRef{}, fmt.Errorf("objectstore: create parent dirs: %w", err)
		}
	}
	f, err := s.root.OpenFile(localKey, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return ObjectRef{}, fmt.Errorf("objectstore: create object: %w", err)
	}
	size, copyErr := copyWithContext(ctx, f, r)
	closeErr := f.Close()
	if copyErr != nil {
		_ = s.root.Remove(localKey)
		return ObjectRef{}, fmt.Errorf("objectstore: write object: %w", copyErr)
	}
	if closeErr != nil {
		_ = s.root.Remove(localKey)
		return ObjectRef{}, fmt.Errorf("objectstore: close object: %w", closeErr)
	}
	info := ObjectInfo{
		Key:         key,
		ContentType: contentType,
		Size:        size,
		UpdatedAt:   time.Now().UTC(),
	}
	if err := s.writeMetadata(info); err != nil {
		_ = s.root.Remove(localKey)
		return ObjectRef{}, err
	}
	return ObjectRef{Key: key, ContentType: contentType, Size: size}, nil
}

func (s *LocalStore) Info(ctx context.Context, key string) (ObjectInfo, error) {
	localKey, err := localName(key)
	if err != nil {
		return ObjectInfo{}, err
	}
	if err := ctx.Err(); err != nil {
		return ObjectInfo{}, err
	}
	f, err := s.root.Open(localKey)
	if errors.Is(err, fs.ErrNotExist) {
		return ObjectInfo{}, ErrNotFound
	}
	if err != nil {
		return ObjectInfo{}, fmt.Errorf("objectstore: open object: %w", err)
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return ObjectInfo{}, fmt.Errorf("objectstore: stat object: %w", err)
	}
	return s.objectInfo(key, stat)
}

func (s *LocalStore) Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
	localKey, err := localName(key)
	if err != nil {
		return nil, ObjectInfo{}, err
	}
	if err := ctx.Err(); err != nil {
		return nil, ObjectInfo{}, err
	}
	f, err := s.root.Open(localKey)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ObjectInfo{}, ErrNotFound
	}
	if err != nil {
		return nil, ObjectInfo{}, fmt.Errorf("objectstore: open object: %w", err)
	}
	stat, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, ObjectInfo{}, fmt.Errorf("objectstore: stat object: %w", err)
	}
	info, err := s.objectInfo(key, stat)
	if err != nil {
		_ = f.Close()
		return nil, ObjectInfo{}, err
	}
	return f, info, nil
}

func (s *LocalStore) objectInfo(key string, stat fs.FileInfo) (ObjectInfo, error) {
	info, err := s.readMetadata(key)
	if errors.Is(err, fs.ErrNotExist) {
		info = ObjectInfo{
			Key:         key,
			ContentType: contentTypeForKey(key),
			Size:        stat.Size(),
			UpdatedAt:   stat.ModTime().UTC(),
		}
	} else if err != nil {
		return ObjectInfo{}, err
	}
	info.Key = key
	info.Size = stat.Size()
	if info.ContentType == "" {
		info.ContentType = contentTypeForKey(key)
	}
	if info.UpdatedAt.IsZero() {
		info.UpdatedAt = stat.ModTime().UTC()
	}
	return info, nil
}

func (s *LocalStore) Delete(ctx context.Context, key string) error {
	localKey, err := localName(key)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.root.Remove(localKey); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("objectstore: delete object: %w", err)
	}
	if err := s.root.Remove(metadataName(key)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("objectstore: delete metadata: %w", err)
	}
	return nil
}

func (s *LocalStore) URL(ctx context.Context, key string, opts URLOptions) (string, error) {
	localKey, err := localName(key)
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if s.publicBaseURL != "" {
		u, err := url.JoinPath(s.publicBaseURL, key)
		if err != nil {
			return "", fmt.Errorf("objectstore: build public url: %w", err)
		}
		return u, nil
	}
	if opts.RequirePublic {
		return "", fmt.Errorf("%w: local public media URL is not configured", ErrUnsupported)
	}
	return filepath.Join(s.rootPath, localKey), nil
}

func localName(key string) (string, error) {
	if err := ValidateKey(key); err != nil {
		return "", err
	}
	local, err := filepath.Localize(key)
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrInvalidKey, key)
	}
	return local, nil
}

func contentTypeForKey(key string) string {
	if typ := mime.TypeByExtension(filepath.Ext(key)); typ != "" {
		return typ
	}
	return defaultContentType
}

func (s *LocalStore) writeMetadata(info ObjectInfo) error {
	if err := s.root.MkdirAll(metadataDir, 0o700); err != nil {
		return fmt.Errorf("objectstore: create metadata dir: %w", err)
	}
	raw, err := json.Marshal(localMetadata{
		ContentType: info.ContentType,
		Size:        info.Size,
		UpdatedAt:   info.UpdatedAt,
	})
	if err != nil {
		return fmt.Errorf("objectstore: encode metadata: %w", err)
	}
	if err := s.root.WriteFile(metadataName(info.Key), raw, 0o600); err != nil {
		return fmt.Errorf("objectstore: write metadata: %w", err)
	}
	return nil
}

func (s *LocalStore) readMetadata(key string) (ObjectInfo, error) {
	raw, err := s.root.ReadFile(metadataName(key))
	if err != nil {
		return ObjectInfo{}, err
	}
	var meta localMetadata
	if err := json.Unmarshal(raw, &meta); err != nil {
		return ObjectInfo{}, fmt.Errorf("objectstore: decode metadata: %w", err)
	}
	return ObjectInfo{
		Key:         key,
		ContentType: meta.ContentType,
		Size:        meta.Size,
		UpdatedAt:   meta.UpdatedAt,
	}, nil
}

type localMetadata struct {
	ContentType string    `json:"content_type"`
	Size        int64     `json:"size"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func metadataName(key string) string {
	sum := sha256.Sum256([]byte(key))
	return metadataDir + "/" + hex.EncodeToString(sum[:]) + ".json"
}

func copyWithContext(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
	buf := make([]byte, 32*1024)
	var written int64
	for {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		nr, er := src.Read(buf)
		if nr > 0 {
			nw, ew := dst.Write(buf[:nr])
			if nw < 0 || nr < nw {
				return written, io.ErrShortWrite
			}
			written += int64(nw)
			if ew != nil {
				return written, ew
			}
			if nr != nw {
				return written, io.ErrShortWrite
			}
		}
		if er != nil {
			if er == io.EOF {
				return written, nil
			}
			return written, er
		}
	}
}
