package guestiso

import (
	"context"
	"fmt"
	"io"
	"os"

	"cloud.google.com/go/storage"
	"google.golang.org/api/option"
)

// Publish uploads the ISO at isoPath to bucket/object using the supplied
// service-account key JSON (resolved by the caller from the entry's op:// ref).
// It never sets public ACLs. Returns the stored gs:// URI.
func Publish(ctx context.Context, bucket, object, isoPath string, saKeyJSON []byte) (string, error) {
	return publishWith(ctx, bucket, object, isoPath, option.WithCredentialsJSON(saKeyJSON))
}

// publishWith performs the upload with caller-supplied client options. Split out
// so tests can inject a fake endpoint + no-auth without credential conflicts.
func publishWith(ctx context.Context, bucket, object, isoPath string, opts ...option.ClientOption) (string, error) {
	f, err := os.Open(isoPath)
	if err != nil {
		return "", fmt.Errorf("open iso: %w", err)
	}
	defer f.Close()

	client, err := storage.NewClient(ctx, opts...)
	if err != nil {
		return "", fmt.Errorf("storage client: %w", err)
	}
	defer client.Close()

	w := client.Bucket(bucket).Object(object).NewWriter(ctx)
	if _, err := io.Copy(w, f); err != nil {
		_ = w.Close()
		return "", fmt.Errorf("upload: %w", err)
	}
	if err := w.Close(); err != nil {
		return "", fmt.Errorf("finalize upload: %w", err)
	}
	return fmt.Sprintf("gs://%s/%s", bucket, object), nil
}
