package art

import (
	"fmt"
	"math"
	"strconv"
)

// Palette color roles. The guardian keeps the reference mood — purple panel,
// crimson helmet, blue armor, luminous core — as an original design.
const (
	PurplePanel = "#8f5cc7"
	HalftoneDot = "#6d3f9e"
	PanelBlack  = "#0d0a12"
	Outline     = "#1a1424"
	ArmorBlue   = "#3e63c8"
	ArmorShade  = "#2a4490"
	ArmorLight  = "#7d9df0"
	HelmetMagma = "#c8325f"
	HelmetShade = "#8e1f44"
	PadRed      = "#c22f45"
	FacePlate   = "#c9b9a8"
	FaceShade   = "#8f7f70"
	CoreBright  = "#ffd75e"
	CoreGlow    = "#e8912d"
	Highlight   = "#f2ecff"
)

// State drives the two reactive zones of the guardian: the eyes report
// system health/attention, the palm core reports workflow activity.
type State int

const (
	StateIdle State = iota
	StateDiscovering
	StateRunning
	StateAwaiting
	StateFailed
	StateOffline
	StateDone
)

// stateColors assigns the reactive palette per state: eyes (E) and the palm
// core pair (C bright center, c glow ring).
type stateColors struct {
	Eyes string
	Core string
	Ring string
	Glow string
}

var stateTable = map[State]stateColors{
	StateIdle:        {Eyes: "#7d9df0", Core: "#5a4a8a", Ring: "#43376b", Glow: "#3a2f5c"},
	StateDiscovering: {Eyes: "#7d9df0", Core: "#7d9df0", Ring: "#3e63c8", Glow: "#2a4490"},
	StateRunning:     {Eyes: "#ffd75e", Core: "#ffd75e", Ring: "#e8912d", Glow: "#b56a1d"},
	StateAwaiting:    {Eyes: "#ffb347", Core: "#ffb347", Ring: "#c97b1d", Glow: "#8f5512"},
	StateFailed:      {Eyes: "#ff5d5d", Core: "#ff5d5d", Ring: "#a82f3c", Glow: "#6e1d28"},
	StateOffline:     {Eyes: "#5a5a72", Core: "#3a3a4c", Ring: "#2a2a38", Glow: "#1e1e28"},
	StateDone:        {Eyes: "#9fe8a8", Core: "#9fe8a8", Ring: "#4d9d5f", Glow: "#2f6e3d"},
}

// reactiveKeys are the only grid characters a state may recolor. Everything
// else stays fixed so states can never silently repaint the artwork.
var reactiveKeys = map[rune]bool{'E': true, 'C': true, 'c': true, 'y': true}

// WithState returns a copy of the frame whose reactive keys carry the
// state's colors. The input frame is never mutated.
func WithState(f Frame, s State) Frame {
	colors, ok := stateTable[s]
	if !ok {
		panic(fmt.Sprintf("art: unknown state %d", int(s)))
	}
	pal := make(Palette, len(f.Pal))
	for k, v := range f.Pal {
		pal[k] = v
	}
	if reactiveKeys['E'] {
		pal['E'] = colors.Eyes
	}
	pal['C'] = colors.Core
	pal['c'] = colors.Ring
	pal['y'] = colors.Glow
	return Frame{Grid: f.Grid, Pal: pal}
}

// HexTo256 approximates a hex color with the xterm-256 cube/gray ramp.
func HexTo256(hex string) int {
	r, g, bl := parseHex(hex)
	gray := (r + g + bl) / 3
	if abs(r-gray) < 12 && abs(g-gray) < 12 && abs(bl-gray) < 12 {
		if gray < 8 {
			return 16
		}
		if gray > 248 {
			return 231
		}
		return 232 + (gray-8)/10
	}
	return 16 + 36*(r/51) + 6*(g/51) + (bl / 51)
}

// Luminance returns perceived brightness in [0,1] for a hex color. The
// integer-weight form makes the endpoints exact: black is 0 and white is 1.
func Luminance(hex string) float64 {
	if hex == "" {
		return 0
	}
	r, g, b := parseHex(hex)
	return float64(2126*r+7152*g+722*b) / (255 * 10000)
}

func parseHex(hex string) (int, int, int) {
	if len(hex) != 7 || hex[0] != '#' {
		panic(fmt.Sprintf("art: malformed hex color %q", hex))
	}
	v, err := strconv.ParseUint(hex[1:], 16, 32)
	if err != nil {
		panic(fmt.Sprintf("art: malformed hex color %q: %v", hex, err))
	}
	return int(v >> 16 & 0xff), int(v >> 8 & 0xff), int(v & 0xff)
}

func abs(n int) int { return int(math.Abs(float64(n))) }
