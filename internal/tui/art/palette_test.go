package art

import "testing"

func TestHexConversions(t *testing.T) {
	if got := HexTo256("#000000"); got != 16 {
		t.Errorf("HexTo256 black = %d, want 16", got)
	}
	if got := HexTo256("#ffffff"); got != 231 {
		t.Errorf("HexTo256 white = %d, want 231", got)
	}
	if got := HexTo256("#ff0000"); got != 196 {
		t.Errorf("HexTo256 red = %d, want 196", got)
	}
	if Luminance("#000000") != 0 {
		t.Error("black luminance must be exactly 0")
	}
	if Luminance("#ffffff") != 1 {
		t.Error("white luminance must be exactly 1")
	}
}
