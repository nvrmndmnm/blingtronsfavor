package client

import "testing"

func TestFlavorForKey(t *testing.T) {
	tests := []struct {
		key  string
		want Flavor
	}{
		{"forever", FlavorForever},
		{"camelot", FlavorForever},
		{"Camelot", FlavorForever},
		{"FOREVER", FlavorForever},
		{"WOW-Camelot", FlavorForever},
		{"wow_camelot", FlavorForever},
		{"wow forever", FlavorForever},
		{"mainline", FlavorMainline},
		{"retail", FlavorMainline},
		{"classic", FlavorClassic},
		{"Classic Era", FlavorClassic},
		{"vanilla", FlavorClassic},
		{"mists", FlavorMists},
		{"mop", FlavorMists},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			got, ok := FlavorForKey(tt.key)
			if !ok || got != tt.want {
				t.Errorf("FlavorForKey(%q) = (%v, %v), want (%v, true)", tt.key, got, ok, tt.want)
			}
		})
	}
	for _, key := range []string{"", "wrath", "cameloot", "forevers"} {
		if got, ok := FlavorForKey(key); ok {
			t.Errorf("FlavorForKey(%q) = (%v, true), want no match", key, got)
		}
	}
}
