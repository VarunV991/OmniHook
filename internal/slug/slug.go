// Package slug validates endpoint slugs. Slugs appear in URL paths
// (/hook/:slug, /api/endpoints/:slug), so they must be router-safe: a slug
// can never contain a slash, dot-segment, or characters that escape its scope.
package slug

// Valid reports whether s matches ^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$.
func Valid(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		ok := c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_'
		if !ok {
			return false
		}
	}
	// First char must be alphanumeric (no leading -/_).
	c := s[0]
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9'
}
