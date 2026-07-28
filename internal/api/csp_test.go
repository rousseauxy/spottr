package api

import (
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"testing"
	"testing/fstest"
)

// This is the exact shape SvelteKit's static adapter emits: an inline
// bootstrap whose body names the generated chunks, so its hash changes on
// every build. A hardcoded hash would blank the app at the next rebuild.
const svelteKitIndex = `<!doctype html>
<html><head><link href="/_app/immutable/entry/start.hO08VEeB.js" rel="modulepreload"></head>
<body>
<div style="display: contents">
			<script>
				{
					__sveltekit_1m9nym1 = { base: "" };
					const element = document.currentScript.parentElement;
					Promise.all([
						import("/_app/immutable/entry/start.hO08VEeB.js"),
						import("/_app/immutable/entry/app.D5WIm372.js")
					]).then(([kit, app]) => { kit.start(app, element); });
				}
			</script>
		</div>
</body></html>`

func TestBuildCSPHashesInlineBootstrap(t *testing.T) {
	fsys := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte(svelteKitIndex)}}

	policy := buildCSP(fsys)

	// Work out what the browser would compute for that script body.
	scripts := extractInlineScripts(svelteKitIndex)
	if len(scripts) != 1 {
		t.Fatalf("expected 1 inline script, got %d", len(scripts))
	}
	sum := sha256.Sum256([]byte(scripts[0]))
	want := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"

	if !strings.Contains(policy, want) {
		t.Errorf("policy missing bootstrap hash %s\npolicy: %s", want, policy)
	}
	if !strings.HasPrefix(policy, "script-src 'self' 'sha256-") {
		t.Errorf("unexpected script-src: %s", policy)
	}
	// The whole point is to avoid this.
	if strings.Contains(policy, "script-src 'self' 'unsafe-inline'") {
		t.Error("script-src fell back to 'unsafe-inline'")
	}
}

func TestExtractInlineScriptsIgnoresExternal(t *testing.T) {
	html := `<script src="/app.js"></script><script>inline()</script><script src="/b.js"></script>`
	got := extractInlineScripts(html)
	if len(got) != 1 || got[0] != "inline()" {
		t.Errorf("got %q, want exactly [inline()]", got)
	}
}

func TestExtractInlineScriptsHandlesAttributesAndEmpties(t *testing.T) {
	html := `<script type="module">boot()</script><script>   </script><script></script>`
	got := extractInlineScripts(html)
	if len(got) != 1 || got[0] != "boot()" {
		t.Errorf("got %q, want exactly [boot()]", got)
	}
}

// A missing or unreadable asset must not silently widen the policy.
func TestBuildCSPFallsBackClosed(t *testing.T) {
	policy := buildCSP(fstest.MapFS{})
	if !strings.HasPrefix(policy, "script-src 'self';") {
		t.Errorf("fallback policy should be plain self, got: %s", policy)
	}
	if strings.Contains(policy, "unsafe-inline") && strings.Contains(policy, "script-src 'self' 'unsafe-inline'") {
		t.Error("fallback policy allowed inline scripts")
	}
	if buildCSP(nil) == "" {
		t.Error("nil FS produced an empty policy")
	}
}

func TestBuildCSPKeepsHardeningDirectives(t *testing.T) {
	policy := buildCSP(fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte(svelteKitIndex)}})
	for _, want := range []string{
		"object-src 'none'",
		"base-uri 'none'",
		"frame-ancestors 'none'",
		"default-src 'self'",
	} {
		if !strings.Contains(policy, want) {
			t.Errorf("policy lost %q: %s", want, policy)
		}
	}
}
