package git_pages

import (
	"crypto/sha256"
	"fmt"
	"testing"
)

func TestPatchFillsGitHash(t *testing.T) {
	const data = "hello"
	gitHash := fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("blob %d\x00%s", len(data), data))))

	manifest := NewManifest()
	applyPatch(t, manifest, tarMember{name: "docs/page.html", data: data})
	if got := manifest.Contents["docs/page.html"].GetGitHash(); got != gitHash {
		t.Fatalf("expected git hash %s, got %q", gitHash, got)
	}
	if got := AddFile(NewManifest(), "page.html", []byte(data)).GetGitHash(); got != gitHash {
		t.Fatalf("git hash must match the one of a full upload: %s, got %q", gitHash, got)
	}

	applyPatch(t, manifest, tarMember{name: "copy.html", link: BlobReferencePrefix + gitHash})
	if entry := manifest.Contents["copy.html"]; string(entry.GetData()) != data {
		t.Errorf("blob reference must resolve to the patched file: %v", entry)
	}
}
