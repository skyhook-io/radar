package investigation

import (
	"crypto/rand"
	"regexp"
	"strings"
)

// An evidence ref is ev_<scope>_<nonce>: the scope correlates a ref with one
// turn, the nonce with one tool result. Both are lower-case base32 so a ref
// survives every transport untouched. The ref is model-visible and conveys no
// authority; only the product that issued it can say whether it stands.
var (
	scopeRe = regexp.MustCompile(`^[a-z2-7]{26,128}$`)
	refRe   = regexp.MustCompile(`^ev_[a-z2-7]{26,128}_[a-z2-7]{26,128}$`)
	// The ref reaches the model inside a marker wrapper, so a model that
	// copies the whole marker instead of its payload is quoting Radar
	// correctly. Unwrapping is transcription, not interpretation.
	// Anchored, and one marker only: a field holding two markers, or a marker
	// inside prose, is the agent meaning something this cannot read, and
	// picking the first would be choosing an interpretation rather than
	// removing a wrapper.
	wrappedRefRe = regexp.MustCompile(`^\[\[radar:evidence-ref=(ev_[a-z2-7]{26,128}_[a-z2-7]{26,128})\]\]$`)
)

const (
	refMarkerPrefix = "[[radar:evidence-ref="
	refMarkerSuffix = "]]\n"
)

// ValidScope reports whether scope has the turn-scope grammar.
func ValidScope(scope string) bool { return scopeRe.MatchString(scope) }

// ValidRef reports whether ref has the evidence-ref grammar.
func ValidRef(ref string) bool { return refRe.MatchString(ref) }

// NewScope mints a fresh turn scope.
func NewScope() string { return strings.ToLower(rand.Text()) }

// NewRef mints a fresh ref under scope. Uniqueness within the scope is the
// issuer's to check; the nonce is random, not sequential, so a ref cannot be
// guessed from its neighbours.
func NewRef(scope string) string { return "ev_" + scope + "_" + strings.ToLower(rand.Text()) }

// RefMarker is the line the transport prepends to a tool result so the model
// can cite it: the marker, then a newline, then the result untouched.
func RefMarker(ref string) string { return refMarkerPrefix + ref + refMarkerSuffix }

// SplitRefMarker removes a leading ref marker from a tool result before the
// result is capped or persisted, returning the clean text and the ref. A
// malformed marker, or one that is not at the very start, is ordinary result
// text. The split authenticates nothing: a well-formed marker at the start of
// a payload from any MCP server comes out as a ref, and only the issuer's
// check against what it recorded makes it provenance.
func SplitRefMarker(result string) (clean, ref string) {
	if !strings.HasPrefix(result, refMarkerPrefix) {
		return result, ""
	}
	remainder := result[len(refMarkerPrefix):]
	end := strings.Index(remainder, refMarkerSuffix)
	if end < 0 {
		return result, ""
	}
	candidate := remainder[:end]
	if !refRe.MatchString(candidate) {
		return result, ""
	}
	return remainder[end+len(refMarkerSuffix):], candidate
}

// unwrapRef returns the ref inside a marker wrapper the model pasted whole, or
// the value unchanged when the field is not exactly one wrapper.
func unwrapRef(ref string) string {
	ref = strings.TrimSpace(ref)
	if m := wrappedRefRe.FindStringSubmatch(ref); m != nil {
		return m[1]
	}
	return ref
}
