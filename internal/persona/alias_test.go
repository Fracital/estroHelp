package persona

import "testing"

func TestAliasesResolve(t *testing.T) {
	cases := []struct{ alias, wantID string }{
		{"kimi-k3.1-pro", "kali-gamma"},
		{"kimi-k3", "kali-gamma"},
		{"kimi-k2.8", "kali-beta"},
		{"Kimi-K3.1-Pro", "kali-gamma"}, // case-insensitive like the brand names
	}
	for _, c := range cases {
		p, ok := Resolve(c.alias)
		if !ok {
			t.Fatalf("Resolve(%q) not found", c.alias)
		}
		if p.ID != c.wantID {
			t.Errorf("Resolve(%q) = %s, want %s", c.alias, p.ID, c.wantID)
		}
	}
	// Aliases must not leak into the public catalog.
	for _, p := range All() {
		if p.ID != "kali-gamma" && p.ID != "kali-beta" && p.ID != "kali-epsilon" {
			t.Errorf("unexpected catalog entry %s", p.ID)
		}
	}
}
