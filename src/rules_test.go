package git_pages

import (
	"archive/tar"
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"testing"
)

type tarMember struct {
	name     string
	data     string
	whiteout bool
}

func applyPatch(t *testing.T, manifest *Manifest, members ...tarMember) {
	t.Helper()
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	for _, member := range members {
		header := &tar.Header{Name: member.name, Mode: 0644}
		if member.whiteout {
			header.Typeflag = tar.TypeChar
		} else {
			header.Typeflag = tar.TypeReg
			header.Size = int64(len(member.data))
		}
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte(member.data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := ApplyTarPatch(manifest, &buffer, CreateParents); err != nil {
		t.Fatal(err)
	}
	if err := ProcessRedirectsFile(context.Background(), manifest); err != nil {
		t.Fatal(err)
	}
	if err := ProcessHeadersFile(context.Background(), manifest); err != nil {
		t.Fatal(err)
	}
}

func newRulesTestManifest() *Manifest {
	config = &Config{Limits: LimitsConfig{
		AllowedCustomHeaders: []string{"Cache-Control", "X-Robots-Tag"},
		AllowBasicAuth:       true,
	}}
	return &Manifest{Contents: map[string]*Entry{
		"": NewManifestEntry(Type_Directory, nil),
	}}
}

func headerRulePaths(manifest *Manifest) (paths []string) {
	for _, rule := range manifest.GetHeaders() {
		paths = append(paths, rule.GetPath())
	}
	return
}

func redirectRuleSources(manifest *Manifest) (sources []string) {
	for _, rule := range manifest.GetRedirects() {
		sources = append(sources, rule.GetFrom())
	}
	return
}

func problemPaths(manifest *Manifest) (paths []string) {
	for _, problem := range manifest.GetProblems() {
		paths = append(paths, problem.GetPath())
	}
	return
}

func TestPatchReplacesHeaderRules(t *testing.T) {
	manifest := newRulesTestManifest()
	applyPatch(t, manifest,
		tarMember{name: "_headers", data: "/old/*\n  X-Robots-Tag: noindex\n" +
			"/private/*\n  Basic-Auth: user:pass\n" +
			"/bad\n  X-Forbidden: 1\n"},
		tarMember{name: "index.html", data: "hi"})
	if got := headerRulePaths(manifest); !slices.Equal(got, []string{"/old/*", "/private/*"}) {
		t.Fatalf("initial rules: %v", got)
	}
	if len(manifest.GetBasicAuth()) != 1 || len(manifest.GetProblems()) != 1 {
		t.Fatalf("initial basic auth %v, problems %v",
			manifest.GetBasicAuth(), manifest.GetProblems())
	}

	applyPatch(t, manifest, tarMember{name: "other.html", data: "unrelated"})
	if got := headerRulePaths(manifest); !slices.Equal(got, []string{"/old/*", "/private/*"}) {
		t.Errorf("rules must survive a patch without _headers: %v", got)
	}

	applyPatch(t, manifest, tarMember{name: "_headers", data: "/new/*\n  X-Robots-Tag: noindex\n"})
	if got := headerRulePaths(manifest); !slices.Equal(got, []string{"/new/*"}) {
		t.Errorf("rules must be replaced: %v", got)
	}
	if len(manifest.GetBasicAuth()) != 0 {
		t.Errorf("basic auth rules must be replaced: %v", manifest.GetBasicAuth())
	}
	if len(manifest.GetProblems()) != 0 {
		t.Errorf("problems of the previous _headers must be dropped: %v", problemPaths(manifest))
	}
	if _, found := manifest.Contents["index.html"]; !found {
		t.Errorf("unrelated contents must be kept")
	}
}

func TestPatchWhiteoutClearsRules(t *testing.T) {
	manifest := newRulesTestManifest()
	applyPatch(t, manifest,
		tarMember{name: "_headers", data: "/*\n  X-Robots-Tag: noindex\n" +
			"/private/*\n  Basic-Auth: user:pass\n"},
		tarMember{name: "_redirects", data: "/from /to 301\n/bad /to 999\n"})
	if len(manifest.GetHeaders()) == 0 || len(manifest.GetRedirects()) == 0 {
		t.Fatalf("initial rules missing")
	}

	applyPatch(t, manifest, tarMember{name: "_headers", whiteout: true})
	if len(manifest.GetHeaders()) != 0 || len(manifest.GetBasicAuth()) != 0 {
		t.Errorf("whiteout must clear header rules: %v, %v",
			headerRulePaths(manifest), manifest.GetBasicAuth())
	}
	if got := redirectRuleSources(manifest); !slices.Equal(got, []string{"/from"}) {
		t.Errorf("whiteout of _headers must keep redirect rules: %v", got)
	}

	applyPatch(t, manifest, tarMember{name: "_redirects", whiteout: true})
	if len(manifest.GetRedirects()) != 0 {
		t.Errorf("whiteout must clear redirect rules: %v", redirectRuleSources(manifest))
	}
	if len(manifest.GetProblems()) != 0 {
		t.Errorf("whiteout must drop problems of the rule file: %v", problemPaths(manifest))
	}
}

func TestPatchWhiteoutOfNestedRuleFileKeepsRules(t *testing.T) {
	manifest := newRulesTestManifest()
	applyPatch(t, manifest,
		tarMember{name: "_headers", data: "/*\n  X-Robots-Tag: noindex\n"},
		tarMember{name: "docs/_headers", data: "not a rule file"})
	applyPatch(t, manifest, tarMember{name: "docs/_headers", whiteout: true})
	if len(manifest.GetHeaders()) != 1 {
		t.Errorf("rules must be kept: %v", headerRulePaths(manifest))
	}
}

func TestApplyAllMatchingHeaderRules(t *testing.T) {
	manifest := newRulesTestManifest()
	applyPatch(t, manifest, tarMember{name: "_headers", data: "" +
		"/pdf-map.json\n  Cache-Control: public, max-age=300\n" +
		"/*\n  X-Robots-Tag: noindex\n" +
		"/private/*\n  X-Robots-Tag: nosnippet\n  Basic-Auth: a:b\n" +
		"/*\n  Basic-Auth: c:d\n"})

	check := func(path string, name string, expected []string) {
		t.Helper()
		headers, err := ApplyHeaderRules(manifest, &url.URL{Path: path})
		if err != nil {
			t.Fatal(err)
		}
		if got := headers.Values(name); !slices.Equal(got, expected) {
			t.Errorf("%s: %s: expected %q, got %q", path, name, expected, got)
		}
	}
	check("pdf-map.json", "Cache-Control", []string{"public, max-age=300"})
	check("pdf-map.json", "X-Robots-Tag", []string{"noindex"})
	check("index.html", "Cache-Control", nil)
	check("private/doc.pdf", "X-Robots-Tag", []string{"noindex", "nosnippet"})

	rule := matchPathRules(manifest.GetBasicAuth(), &url.URL{Path: "private/doc.pdf"})
	if rule.GetPath() != "/private/*" {
		t.Errorf("basic auth must use the first matching rule: %s", rule.GetPath())
	}
}

func TestSiteRelativeRedirectRules(t *testing.T) {
	manifest := newRulesTestManifest()
	applyPatch(t, manifest, tarMember{name: "_redirects", data: "" +
		"/files/* https://cdn.example.net/files/:splat 301!\n" +
		"/old-section/* /new-section/:splat 301\n" +
		"/go/* /:splat 302\n" +
		"/app/* /index.html 200\n"})

	check := func(path, expectedTo string, expectedStatus int) {
		t.Helper()
		fromURL := &url.URL{Host: "example.test", Path: path, RawQuery: "q=1"}
		_, toURL, status := ApplyRedirectRules(manifest, fromURL, RedirectAny)
		if toURL == nil || toURL.String() != expectedTo || status != expectedStatus {
			t.Errorf("%s: expected %d %s, got %d %v", path, expectedStatus, expectedTo, status, toURL)
		}
	}
	check("/files/a/b.pdf", "https://cdn.example.net/files/a/b.pdf?q=1", 301)
	check("/old-section/page/", "/new-section/page/?q=1", 301)
	check("/go//evil.example/x", "/evil.example/x?q=1", 302)
	check("/app/deep/link", "/index.html?q=1", 200)
}

func TestCompareManifestDetectsRuleChanges(t *testing.T) {
	left := newRulesTestManifest()
	applyPatch(t, left, tarMember{name: "_headers", data: "/*\n  X-Robots-Tag: noindex\n"})
	right := newRulesTestManifest()
	applyPatch(t, right, tarMember{name: "_headers", data: "/*\n  X-Robots-Tag: noindex\n"})
	if !CompareManifest(left, right) {
		t.Errorf("manifests with the same rules must be equal")
	}
	applyPatch(t, right, tarMember{name: "_headers", data: "/*\n  X-Robots-Tag: nosnippet\n"})
	if CompareManifest(left, right) {
		t.Errorf("manifests with different header rules must differ")
	}
	applyPatch(t, right, tarMember{name: "_headers", data: "/*\n  X-Robots-Tag: noindex\n"})
	applyPatch(t, right, tarMember{name: "_redirects", data: "/a /b 301\n"})
	if CompareManifest(left, right) {
		t.Errorf("manifests with different redirect rules must differ")
	}
}

func TestProjectSiteRedirectsOmitPrefix(t *testing.T) {
	var err error
	if config, err = Configure(); err != nil {
		t.Fatal(err)
	}
	if backend, err = NewFSBackend(context.Background(), &FSConfig{Root: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	domainCache = &dummyDomainCache{}

	manifest := &Manifest{Contents: map[string]*Entry{"": NewManifestEntry(Type_Directory, nil)}}
	applyPatch(t, manifest,
		tarMember{name: "_redirects", data: "" +
			"/old/* /en/:splat 301\n" +
			"/files/* https://cdn.example.net/files/:splat 301!\n"},
		tarMember{name: "en/about/index.html", data: "about"})
	result := Update(context.Background(), "example.test/site", nil, manifest, ModifyManifestOptions{})
	if result.err != nil {
		t.Fatal(result.err)
	}

	check := func(target string, expectedStatus int, expectedLocation string) {
		t.Helper()
		recorder := httptest.NewRecorder()
		ServePages(recorder, httptest.NewRequest(http.MethodGet, "http://example.test"+target, nil))
		location := recorder.Header().Get("Location")
		if recorder.Code != expectedStatus || location != expectedLocation {
			t.Errorf("%s: expected %d %q, got %d %q",
				target, expectedStatus, expectedLocation, recorder.Code, location)
		}
	}
	check("/site?q=1", http.StatusFound, "/?q=1")
	check("/site/en/about?q=1", http.StatusFound, "/en/about/?q=1")
	check("/site/en/about/", http.StatusOK, "")
	check("/site/old/about/?q=1", http.StatusMovedPermanently, "/en/about/?q=1")
	check("/site/files/a.pdf", http.StatusMovedPermanently, "https://cdn.example.net/files/a.pdf")
}
