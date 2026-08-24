package tui

import (
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// RenderPlain produces the View() string with the lipgloss color profile
// pinned to termenv.Ascii, then restores the previously detected profile.
//
// Why this exists: lipgloss detects color support lazily from the process
// environment (TERM, CLICOLOR_FORCE, NO_COLOR), so a headless View() capture
// could silently change bytes when the surrounding environment changes. The
// golden views must be byte-stable regardless of that environment, so every
// golden capture renders through this entry instead of trusting detection.
//
// Production interactive rendering is unaffected: tea.Program renders through
// View() with the default detected profile, exactly as before. Only explicit
// string-production callers (tests, goldens, tooling that needs plain text)
// opt into the pin. The save/restore keeps the mutation local even though
// nothing else in this process renders concurrently during tests.
//
// Mechanism note: lipgloss v1.1.0 exposes the package-level SetColorProfile
// on the default renderer (renderer.go), and termenv.Style.Styled short-
// circuits to the raw string under the Ascii profile, so bold/faint/foreground
// styles degrade to exactly the bytes an ASCII terminal would print.
func RenderPlain(m Model) string {
	previous := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.Ascii)
	defer lipgloss.SetColorProfile(previous)
	return m.View()
}
