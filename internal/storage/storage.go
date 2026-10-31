// Package storage stores attachments on local disk or S3-compatible storage.
package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/kohanmathers/buggy/internal/config"
)

type Storage interface {
	Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	Open(ctx context.Context, key string) (io.ReadCloser, error)
	Delete(ctx context.Context, key string) error
	LocalPath() string
}

func New(cfg config.Storage) (Storage, error) {
	switch cfg.Driver {
	case "s3":
		cl, err := minio.New(cfg.S3.Endpoint, &minio.Options{
			Creds:  credentials.NewStaticV4(cfg.S3.AccessKey, cfg.S3.SecretKey, ""),
			Secure: cfg.S3.UseSSL,
			Region: cfg.S3.Region,
		})
		if err != nil {
			return nil, err
		}
		return &s3Store{cl: cl, bucket: cfg.S3.Bucket, prefix: strings.Trim(cfg.S3.Prefix, "/")}, nil
	default:
		if err := os.MkdirAll(cfg.Path, 0o755); err != nil {
			return nil, err
		}
		return &localStore{root: cfg.Path}, nil
	}
}

type localStore struct{ root string }

func (l *localStore) path(key string) (string, error) {
	clean := filepath.Clean("/" + key)
	if strings.Contains(key, "..") {
		return "", errors.New("invalid key")
	}
	return filepath.Join(l.root, clean), nil
}

func (l *localStore) Put(_ context.Context, key string, r io.Reader, _ int64, _ string) error {
	p, err := l.path(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp := p + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func (l *localStore) Open(_ context.Context, key string) (io.ReadCloser, error) {
	p, err := l.path(key)
	if err != nil {
		return nil, err
	}
	return os.Open(p)
}

func (l *localStore) Delete(_ context.Context, key string) error {
	p, err := l.path(key)
	if err != nil {
		return err
	}
	return os.Remove(p)
}

func (l *localStore) LocalPath() string { return l.root }

type s3Store struct {
	cl     *minio.Client
	bucket string
	prefix string
}

func (s *s3Store) key(k string) string {
	if s.prefix == "" {
		return k
	}
	return s.prefix + "/" + k
}

func (s *s3Store) Put(ctx context.Context, key string, r io.Reader, size int64, ct string) error {
	_, err := s.cl.PutObject(ctx, s.bucket, s.key(key), r, size, minio.PutObjectOptions{ContentType: ct})
	if err != nil {
		return fmt.Errorf("s3 put: %w", err)
	}
	return nil
}

func (s *s3Store) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	return s.cl.GetObject(ctx, s.bucket, s.key(key), minio.GetObjectOptions{})
}

func (s *s3Store) Delete(ctx context.Context, key string) error {
	return s.cl.RemoveObject(ctx, s.bucket, s.key(key), minio.RemoveObjectOptions{})
}

func (s *s3Store) LocalPath() string { return "" }
