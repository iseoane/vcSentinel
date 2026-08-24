package art

import (
	"strings"
	"testing"
)

// testFrame is a tiny deterministic frame for engine tests: its first two
// pixel rows differ, so text row 0 packs both solid and split cells.
var testFrame = Frame{
	Grid: []string{
		"AABB",
		"AACC",
		"CCDD",
		"CCDD",
	},
	Pal: Palette{
		'.': PurplePanel,
		'A': "#000000",
		'B': "#ffffff",
		'C': "#ff0000",
		'D': "#3e63c8",
	},
}

func TestEnginePacksHalfBlocks(t *testing.T) {
	img := Render(testFrame, false, false)
	if img.W != 4 {
		t.Fatalf("width %d, want 4", img.W)
	}
	if len(img.Rows) != 2 {
		t.Fatalf("rows %d, want 2", len(img.Rows))
	}
	first := img.Rows[0]
	if first[0].Ch != '█' || first[0].Fg != "#000000" {
		t.Errorf("solid cell (0,0) = %+v, want solid black", first[0])
	}
	// Column 2 packs white (pixel row 0) over red (pixel row 1): split.
	if first[2].Ch != '▀' || first[2].Fg != "#ffffff" || first[2].Bg != "#ff0000" {
		t.Errorf("split cell (2,0) = %+v, want white-over-red ▀", first[2])
	}
}

func TestEngineOddHeightRepeatsLastRow(t *testing.T) {
	f := Frame{Grid: []string{"AB", "AB", "AB"}, Pal: testFrame.Pal}
	img := Render(f, false, false)
	if len(img.Rows) != 2 {
		t.Fatalf("rows %d, want 2 (odd grid repeats its last row)", len(img.Rows))
	}
}

func TestEngineUnknownKeyPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("rendering an unknown palette key must panic")
		}
	}()
	Render(Frame{Grid: []string{"XZ", "XZ"}, Pal: testFrame.Pal}, false, false)
}

func TestEngineHalftoneOnlyTouchesBackground(t *testing.T) {
	plain := Render(testFrame, false, false)
	dotted := Render(testFrame, false, true)
	for y := range plain.Rows {
		for x := range plain.Rows[y] {
			p, d := plain.Rows[y][x], dotted.Rows[y][x]
			if p.Fg == PurplePanel && d.Fg != HalftoneDot && d.Fg != PurplePanel {
				t.Errorf("halftone repainted non-background cell (%d,%d)", x, y)
			}
		}
	}
}

func TestEngineMonoDropsAllEscapes(t *testing.T) {
	img := Render(testFrame, true, false)
	if s := img.ANSI(); strings.Contains(s, "\x1b[") {
		t.Fatalf("mono render emitted ANSI escapes: %q", s[:30])
	}
	plain := img.Plain()
	if strings.TrimSpace(plain) == "" {
		t.Fatal("mono plain structure is empty")
	}
}

func TestEngineANSIColorizes(t *testing.T) {
	s := Render(testFrame, false, false).ANSI()
	if !strings.Contains(s, "\x1b[38;5;") {
		t.Fatal("color render emitted no foreground escapes")
	}
	if strings.Contains(s, "\x1b[0m\x1b[0m") {
		t.Error("duplicated resets found")
	}
}

func TestEnginePlainStructure(t *testing.T) {
	plain := Render(testFrame, false, false).Plain()
	lines := strings.Split(strings.TrimRight(plain, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("plain rows %d, want 2", len(lines))
	}
	if !strings.Contains(plain, "▀") {
		t.Error("plain view lost the split cell marker")
	}
}
