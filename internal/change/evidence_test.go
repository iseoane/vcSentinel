package change

import (
	"reflect"
	"testing"
)

func TestNewCharacteristicsInputAssemblesDetectorInput(t *testing.T) {
	profile := ChangeProfile{
		Symbols: ChangeSymbols{ExportedTouched: 2, Complete: true},
	}
	paths := []string{"internal/auth/login.go"}
	diff := "diff --git a/internal/auth/login.go b/internal/auth/login.go\n" +
		"--- a/internal/auth/login.go\n+++ b/internal/auth/login.go\n" +
		"@@ -0,0 +1 @@\n+func Login() {}\n"
	gitattributes := "generated/** linguist-generated\n"

	input := NewCharacteristicsInput(profile, paths, diff, gitattributes)

	if input.Symbols != profile.Symbols {
		t.Fatalf("symbols = %+v, want %+v", input.Symbols, profile.Symbols)
	}
	if !reflect.DeepEqual(input.Rutas, paths) {
		t.Fatalf("paths = %q, want %q", input.Rutas, paths)
	}
	if !reflect.DeepEqual(input.LineasAnadidas[paths[0]], []string{"func Login() {}"}) {
		t.Fatalf("added lines = %q", input.LineasAnadidas[paths[0]])
	}
	if input.Gitattributes != gitattributes {
		t.Fatalf("gitattributes = %q, want %q", input.Gitattributes, gitattributes)
	}
	wantSensitivePatterns := []string{"**/auth/**", "**/*auth*.go", "**/security/**"}
	if !reflect.DeepEqual(input.PatronesSensibles, wantSensitivePatterns) {
		t.Fatalf("sensitive patterns = %q, want %q", input.PatronesSensibles, wantSensitivePatterns)
	}
}

func TestNewCharacteristicsInputParsesQuotedPaths(t *testing.T) {
	path := "space and é.go"
	diff := `diff --git "a/space and \303\251.go" "b/space and \303\251.go"
--- "a/space and \303\251.go"
+++ "b/space and \303\251.go"
@@ -0,0 +1 @@
+quoted path
`

	input := NewCharacteristicsInput(ChangeProfile{}, []string{path}, diff, "")
	if got := input.LineasAnadidas[path]; !reflect.DeepEqual(got, []string{"quoted path"}) {
		t.Fatalf("added lines for quoted path = %q, want %q", got, []string{"quoted path"})
	}
}

func TestNewCharacteristicsInputParsesPureAdditions(t *testing.T) {
	path := "new.go"
	diff := `diff --git a/new.go b/new.go
new file mode 100644
--- /dev/null
+++ b/new.go
@@ -0,0 +1,2 @@
+first
+second
`

	input := NewCharacteristicsInput(ChangeProfile{}, []string{path}, diff, "")
	if got := input.LineasAnadidas[path]; !reflect.DeepEqual(got, []string{"first", "second"}) {
		t.Fatalf("added lines for pure addition = %q, want %q", got, []string{"first", "second"})
	}
}

func TestNewCharacteristicsInputIgnoresDeletedFiles(t *testing.T) {
	path := "deleted.go"
	diff := `diff --git a/deleted.go b/deleted.go
deleted file mode 100644
--- a/deleted.go
+++ /dev/null
@@ -1 +0,0 @@
-func Deleted() {}
\ No newline at end of file
`

	input := NewCharacteristicsInput(ChangeProfile{}, []string{path}, diff, "")
	if len(input.LineasAnadidas) != 0 {
		t.Fatalf("deleted file contributed added lines: %v", input.LineasAnadidas)
	}
}

func TestNewCharacteristicsInputIgnoresRenamesWithoutHunks(t *testing.T) {
	diff := `diff --git a/old.go b/new.go
similarity index 100%
rename from old.go
rename to new.go
`

	input := NewCharacteristicsInput(ChangeProfile{}, []string{"new.go"}, diff, "")
	if len(input.LineasAnadidas) != 0 {
		t.Fatalf("rename without a hunk contributed added lines: %v", input.LineasAnadidas)
	}
}

func TestNewCharacteristicsInputIgnoresModeOnlyChanges(t *testing.T) {
	diff := `diff --git a/script.sh b/script.sh
old mode 100644
new mode 100755
`

	input := NewCharacteristicsInput(ChangeProfile{}, []string{"script.sh"}, diff, "")
	if len(input.LineasAnadidas) != 0 {
		t.Fatalf("mode-only change contributed added lines: %v", input.LineasAnadidas)
	}
}

func TestNewCharacteristicsInputIgnoresBinaryFiles(t *testing.T) {
	diff := `diff --git a/image.bin b/image.bin
new file mode 100644
index 0000000..1234567
Binary files /dev/null and b/image.bin differ
`

	input := NewCharacteristicsInput(ChangeProfile{}, []string{"image.bin"}, diff, "")
	if len(input.LineasAnadidas) != 0 {
		t.Fatalf("binary file contributed added lines: %v", input.LineasAnadidas)
	}
}

func TestNewCharacteristicsInputIgnoresNoNewlineMarker(t *testing.T) {
	path := "plain.txt"
	diff := `diff --git a/plain.txt b/plain.txt
--- a/plain.txt
+++ b/plain.txt
@@ -1 +1 @@
-old
+new
\ No newline at end of file
`

	input := NewCharacteristicsInput(ChangeProfile{}, []string{path}, diff, "")
	if got := input.LineasAnadidas[path]; !reflect.DeepEqual(got, []string{"new"}) {
		t.Fatalf("added lines with no-newline marker = %q, want %q", got, []string{"new"})
	}
}

func TestNewCharacteristicsInputKeepsHeaderSequencesInsideAddedLines(t *testing.T) {
	path := "markers.txt"
	diff := `diff --git a/markers.txt b/markers.txt
--- a/markers.txt
+++ b/markers.txt
@@ -0,0 +1,2 @@
+++ b/not-a-header
+@@ -not-a-hunk
`

	input := NewCharacteristicsInput(ChangeProfile{}, []string{path}, diff, "")
	want := []string{"++ b/not-a-header", "@@ -not-a-hunk"}
	if got := input.LineasAnadidas[path]; !reflect.DeepEqual(got, want) {
		t.Fatalf("added lines with header sequences = %q, want %q", got, want)
	}
}

func TestNewCharacteristicsInputParsesColorlessDiff(t *testing.T) {
	path := "plain.go"
	diff := "diff --git a/plain.go b/plain.go\n" +
		"--- a/plain.go\n+++ b/plain.go\n" +
		"@@ -0,0 +1 @@\n+plain line\n"

	input := NewCharacteristicsInput(ChangeProfile{}, []string{path}, diff, "")
	if got := input.LineasAnadidas[path]; !reflect.DeepEqual(got, []string{"plain line"}) {
		t.Fatalf("added lines from colorless diff = %q, want %q", got, []string{"plain line"})
	}
}
