// Command tuipreview prints the Control Center visual contract: the
// dashboard mockups in color, plain, and HTML forms. It exists for human
// review; it is not wired into the sentinel CLI.
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
	flag.Parse()

	var html strings.Builder
	html.WriteString("<html><body style='background:#141018;color:#ddd;font-family:monospace'>\n")

	for _, w := range []int{80, 100, 140} {
		colored := art.Dashboard(w)
		fmt.Printf("── dashboard · %d columns %s\n%s\n", w, strings.Repeat("─", 20), colored)
		if *htmlOut != "" {
			html.WriteString(fmt.Sprintf("<h2>dashboard — %d columns</h2>\n<pre style='padding:12px;'>%s</pre>\n", w, escapeANSI(colored)))
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

// escapeANSI converts xterm-256 foreground codes into HTML spans so the
// colored preview keeps its palette in the browser.
func escapeANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\x1b' {
			c := s[i]
			if c == '<' {
				b.WriteString("&lt;")
			} else if c == '>' {
				b.WriteString("&gt;")
			} else if c == '&' {
				b.WriteString("&amp;")
			} else {
				b.WriteByte(c)
			}
			continue
		}
		// Parse \x1b[38;5;Nm or \x1b[0m
		end := strings.IndexByte(s[i:], 'm')
		if end < 0 {
			b.WriteString(s[i:])
			break
		}
		seq := s[i+1 : i+end]
		i += end
		if seq == "0" {
			b.WriteString("</span>")
			continue
		}
		if n, ok := strings.CutPrefix(seq, "38;5;"); ok {
			fmt.Fprintf(&b, `<span style="color:#%06x">`, xtermHex(atoi(n)))
		}
	}
	return b.String()
}

func atoi(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		n = n*10 + int(s[i]-'0')
	}
	return n
}

// xtermHex approximates an xterm-256 index as an RGB value for HTML output.
func xtermHex(n int) int {
	if n < 16 {
		return 0
	}
	if n < 232 {
		n -= 16
		r, g, b := n/36, (n/6)%6, n%6
		return r*51<<16 | g*51<<8 | b*51
	}
	v := (n-232)*10 + 8
	return v<<16 | v<<8 | v
}
