// Command tuipreview prints the Slice-1 visual contract: the guardian pixel
// art in every state, the compact mascot, and the mock dashboards. It exists
// for human review and for capturing the HTML proof; it is not wired into
// the sentinel CLI.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/ISeoane-Quental/vas.sentinel/internal/tui/art"
)

func main() {
	htmlOut := flag.String("html", "", "write a self-contained HTML preview with true colors")
	mono := flag.Bool("mono", false, "render monochrome degradation instead of ANSI color")
	noHalftone := flag.Bool("no-halftone", false, "disable the background dot pattern")
	flag.Parse()

	states := []struct {
		Name string
		S    art.State
	}{
		{"idle", art.StateIdle},
		{"discovering", art.StateDiscovering},
		{"running", art.StateRunning},
		{"awaiting decision", art.StateAwaiting},
		{"failed", art.StateFailed},
		{"offline", art.StateOffline},
		{"done", art.StateDone},
	}

	var html strings.Builder
	html.WriteString("<html><body style='background:#141018;color:#ddd;font-family:monospace'>\n")

	for _, st := range states {
		splash := art.Render(art.Splash(st.S), *mono, !*noHalftone)
		compact := art.Render(art.Compact(st.S), *mono, false)
		if *htmlOut != "" {
			html.WriteString(fmt.Sprintf("<h2>splash — %s</h2>\n", st.Name))
			html.WriteString(splash.HTML())
			html.WriteString(fmt.Sprintf("<h3>compact — %s</h3>\n", st.Name))
			html.WriteString(compact.HTML())
		}
		fmt.Printf("── splash · %s %s\n%s", st.Name, strings.Repeat("─", 30), section(splash, *mono))
		fmt.Printf("── compact · %s\n%s\n", st.Name, section(compact, *mono))
	}

	for _, w := range []int{80, 100, 140} {
		dash := art.Dashboard(w)
		fmt.Printf("── dashboard · %d columns %s\n%s\n", w, strings.Repeat("─", 20), dash)
		if *htmlOut != "" {
			html.WriteString(fmt.Sprintf("<h2>dashboard — %d columns</h2>\n<pre style='padding:12px;'>%s</pre>\n", w, escapeHTML(dash)))
		}
	}

	if *htmlOut != "" {
		html.WriteString("</body></html>\n")
		if err := os.WriteFile(*htmlOut, []byte(html.String()), 0o600); err != nil {
			fmt.Fprintf(os.Stderr, "tuipreview: cannot write %s: %v\n", *htmlOut, err)
			os.Exit(1)
		}
		fmt.Printf("HTML preview written to %s\n", *htmlOut)
	}
}

// section renders an image for terminal output: ANSI when color is on, the
// structural plain view otherwise.
func section(img art.Image, mono bool) string {
	if mono {
		return img.Plain()
	}
	return img.ANSI()
}

func escapeHTML(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}
