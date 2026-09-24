package git_pages

import (
	"archive/tar"
	"bytes"
	"context"
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
