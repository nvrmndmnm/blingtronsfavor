package tui

import (
	goruntime "runtime"
	"strings"
	"testing"
)

func TestClassifyStatus(t *testing.T) {
	cases := []struct {
		word string
		want statusClass
	}{
		{"up to date", statusGood},
		{"updated 1.0.0 → 1.0.1", statusGood},
		{"installed 2.0.0", statusGood},
		{"Match", statusGood},
		{"update 3.4.5", statusWarn},
		{"MultiMatch", statusWarn},
		{"NoToc", statusWarn},
		{"failed", statusBad},
		{"Missing", statusBad},
		{"Mismatch", statusBad},
		{"manual", statusNeutral},
		{"-", statusNeutral},
		{"Unknown", statusNeutral},
		{"", statusNeutral},
	}
	for _, tc := range cases {
		if got := classifyStatus(tc.word); got != tc.want {
			t.Errorf("classifyStatus(%q) = %v, want %v", tc.word, got, tc.want)
		}
	}
}

func TestGlyphFallbackMapping(t *testing.T) {
	// The status glyphs degrade exactly as the UX contract fixes.
	pairs := [][2]string{
		{unicodeGlyphs.Check, asciiGlyphs.Check},
		{unicodeGlyphs.Warn, asciiGlyphs.Warn},
		{unicodeGlyphs.Cross, asciiGlyphs.Cross},
		{unicodeGlyphs.Pending, asciiGlyphs.Pending},
	}
	want := [][2]string{{"✓", "+"}, {"⚠", "!"}, {"✗", "x"}, {"…", "*"}}
	for i, p := range pairs {
		if p != want[i] {
			t.Errorf("glyph %d degrades %q → %q, want %q → %q", i, p[0], p[1], want[i][0], want[i][1])
		}
	}
}

func TestASCIIGlyphsAreASCII(t *testing.T) {
	for _, s := range []string{
		asciiGlyphs.Check, asciiGlyphs.Warn, asciiGlyphs.Cross, asciiGlyphs.Pending,
		asciiGlyphs.Arrow, asciiGlyphs.Ellipsis, asciiGlyphs.Dash, asciiGlyphs.MidDot,
		asciiGlyphs.Cursor, asciiGlyphs.Up, asciiGlyphs.Down,
	} {
		for _, r := range s {
			if r > 127 {
				t.Errorf("ascii glyph %q contains non-ASCII rune %q", s, r)
			}
		}
	}
}

func TestLocaleUTF8(t *testing.T) {
	clearLocale := func() {
		t.Helper()
		t.Setenv("LC_ALL", "")
		t.Setenv("LC_CTYPE", "")
		t.Setenv("LANG", "")
	}

	clearLocale()
	t.Setenv("LANG", "C.UTF-8")
	if !localeUTF8() {
		t.Error("LANG=C.UTF-8: want UTF-8")
	}
	clearLocale()
	t.Setenv("LANG", "en_US.ISO-8859-1")
	if localeUTF8() {
		t.Error("LANG=en_US.ISO-8859-1: want non-UTF-8")
	}
	clearLocale()
	t.Setenv("LANG", "C")
	if localeUTF8() {
		t.Error("LANG=C: want non-UTF-8")
	}
	clearLocale()
	t.Setenv("LANG", "C.UTF-8")
	t.Setenv("LC_ALL", "C")
	if localeUTF8() {
		t.Error("LC_ALL=C must take precedence over LANG=C.UTF-8")
	}
	clearLocale()
	if got := localeUTF8(); got != (goruntime.GOOS == "windows") {
		t.Errorf("no locale set: UTF-8 = %v, want %v", got, goruntime.GOOS == "windows")
	}
}

func TestDetectCaps(t *testing.T) {
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_CTYPE", "")
	t.Setenv("LANG", "C.UTF-8")
	t.Setenv("NO_COLOR", "")

	c := detectCaps(false)
	if !c.unicode || !c.color {
		t.Errorf("UTF-8 terminal: caps = %+v, want unicode and color", c)
	}

	c = detectCaps(true) // the --ascii override wins over the locale
	if c.unicode {
		t.Errorf("--ascii: caps = %+v, want unicode disabled", c)
	}

	t.Setenv("NO_COLOR", "1")
	c = detectCaps(false)
	if c.color {
		t.Errorf("NO_COLOR=1: caps = %+v, want color disabled", c)
	}

	t.Setenv("LANG", "C")
	c = detectCaps(false)
	if c.unicode {
		t.Errorf("LANG=C: caps = %+v, want unicode disabled", c)
	}
}

func TestThemeStatusGlyphAndStyle(t *testing.T) {
	th := newTheme(caps{unicode: true, color: true})
	if th.statusGlyph(statusGood) != "✓" || th.statusGlyph(statusBad) != "✗" {
		t.Errorf("unicode glyphs wrong: good=%q bad=%q", th.statusGlyph(statusGood), th.statusGlyph(statusBad))
	}
	if th.g.Ellipsis != "…" || th.g.Arrow != "→" {
		t.Errorf("unicode punctuation wrong: ellipsis=%q arrow=%q", th.g.Ellipsis, th.g.Arrow)
	}

	th = newTheme(caps{unicode: false, color: false})
	if th.statusGlyph(statusGood) != "+" || th.statusGlyph(statusWarn) != "!" ||
		th.statusGlyph(statusBad) != "x" || th.statusGlyph(statusNeutral) != "*" {
		t.Errorf("ascii glyphs wrong: %q %q %q %q",
			th.statusGlyph(statusGood), th.statusGlyph(statusWarn),
			th.statusGlyph(statusBad), th.statusGlyph(statusNeutral))
	}

	// Every colored status also carries its word: the status styles must
	// render plain text untouched apart from the (invisible) escapes.
	rendered := th.statusStyle(statusGood).Render("up to date")
	if !strings.Contains(rendered, "up to date") {
		t.Errorf("status style lost its word: %q", rendered)
	}
}
