package objectstore

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/minio/minio-go/v7"
)

type MinIO struct {
	client *minio.Client
	bucket string
}

func (store *MinIO) OpenPDF(ctx context.Context, key string) (io.ReadSeekCloser, time.Time, error) {
	object, err := store.client.GetObject(ctx, store.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("open PDF object: %w", err)
	}
	info, err := object.Stat()
	if err != nil {
		object.Close()
		return nil, time.Time{}, fmt.Errorf("stat PDF object: %w", err)
	}
	return object, info.LastModified, nil
}

func NewMinIO(client *minio.Client, bucket string) *MinIO {
	return &MinIO{client: client, bucket: bucket}
}

func (store *MinIO) PutPDF(ctx context.Context, key string, body io.Reader, size int64) error {
	_, err := store.client.PutObject(ctx, store.bucket, key, body, size, minio.PutObjectOptions{
		ContentType: "application/pdf",
	})
	if err != nil {
		return fmt.Errorf("store PDF object: %w", err)
	}
	return nil
}

func (store *MinIO) Delete(ctx context.Context, key string) error {
	if err := store.client.RemoveObject(ctx, store.bucket, key, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("delete PDF object: %w", err)
	}
	return nil
}
