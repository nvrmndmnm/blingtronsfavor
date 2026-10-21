package toc

import (
	"os"
	"testing"
)

func TestRewriteInterface(t *testing.T) {
	tests := []struct {
		name    string
		content string
		iface   int
		want    string
	}{
		{
			name:    "rewrite single interface line",
			content: "## Interface: 120100\n## Title: Demo\nDemo.lua\n",
			iface:   16001,
			want:    "## Interface: 16001\n## Title: Demo\nDemo.lua\n",
		},
		{
			name:    "collapse multi-interface line",
			content: "## Interface: 11509 16001 120100\nDemo.lua\n",
			iface:   16001,
			want:    "## Interface: 16001\nDemo.lua\n",
		},
		{
			name:    "append when missing",
			content: "## Title: Demo\nDemo.lua\n",
			iface:   16001,
			want:    "## Title: Demo\nDemo.lua\n## Interface: 16001\n",
		},
		{
			name:    "collapse duplicate directives",
			content: "## Interface: 120100\nDemo.lua\n## Interface: 11509\n",
			iface:   16001,
			want:    "## Interface: 16001\nDemo.lua\n",
		},
		{
			name:    "no trailing newline",
			content: "## Title: Demo",
			iface:   16001,
			want:    "## Title: Demo\n## Interface: 16001\n",
		},
		{
			name:    "preserve BOM and CRLF",
			content: "\xEF\xBB\xBF## Interface: 120100\r\nDemo.lua\r\n",
			iface:   16001,
			want:    "\xEF\xBB\xBF## Interface: 16001\r\nDemo.lua\r\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeToc(t, "Demo.toc", tt.content)
			if err := RewriteInterface(path, tt.iface); err != nil {
				t.Fatalf("RewriteInterface: %v", err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read result: %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("RewriteInterface() = %q, want %q", got, tt.want)
			}
		})
	}
}
