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
	// Aliases must not leak into the brand listing.
	if len(All()) != len(Order) {
		t.Errorf("All() = %d personas, want %d", len(All()), len(Order))
	}
}

func TestAliasRows(t *testing.T) {
	rows := AliasRows()
	if len(rows) != 2 || rows[0].Display != "kimi-k3.1-pro" || rows[1].Display != "kimi-k3" {
		t.Fatalf("rows %+v", rows)
	}
	for _, r := range rows {
		if r.Persona == nil || r.ID != "kali-gamma" {
			t.Errorf("row %s points at %v", r.Display, r.Persona)
		}
	}
}

func TestRequestedID(t *testing.T) {
	gamma, _ := Resolve("kali-gamma")
	cases := []struct{ in, want string }{
		{"kali-gamma", "kali-gamma"},
		{"kimi-k3.1-pro", "kimi-k3.1-pro"},
		{"kimi-k3", "kimi-k3"},
		{"kimi-k2.8", "kali-gamma"}, // not this persona's alias
		{"nonsense", "kali-gamma"},
	}
	for _, c := range cases {
		if got := gamma.RequestedID(c.in); got != c.want {
			t.Errorf("RequestedID(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
