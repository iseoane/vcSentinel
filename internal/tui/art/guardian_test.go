package art

import "testing"

// gridWidth is the mandatory width of every splash grid row; compact rows
// share their own constant width.
const (
	gridWidth   = 44
	compactWide = 18
)

func TestSplashGridInvariants(t *testing.T) {
	if len(guardianGrid) == 0 || len(guardianGrid)%2 != 0 {
		t.Fatalf("splash grid must have an even row count, got %d", len(guardianGrid))
	}
	for i, row := range guardianGrid {
		if len(row) != gridWidth {
			t.Errorf("splash row %d has width %d, want %d: %q", i, len(row), gridWidth, row)
		}
		for _, r := range row {
			if _, ok := guardianPalette[r]; !ok {
				t.Errorf("splash row %d uses unknown key %q", i, string(r))
			}
		}
	}
}

func TestCompactGridInvariants(t *testing.T) {
	if len(compactGrid) == 0 || len(compactGrid)%2 != 0 {
		t.Fatalf("compact grid must have an even row count, got %d", len(compactGrid))
	}
	for i, row := range compactGrid {
		if len(row) != compactWide {
			t.Errorf("compact row %d has width %d, want %d: %q", i, len(row), compactWide, row)
		}
	}
}

func TestStateRecolorsOnlyReactiveKeys(t *testing.T) {
	base := Render(Splash(StateIdle), false, false)
	for _, s := range []State{StateRunning, StateAwaiting, StateFailed, StateOffline, StateDone} {
		st := Render(Splash(s), false, false)
		changed := 0
		for y := range base.Rows {
			for x := range base.Rows[y] {
				if base.Rows[y][x] != st.Rows[y][x] {
					changed++
					if !inReactiveZone(x, y) {
						t.Errorf("state %d repainted fixed pixel (%d,%d)", int(s), x, y)
					}
				}
			}
		}
		if changed == 0 {
			t.Errorf("state %d changed no cells; reactive keys are not painted", int(s))
		}
	}
}

// inReactiveZone reports whether a packed cell covers any reactive grid key.
// Eyes live in visor text rows; the core in the palm text rows.
func inReactiveZone(x, y int) bool {
	if y >= 3 && y <= 4 && x >= 10 && x <= 16 {
		return true // visor
	}
	return y >= 14 && y <= 19 && x >= 26 && x <= 39 // palm core region
}

func TestGoldenSplashPlain(t *testing.T) {
	golden(t, "splash_plain", Render(Splash(StateIdle), false, true).Plain())
}

func TestGoldenCompactPlain(t *testing.T) {
	golden(t, "compact_plain", Render(Compact(StateRunning), false, false).Plain())
}
