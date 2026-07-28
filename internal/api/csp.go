package api

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"io/fs"
	"strings"
)

// baseCSPDirectives is the policy with script-src left open for the hashes
// computed from the shipped HTML.
//
// style-src needs 'unsafe-inline' because Svelte emits scoped <style> blocks;
// script-src deliberately does NOT, since script injection is the dangerous
// case and we can enumerate our inline scripts exactly.
const baseCSPDirectives = "default-src 'self'; " +
	"style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data:; " +
	"font-src 'self' data:; " +
	"connect-src 'self'; " +
	"object-src 'none'; " +
	"base-uri 'none'; " +
	"form-action 'self'; " +
	"frame-ancestors 'none'"

// buildCSP returns the Content-Security-Policy for the shipped frontend.
//
// SvelteKit's static build bootstraps the app from an inline <script> whose
// body names the generated chunk files, so its hash changes on every build.
// Hardcoding a hash would break the SPA at the next rebuild, and allowing
// 'unsafe-inline' would defeat the point of having a script-src at all — so the
// hashes are derived from the actual asset at startup.
//
// If index.html cannot be read or contains no inline script, the policy falls
// back to plain 'self', which is safe (it can only be too strict, never too
// permissive).
func buildCSP(webFS fs.FS) string {
	hashes := inlineScriptHashes(webFS, "index.html")

	scriptSrc := "script-src 'self'"
	for _, h := range hashes {
		scriptSrc += " '" + h + "'"
	}
	return scriptSrc + "; " + baseCSPDirectives
}

// inlineScriptHashes returns the base64 SHA-256 of every inline <script> body
// in the named file, formatted as CSP hash-source values.
func inlineScriptHashes(webFS fs.FS, name string) []string {
	if webFS == nil {
		return nil
	}
	f, err := webFS.Open(name)
	if err != nil {
		return nil
	}
	defer f.Close()

	// The bootstrap document is small; cap the read so a malformed asset
	// cannot balloon startup memory.
	body, err := io.ReadAll(io.LimitReader(f, 4<<20))
	if err != nil {
		return nil
	}

	var out []string
	seen := make(map[string]bool)
	for _, content := range extractInlineScripts(string(body)) {
		sum := sha256.Sum256([]byte(content))
		h := "sha256-" + base64.StdEncoding.EncodeToString(sum[:])
		if !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}
	return out
}

// extractInlineScripts returns the exact body text of each <script> element that
// has no src attribute. The browser hashes precisely these bytes, so the
// content must be taken verbatim — no trimming, no normalization.
func extractInlineScripts(html string) []string {
	var out []string
	lower := strings.ToLower(html)

	pos := 0
	for {
		start := strings.Index(lower[pos:], "<script")
		if start < 0 {
			return out
		}
		start += pos

		// End of the opening tag.
		tagEnd := strings.IndexByte(lower[start:], '>')
		if tagEnd < 0 {
			return out
		}
		tagEnd += start

		openTag := lower[start : tagEnd+1]

		closeIdx := strings.Index(lower[tagEnd:], "</script")
		if closeIdx < 0 {
			return out
		}
		closeIdx += tagEnd

		// Scripts loaded from a URL are covered by 'self'; only inline bodies
		// need a hash.
		if !strings.Contains(openTag, " src=") {
			if body := html[tagEnd+1 : closeIdx]; strings.TrimSpace(body) != "" {
				out = append(out, body)
			}
		}
		pos = closeIdx + 1
	}
}

// describeCSP renders the policy for a startup log line.
func describeCSP(policy string) string {
	if i := strings.Index(policy, ";"); i > 0 {
		return fmt.Sprintf("%s (+%d more directives)", policy[:i], strings.Count(policy, ";"))
	}
	return policy
}
