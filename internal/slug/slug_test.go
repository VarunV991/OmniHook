package slug

import "testing"

func TestValid(t *testing.T) {
	good := []string{"a", "proj-1_2", "X9", "0123456789012345678901234567890123456789012345678901234567890123"}
	for _, s := range good {
		if !Valid(s) {
			t.Fatalf("valid %q rejected", s)
		}
	}
	bad := []string{"", "a/b", "..", "../x", "-lead", "_lead", "has space", "semi;colon", "01234567890123456789012345678901234567890123456789012345678901234", "a?b", "%2e"}
	for _, s := range bad {
		if Valid(s) {
			t.Fatalf("invalid %q accepted", s)
		}
	}
}
