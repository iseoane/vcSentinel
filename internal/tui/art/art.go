// Package art renders the Sentinel guardian pixel art and the static mock
// frames of the Control Center dashboard. It is a pure rendering library: no
// I/O, no clock, no daemon access. The same pixel grid feeds three writers
// (ANSI 256-color, HTML true color, and a plain structural view) plus a
// luminance-based mono degradation, so the visual contract is testable
// headlessly and reviewable in any terminal or browser.
package art

import (
	"fmt"
	"strings"
)

// Frame is one renderable pixel image: a grid of palette keys plus the
// palette that gives them colors. Every grid row must have the same length;
// Render enforces this and panics on violation, because a malformed grid is a
// programming error in the art data, not a runtime condition.
type Frame struct {
	Grid []string
	Pal  Palette
}

// Palette maps grid characters to hex colors ("#rrggbb").
type Palette map[rune]string

// Cell is one terminal cell after half-block packing: Fg/Bg are hex colors
// (empty when unused) and Ch is '█', '▀', or a mono density rune.
type Cell struct {
	Fg string
	Bg string
	Ch rune
}

// Image is the packed terminal image: W cells wide, len(Rows) cells tall.
type Image struct {
	W    int
	Rows [][]Cell
}

// Render packs a frame into half-block cells: each terminal cell carries two
// vertical pixels ('▀' with fg=top, bg=bottom, or '█' when both pixels share
// one color). Odd-height grids repeat their last row so the image never
// loses its bottom edge. With mono, color is dropped and density runes
// encode luminance instead. With halftone, background cells get a
// deterministic dot pattern that mimics the comic-panel reference.
func Render(f Frame, mono, halftone bool) Image {
	grid := f.Grid
	if len(grid)%2 == 1 {
		grid = append(append([]string(nil), grid...), grid[len(grid)-1])
	}
	w := len(grid[0])
	img := Image{W: w, Rows: make([][]Cell, 0, len(grid)/2)}
	for y := 0; y < len(grid); y += 2 {
		row := make([]Cell, w)
		for x := 0; x < w; x++ {
			topKey, botKey := rune(grid[y][x]), rune(grid[y+1][x])
			top, bot := f.keyColor(topKey, x, y, halftone), f.keyColor(botKey, x, y+1, halftone)
			row[x] = packCell(top, bot, mono)
		}
		img.Rows = append(img.Rows, row)
	}
	return img
}

// keyColor resolves one pixel to its hex color, applying the halftone dot
// pattern to background cells. Unknown keys are a hard error: silent black
// pixels would corrupt the visual contract.
func (f Frame) keyColor(key rune, x, y int, halftone bool) string {
	hex, ok := f.Pal[key]
	if !ok {
		panic(fmt.Sprintf("art: grid key %q at (%d,%d) has no palette entry", key, x, y))
	}
	if halftone && key == '.' && (x+y)%2 == 0 && x%4 == 1 {
		return HalftoneDot
	}
	return hex
}

// packCell merges two vertically stacked pixels into one terminal cell.
func packCell(top, bot string, mono bool) Cell {
	if mono {
		return monoCell(top, bot)
	}
	if top == bot {
		return Cell{Fg: top, Ch: '█'}
	}
	return Cell{Fg: top, Bg: bot, Ch: '▀'}
}

// monoCell degrades two pixels to one density rune by combined luminance,
// so structure survives without any color support.
func monoCell(top, bot string) Cell {
	l := (Luminance(top) + Luminance(bot)) / 2
	switch {
	case l > 0.72:
		return Cell{Ch: '█'}
	case l > 0.45:
		return Cell{Ch: '▓'}
	case l > 0.22:
		return Cell{Ch: '▒'}
	case l > 0.08:
		return Cell{Ch: '░'}
	default:
		return Cell{Ch: ' '}
	}
}

// Plain renders the structural view: every cell becomes '█' or '▀' with no
// color information. This is what goldens pin and what reviewers see in a
// non-color context; it proves shape without depending on terminals.
func (im Image) Plain() string {
	var b strings.Builder
	for _, row := range im.Rows {
		for _, c := range row {
			if c.Fg == "" && c.Bg == "" {
				// Mono density cell: the rune already encodes structure.
				b.WriteRune(c.Ch)
				continue
			}
			if c.Ch == '▀' {
				b.WriteRune('▀')
				continue
			}
			if Luminance(c.Fg) > 0.08 || c.Bg != "" && Luminance(c.Bg) > 0.08 {
				b.WriteRune('█')
				continue
			}
			b.WriteRune(' ')
		}
		b.WriteRune('\n')
	}
	return b.String()
}

// ANSI renders the image with xterm-256 colors. Every colored cell emits a
// foreground and, when the cell packs two colors, a background escape; the
// trailing reset keeps frames composable.
func (im Image) ANSI() string {
	var b strings.Builder
	for _, row := range im.Rows {
		colored := false
		for _, c := range row {
			switch {
			case c.Fg != "" && c.Bg != "":
				colored = true
				fmt.Fprintf(&b, "\x1b[38;5;%d;48;5;%dm%c", HexTo256(c.Fg), HexTo256(c.Bg), c.Ch)
			case c.Fg != "":
				colored = true
				fmt.Fprintf(&b, "\x1b[38;5;%dm%c", HexTo256(c.Fg), c.Ch)
			default:
				b.WriteByte(' ')
			}
		}
		if colored {
			b.WriteString("\x1b[0m")
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// HTML renders the image as a self-contained fragment: a <pre> block of
// colored ▀/█ spans using the exact palette hexes, for browser review.
func (im Image) HTML() string {
	var b strings.Builder
	b.WriteString(`<pre style="background:#0d0a12;line-height:1;font-size:14px;padding:12px;">`)
	for _, row := range im.Rows {
		for _, c := range row {
			switch {
			case c.Fg != "" && c.Bg != "":
				fmt.Fprintf(&b, `<span style="color:%s;background:%s">%c</span>`, c.Fg, c.Bg, c.Ch)
			case c.Fg != "":
				fmt.Fprintf(&b, `<span style="color:%s">%c</span>`, c.Fg, c.Ch)
			default:
				b.WriteString(" ")
			}
		}
		b.WriteString("\n")
	}
	b.WriteString("</pre>")
	return b.String()
}
