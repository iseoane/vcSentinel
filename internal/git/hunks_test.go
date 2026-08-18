package git

import "testing"

func TestTouchedRangesPureAddition(t *testing.T) {
	before := "a\nb\nc\nd\n"
	after := "a\nb\nX\nc\nd\n"

	got, err := TouchedRanges(before, after)
	if err != nil {
		t.Fatalf("TouchedRanges: unexpected error: %v", err)
	}
	want := []LineRange{{Start: 3, End: 3}}
	assertRanges(t, got, want)
}

func TestTouchedRangesPureAdditionMultipleLines(t *testing.T) {
	before := "a\nb\nc\nd\n"
	after := "a\nb\nX\nY\nc\nd\n"

	got, err := TouchedRanges(before, after)
	if err != nil {
		t.Fatalf("TouchedRanges: unexpected error: %v", err)
	}
	want := []LineRange{{Start: 3, End: 4}}
	assertRanges(t, got, want)
}

func TestTouchedRangesPureDeletion(t *testing.T) {
	before := "a\nb\nc\nd\n"
	after := "a\nc\nd\n"

	got, err := TouchedRanges(before, after)
	if err != nil {
		t.Fatalf("TouchedRanges: unexpected error: %v", err)
	}
	// "b" was removed between "a" and "c": git reports a 0-count insertion
	// point at line 1 of the after content, and TouchedRanges still reports
	// a single-line range there so the deletion's location is not lost.
	want := []LineRange{{Start: 1, End: 1}}
	assertRanges(t, got, want)
}

func TestTouchedRangesPureModification(t *testing.T) {
	before := "a\nb\nc\nd\n"
	after := "a\nB\nc\nd\n"

	got, err := TouchedRanges(before, after)
	if err != nil {
		t.Fatalf("TouchedRanges: unexpected error: %v", err)
	}
	want := []LineRange{{Start: 2, End: 2}}
	assertRanges(t, got, want)
}

func TestTouchedRangesMultipleHunks(t *testing.T) {
	before := "a\nb\nc\nd\n"
	after := "A\nb\nc\nD\n"

	got, err := TouchedRanges(before, after)
	if err != nil {
		t.Fatalf("TouchedRanges: unexpected error: %v", err)
	}
	want := []LineRange{{Start: 1, End: 1}, {Start: 4, End: 4}}
	assertRanges(t, got, want)
}

func assertRanges(t *testing.T, got, want []LineRange) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("TouchedRanges: got %d ranges %v, want %d ranges %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("TouchedRanges: range %d = %+v, want %+v (full got: %v)", i, got[i], want[i], got)
		}
	}
}
