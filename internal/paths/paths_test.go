package paths

import (
	"os"
	"path/filepath"
	"testing"
)

// TestExpandPath: a leading ~/ (or a bare ~) resolves to the home directory
// so shell-quoted paths work; anything else passes through untouched.
func TestExpandPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no resolvable home directory")
	}
	cases := []struct{ in, want string }{
		{in: "~/games/wow", want: filepath.Join(home, "games/wow")},
		{in: "~", want: home},
		{in: "/abs/path", want: "/abs/path"},
		{in: "rel/path", want: "rel/path"},
		{in: "~user/x", want: "~user/x"},
		{in: "", want: ""},
	}
	for _, tc := range cases {
		got, err := ExpandPath(tc.in)
		if err != nil {
			t.Fatalf("ExpandPath(%q): %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("ExpandPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
