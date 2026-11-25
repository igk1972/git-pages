package git_pages

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

func newStatOnlyS3Backend(t *testing.T, etag string, lastModified time.Time) *S3Backend {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead {
			w.WriteHeader(http.StatusNotImplemented)
			return
		}
		w.Header().Set("ETag", `"`+etag+`"`)
		w.Header().Set("Last-Modified", lastModified.UTC().Format(http.TimeFormat))
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	endpoint, _ := url.Parse(server.URL)
	client, err := minio.New(endpoint.Host, &minio.Options{
		Creds:  credentials.NewStaticV4("key", "secret", ""),
		Region: "us-east-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	return &S3Backend{client: client, bucket: "pages"}
}

func TestManifestPreconditionTrustsMatchingETag(t *testing.T) {
	cached := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	backend := newStatOnlyS3Backend(t, "abc", cached.Add(time.Second))

	err := backend.checkManifestPrecondition(context.Background(), "example.org/.index",
		ModifyManifestOptions{IfUnmodifiedSince: cached, IfMatch: "abc"})
	if err != nil {
		t.Fatalf("matching ETag must pass despite a later Last-Modified: %v", err)
	}
}

func TestManifestPreconditionRejectsChangedManifest(t *testing.T) {
	cached := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	backend := newStatOnlyS3Backend(t, "def", cached.Add(time.Second))

	for _, opts := range []ModifyManifestOptions{
		{IfUnmodifiedSince: cached, IfMatch: "abc"},
		{IfUnmodifiedSince: cached},
		{IfMatch: "abc"},
	} {
		err := backend.checkManifestPrecondition(context.Background(), "example.org/.index", opts)
		if !errors.Is(err, ErrPreconditionFailed) {
			t.Errorf("%+v: expected a precondition failure, got %v", opts, err)
		}
	}
}
