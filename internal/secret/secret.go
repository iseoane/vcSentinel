// Package secret scans unified diffs for exposed credentials.
//
// Scan is deliberately standalone: it takes the paths under consideration
// and a git-style unified diff, and returns structured findings. It applies
// no file-class filtering of its own (the FU-11 defect class): documentation
// paths, generated code, and every other path are scanned identically.
// Findings never carry the matched value — only the file path, a stable
// shape name, and the 1-based new-file line number — so they are safe to
// log and persist.
package secret

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Incident is one exposed-credential finding. It intentionally has no field
// carrying the matched value.
type Incident struct {
	Path  string // file path, ToSlash-normalized
	Shape string // stable shape name, e.g. "aws_access_key_id"
	Line  int    // 1-based line in the new file; 0 when the hunk header is unparsable
}

// String renders the incident for logs; it only ever carries the path, line,
// and shape name — never the matched value.
func (i Incident) String() string {
	return fmt.Sprintf("%s:%d %s", i.Path, i.Line, i.Shape)
}

// Shape names reported in Incident.Shape.
const (
	shapeAWSAccessKeyID = "aws_access_key_id"
	shapeGitHubToken    = "github_token"
	shapeSlackToken     = "slack_token"
	shapeStripeLive     = "stripe_live"
	shapeGoogleAPIKey   = "google_api_key"
	shapeSendGridKey    = "sendgrid"
	shapePEMBlock       = "pem_block"
)

// patterns holds the high-confidence, structured credential shapes. Generic
// entropy scoring is deliberately excluded: it would flag prose and base64
// blobs that contain no credentials.
var patterns = []struct {
	shape string
	re    *regexp.Regexp
}{
	{shapeAWSAccessKeyID, regexp.MustCompile(`AKIA[0-9A-Z]{16}`)},
	{shapeGitHubToken, regexp.MustCompile(`gh[pousr]_[A-Za-z0-9_]{20,}`)},
	{shapeGitHubToken, regexp.MustCompile(`github_pat_[A-Za-z0-9_]{20,}`)},
	{shapeSlackToken, regexp.MustCompile(`xox[baprs]-[A-Za-z0-9-]{10,}`)},
	{shapeStripeLive, regexp.MustCompile(`[sr]k-live-[A-Za-z0-9]{10,}`)},
	{shapeGoogleAPIKey, regexp.MustCompile(`AIza[0-9A-Za-z_\-]{35}`)},
	{shapeSendGridKey, regexp.MustCompile(`SG\.[A-Za-z0-9_\-]{22,}\.[A-Za-z0-9_\-]{10,}`)},
	{shapePEMBlock, regexp.MustCompile(`-----BEGIN (?:RSA )?PRIVATE KEY-----`)},
	{shapePEMBlock, regexp.MustCompile(`-----BEGIN OPENSSH PRIVATE KEY-----`)},
}

// hunkHeaderRe extracts the new-file start line from "@@ -a,b +c,d @@".
var hunkHeaderRe = regexp.MustCompile(`^@@ -[0-9]+(?:,[0-9]+)? \+([0-9]+)(?:,[0-9]+)? @@`)

// fileKind records how the diff treated a path, so Scan can tell inspectable
// files apart from files it could not read.
type fileKind int

const (
	kindAbsent  fileKind = iota // not in the diff, binary, or otherwise unselectable
	kindHeader                  // selected via +++ but carrying no text hunks
	kindText                    // selected and carrying at least one text hunk
	kindDeleted                 // +++ /dev/null: pure deletion
)

// Scan inspects the added lines of a git-style unified diff for exposed
// credentials.
//
// paths lists the files under consideration: only those paths produce
// results, compared after filepath.ToSlash normalization on both sides.
// Scan applies no file-class filtering — documentation, generated code, and
// every other path are scanned alike.
//
// incidents holds one entry per (path, shape) pair, recorded at the first
// line where the shape appears and sorted by path, then shape. Line is the
// 1-based new-file line number from the hunk header, 0 when that header is
// unparsable. No incident ever contains the matched value.
//
// unknown lists input paths the scanner could not inspect: paths absent
// from the diff, binary files, or diff sections with no readable text
// hunks. Pure deletions (+++ /dev/null) are skipped silently and are not
// unknown. Empty results are data, not a "clean" verdict — the caller
// decides what absence means.
func Scan(paths []string, unifiedDiff string) ([]Incident, []string) {
	p := &diffParser{
		wanted:    make(map[string]bool, len(paths)),
		firstLine: make(map[string]map[string]int),
		fileState: make(map[string]fileKind),
	}
	for _, path := range paths {
		p.wanted[filepath.ToSlash(path)] = true
	}
	for _, line := range strings.Split(unifiedDiff, "\n") {
		p.parseLine(line)
	}

	incidents := make([]Incident, 0, len(p.firstLine))
	for path, shapes := range p.firstLine {
		for shape, line := range shapes {
			incidents = append(incidents, Incident{Path: path, Shape: shape, Line: line})
		}
	}
	sort.Slice(incidents, func(i, j int) bool {
		if incidents[i].Path != incidents[j].Path {
			return incidents[i].Path < incidents[j].Path
		}
		return incidents[i].Shape < incidents[j].Shape
	})

	var unknown []string
	seen := make(map[string]bool, len(paths))
	for _, path := range paths {
		path = filepath.ToSlash(path)
		if seen[path] {
			continue
		}
		seen[path] = true
		if kind := p.fileState[path]; kind == kindText || kind == kindDeleted {
			continue
		}
		unknown = append(unknown, path)
	}
	return incidents, unknown
}

// diffParser walks the diff line by line, tracking which file a hunk belongs
// to and the next new-file line number.
type diffParser struct {
	wanted    map[string]bool           // normalized input paths to scan
	firstLine map[string]map[string]int // path -> shape -> first matching line
	fileState map[string]fileKind       // path -> how the diff handled it

	curPath   string // file selected by the current +++ header, "" for deletions
	curWanted bool   // curPath is one of the input paths
	oldPath   string // path from the current --- header, for deletion attribution
	inHunk    bool   // inside @@ ... @@ body until the next diff --git
	lineNo    int    // next new-file line number inside the current hunk
	lineKnown bool   // lineNo is trustworthy: the hunk header parsed
}

// parseLine dispatches one diff line. Header prefixes are only honored
// outside hunk bodies: inside a hunk, a line like "+++ x" is added content
// ("++ x"), and a "diff --git" line always resets the file state.
func (p *diffParser) parseLine(line string) {
	line = strings.TrimSuffix(line, "\r")
	switch {
	case strings.HasPrefix(line, "diff --git "):
		p.resetFile()
	case p.inHunk:
		p.parseHunkLine(line)
	case strings.HasPrefix(line, "+++ "):
		p.selectFile(line[len("+++ "):])
	case strings.HasPrefix(line, "--- "):
		p.noteOldPath(line[len("--- "):])
	case strings.HasPrefix(line, "@@"):
		p.startHunk(line)
	default:
		// Header metadata (index, modes, renames, "Binary files ... differ",
		// GIT binary patch payload) carries no scannable added lines.
	}
}

// resetFile drops per-file state at each "diff --git" boundary.
func (p *diffParser) resetFile() {
	p.curPath, p.curWanted = "", false
	p.oldPath = ""
	p.inHunk, p.lineKnown = false, false
}

// selectFile handles the +++ header: it selects the file for subsequent
// hunks, or marks a pure deletion when the new side is /dev/null.
func (p *diffParser) selectFile(rest string) {
	rest = cutTimestamp(rest)
	p.inHunk, p.lineKnown = false, false
	if rest == "/dev/null" {
		p.curPath, p.curWanted = "", false
		if p.oldPath != "" {
			p.fileState[p.oldPath] = kindDeleted
		}
		return
	}
	path := filepath.ToSlash(strings.TrimPrefix(unquotePath(rest), "b/"))
	p.curPath = path
	p.curWanted = p.wanted[path]
	if _, ok := p.fileState[path]; !ok {
		p.fileState[path] = kindHeader
	}
}

// noteOldPath remembers the --- side path so a following "+++ /dev/null"
// can attribute the deletion.
func (p *diffParser) noteOldPath(rest string) {
	rest = cutTimestamp(rest)
	if rest == "/dev/null" {
		p.oldPath = ""
		return
	}
	p.oldPath = filepath.ToSlash(strings.TrimPrefix(unquotePath(rest), "a/"))
}

// startHunk begins a hunk body and records its new-file start line.
func (p *diffParser) startHunk(header string) {
	p.inHunk = true
	if p.curPath != "" {
		p.fileState[p.curPath] = kindText
	}
	p.lineKnown = false
	if m := hunkHeaderRe.FindStringSubmatch(header); m != nil {
		if start, err := strconv.Atoi(m[1]); err == nil {
			p.lineNo = start
			p.lineKnown = true
		}
	}
}

// parseHunkLine walks one line inside a hunk body: additions are scanned and
// counted, context lines only counted, deletions and "\ No newline" markers
// ignored.
func (p *diffParser) parseHunkLine(line string) {
	switch {
	case strings.HasPrefix(line, "@@"):
		p.startHunk(line)
	case strings.HasPrefix(line, "\\"):
		// "\ No newline at end of file": a marker, not content.
	case strings.HasPrefix(line, "+"):
		p.scanContent(line[1:])
		if p.lineKnown {
			p.lineNo++
		}
	case strings.HasPrefix(line, "-"):
		// Removed lines live in the old file; nothing to scan or count.
	default:
		// Context line. An empty string is a context line whose trailing
		// space was stripped somewhere along the way.
		if p.lineKnown {
			p.lineNo++
		}
	}
}

// scanContent checks one added line against every pattern, recording the
// first line per (path, shape). The line content itself is never stored.
func (p *diffParser) scanContent(content string) {
	if !p.curWanted {
		return
	}
	lineNo := 0
	if p.lineKnown {
		lineNo = p.lineNo
	}
	shapes := p.firstLine[p.curPath]
	for _, pattern := range patterns {
		if _, dup := shapes[pattern.shape]; dup {
			continue
		}
		if pattern.re.MatchString(content) {
			if shapes == nil {
				shapes = make(map[string]int)
				p.firstLine[p.curPath] = shapes
			}
			shapes[pattern.shape] = lineNo
		}
	}
}

// cutTimestamp trims a GNU-diff "path<TAB>TIMESTAMP" suffix, if present.
func cutTimestamp(s string) string {
	if i := strings.IndexByte(s, '\t'); i >= 0 {
		return s[:i]
	}
	return s
}

// unquotePath reverses git's C-style path quoting, e.g. "b/caf\303\251.md".
func unquotePath(s string) string {
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return s
	}
	body := s[1 : len(s)-1]
	var b strings.Builder
	b.Grow(len(body))
	for i := 0; i < len(body); i++ {
		ch := body[i]
		if ch != '\\' || i+1 >= len(body) {
			b.WriteByte(ch)
			continue
		}
		i++
		switch esc := body[i]; esc {
		case 'a':
			b.WriteByte('\a')
		case 'b':
			b.WriteByte('\b')
		case 'f':
			b.WriteByte('\f')
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case 'v':
			b.WriteByte('\v')
		case '\\', '"':
			b.WriteByte(esc)
		default:
			if esc >= '0' && esc <= '7' {
				value := int(esc - '0')
				for k := 0; k < 2 && i+1 < len(body) && body[i+1] >= '0' && body[i+1] <= '7'; k++ {
					i++
					value = value*8 + int(body[i]-'0')
				}
				b.WriteByte(byte(value))
			} else {
				b.WriteByte(esc)
			}
		}
	}
	return b.String()
}
