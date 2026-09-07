package ops

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// EventDetail is the structured payload used by operation event writers.
type EventDetail map[string]any

// Event is one line of events.jsonl: the append-only record of VAS Sentinel
// operations in the repository.
type Event struct {
	At       time.Time `json:"at"`
	Cmd      string    `json:"cmd"`
	Exit     int       `json:"exit"`
	Shas     []string  `json:"shas,omitempty"`
	Detail   any       `json:"detail,omitempty"`
	Worktree string    `json:"worktree,omitempty"`
}

// UnmarshalJSON accepts both the historical string representation and the
// structured object representation. A legacy string containing a JSON object
// is normalized for readers; other legacy text remains unchanged.
func (e *Event) UnmarshalJSON(data []byte) error {
	type eventWire struct {
		At       time.Time       `json:"at"`
		Cmd      string          `json:"cmd"`
		Exit     int             `json:"exit"`
		Shas     []string        `json:"shas,omitempty"`
		Detail   json.RawMessage `json:"detail"`
		Worktree string          `json:"worktree,omitempty"`
	}
	var wire eventWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	*e = Event{At: wire.At, Cmd: wire.Cmd, Exit: wire.Exit, Shas: wire.Shas, Worktree: wire.Worktree}
	rawDetail := bytes.TrimSpace(wire.Detail)
	if len(rawDetail) == 0 || bytes.Equal(rawDetail, []byte("null")) {
		return nil
	}

	var detail any
	if err := json.Unmarshal(rawDetail, &detail); err != nil {
		return err
	}
	if legacy, ok := detail.(string); ok {
		var object map[string]any
		if err := json.Unmarshal([]byte(legacy), &object); err == nil && object != nil {
			detail = object
		}
	}
	e.Detail = detail
	return nil
}

var eventsRelPath = filepath.Join("vas-sentinel", "events.jsonl")

// Rotation thresholds: the log never grows without bound. When the file
// exceeds maxEventsBytes it is pruned to the last maxEventsLines lines.
const (
	maxEventsBytes = 256 * 1024
	maxEventsLines = 1000
)

// RecordEvent appends an event to the repository log (O_APPEND, no
// truncation). Creates the directory and the file if they do not exist. A
// failed write returns an error: the log is never silently discarded. After
// appending, if the log exceeds the size threshold it is rotated keeping the
// last maxEventsLines lines (never empties the whole history).
func RecordEvent(gitDir, cmd string, exit int, shas []string, detail EventDetail, worktree string) error {
	path := filepath.Join(gitDir, eventsRelPath)
	event := Event{
		At:       time.Now().UTC(),
		Cmd:      cmd,
		Exit:     exit,
		Shas:     shas,
		Worktree: worktree,
	}
	if detail != nil {
		event.Detail = detail
	}
	line, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Size() > 0 {
		lastByte := []byte{0}
		if _, err := file.ReadAt(lastByte, info.Size()-1); err != nil {
			return err
		}
		if lastByte[0] != '\n' {
			if _, err := file.Write([]byte{'\n'}); err != nil {
				return err
			}
		}
	}
	if _, err := file.Write(append(line, '\n')); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}

	info, err = os.Stat(path)
	if err != nil {
		return nil // without stats no rotation happens, but the event is already written
	}
	if info.Size() > maxEventsBytes {
		return RotateEvents(gitDir, maxEventsLines)
	}
	return nil
}

// visitRawLines visits each JSONL record with its original line terminator (if
// any). Unlike bufio.Scanner, bufio.Reader does not impose a 64 KiB token
// limit, and retaining the bytes here lets selective rewrites preserve every
// kept record verbatim.
func visitRawLines(reader io.Reader, visit func([]byte) error) error {
	buffered := bufio.NewReader(reader)
	for {
		line, err := buffered.ReadBytes('\n')
		if len(line) > 0 {
			if visitErr := visit(line); visitErr != nil {
				return visitErr
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

func copyRawLine(line []byte) []byte {
	return append([]byte(nil), line...)
}

// PRCreateDetail is the schema of the pr-create event detail (§13): the
// publication record. Without pr_url the record is fallback and unverifiable.
type PRCreateDetail struct {
	PrURL    string `json:"pr_url"`
	Fallback bool   `json:"fallback"`
	ChainPR  bool   `json:"chain_pr"`
}

// PurgeResult summarizes the purge of resolved PR records.
type PurgeResult struct {
	Purged int
	Kept   int
	// Warnings document what could not be verified (no gh/network, records
	// without URL): the purge is best-effort and never destroys on uncertainty.
	Warnings []string
}

// PurgeEventsOfResolvedPRs queries the state of the PRs of the pr-create
// events (gh pr view <n> --json state) and purges the records of MERGED/CLOSED
// PRs. Keeps OPEN/DRAFT, the fallback records (without URL) and anything that
// cannot be verified. fetchState is injectable for tests; nil uses the real gh.
func PurgeEventsOfResolvedPRs(gitDir string, fetchState func(number int) (string, error)) (PurgeResult, error) {
	res := PurgeResult{}
	path := filepath.Join(gitDir, eventsRelPath)
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return res, nil
		}
		return res, err
	}

	if fetchState == nil {
		fetchState = pullRequestStateWithGH
	}

	var kept [][]byte
	readErr := visitRawLines(file, func(line []byte) error {
		var ev Event
		if json.Unmarshal(line, &ev) != nil || ev.Cmd != "pr-create" {
			kept = append(kept, copyRawLine(line))
			return nil
		}
		purge, warning := resolvedPRRecord(ev.Detail, fetchState)
		if purge {
			res.Purged++
			return nil
		}
		res.Kept++
		if warning != "" {
			res.Warnings = append(res.Warnings, warning)
		}
		kept = append(kept, copyRawLine(line))
		return nil
	})
	closeErr := file.Close()
	if readErr != nil {
		return res, readErr
	}
	if closeErr != nil {
		return res, closeErr
	}

	if res.Purged == 0 {
		return res, nil
	}
	return res, writeRawTemporaryLog(path, kept)
}

// resolvedPRRecord decides whether the record of a PR is a resolved PR (purge),
// also returning a warning if it could not be verified (it is kept). A
// corrupt detail does NOT abort the purge: it is kept with a warning
// (best-effort, never destroyed by uncertainty).
func resolvedPRRecord(detail any, fetchState func(int) (string, error)) (bool, string) {
	var record PRCreateDetail
	if err := unmarshalDetail(detail, &record); err != nil {
		return false, "record with invalid detail: kept"
	}
	number, ok := pullRequestNumber(record.PrURL)
	if !ok {
		return false, "record without pr_url (fallback): unverifiable, kept"
	}
	state, err := fetchState(number)
	if err != nil {
		return false, "PR #" + strconv.Itoa(number) + " unverifiable (gh/network): kept"
	}
	switch state {
	case "MERGED", "CLOSED":
		return true, ""
	default:
		return false, ""
	}
}

func unmarshalDetail(detail any, target any) error {
	if legacy, ok := detail.(string); ok {
		return json.Unmarshal([]byte(legacy), target)
	}
	raw, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, target)
}

// pullRequestNumber extracts the number from a PR URL (/pull/<n>, tolerating
// suffixes like /pull/42/files).
func pullRequestNumber(url string) (int, bool) {
	idx := strings.LastIndex(url, "/pull/")
	if idx < 0 {
		return 0, false
	}
	rest := url[idx+len("/pull/"):]
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0, false
	}
	n, err := strconv.Atoi(rest[:end])
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// pullRequestStateWithGH queries gh pr view <n> --json state and returns the
// state. Any failure (gh missing, no network) is an error: the purge treats
// it as best-effort and keeps the record.
func pullRequestStateWithGH(number int) (string, error) {
	output, err := exec.Command("gh", "pr", "view", strconv.Itoa(number), "--json", "state").Output()
	if err != nil {
		return "", err
	}
	var raw struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(output, &raw); err != nil {
		return "", err
	}
	return raw.State, nil
}

// Writes the lines (without terminators) to a temporary file and atomically
// and safely replaces the log. It NEVER removes the original before the
// replacement is written. On Windows, os.Rename over an existing destination
// fails, so replacement is backup → rename → cleanup: on any failure, the
// original log remains preserved (as .bak or intact).
func writeTemporaryLog(path string, lines [][]byte) error {
	return writeTemporaryLogWithTerminators(path, lines, os.Rename, true)
}

// writeRawTemporaryLog rewrites only after selecting lines, preserving the
// retained bytes exactly, including CRLF and a missing final terminator.
func writeRawTemporaryLog(path string, lines [][]byte) error {
	return writeTemporaryLogWithTerminators(path, lines, os.Rename, false)
}

// writeTemporaryLogRenaming is the testable variant of writeTemporaryLog:
// the rename operation is injectable so the failure and restoration paths can
// be exercised without depending on the filesystem.
func writeTemporaryLogRenaming(path string, lines [][]byte, rename func(string, string) error) error {
	return writeTemporaryLogWithTerminators(path, lines, rename, true)
}

func writeTemporaryLogWithTerminators(path string, lines [][]byte, rename func(string, string) error, terminateWithNewline bool) error {
	temp, err := os.CreateTemp(filepath.Dir(path), "events-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	writer := bufio.NewWriter(temp)
	for _, line := range lines {
		data := line
		if terminateWithNewline {
			data = make([]byte, len(line)+1)
			copy(data, line)
			data[len(line)] = '\n'
		}
		if _, err := writer.Write(data); err != nil {
			temp.Close()
			return err
		}
	}
	if err := writer.Flush(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}

	backupPath := path + ".bak"
	// Remove a stale backup from an interrupted execution. On Windows,
	// os.Rename fails if the destination exists and would block the next
	// rotation or purge. A stale backup never has newer data than the log, so
	// removing it loses no information.
	if err := os.Remove(backupPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := rename(path, backupPath); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		// No prior log exists: the temporary file becomes the log.
		return rename(tempPath, path)
	}
	if err := rename(tempPath, path); err != nil {
		// Restore the original. If restoration also fails, the log remains safe
		// as .bak and the error reports where it can be recovered.
		if restoreErr := rename(backupPath, path); restoreErr != nil {
			return fmt.Errorf("%v (restore failed: %v; backup log at %s)", err, restoreErr, backupPath)
		}
		return err
	}
	// Best-effort backup cleanup: the log was written successfully, and a
	// failure here (antivirus lock or permissions) must not fail the operation.
	_ = os.Remove(backupPath)
	return nil
}

// PurgeEventsOf rewrites the log removing the lines that reference any of the
// given SHAs (events of commit audits that no longer exist). It keeps intact
// the lines of commits that are still alive: the cleanup is selective by SHA,
// never by age. Returns how many lines it removed. Atomic temp + rename
// write: on failure, the original log is kept.
func PurgeEventsOf(gitDir string, shas []string) (int, error) {
	if len(shas) == 0 {
		return 0, nil
	}
	path := filepath.Join(gitDir, eventsRelPath)
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}

	target := map[string]bool{}
	for _, sha := range shas {
		target[sha] = true
	}

	var kept [][]byte
	removed := 0
	readErr := visitRawLines(file, func(line []byte) error {
		var ev Event
		if json.Unmarshal(line, &ev) == nil && eventsTouchShas(ev.Shas, target) {
			removed++
			return nil
		}
		kept = append(kept, copyRawLine(line))
		return nil
	})
	closeErr := file.Close()
	if readErr != nil {
		return removed, readErr
	}
	if closeErr != nil {
		return removed, closeErr
	}

	if removed == 0 {
		return 0, nil
	}
	return removed, writeRawTemporaryLog(path, kept)
}

// eventsTouchShas reports whether the event's list of SHAs contains any of
// the target SHAs.
func eventsTouchShas(event []string, target map[string]bool) bool {
	for _, sha := range event {
		if target[sha] {
			return true
		}
	}
	return false
}

// RotateEvents prunes the log keeping the last n valid lines (the most
// recent ones). Discards corrupt lines (invalid JSON): only events that
// parse count for the pruning and are kept. It does so with atomic temp +
// rename write: if anything fails, the original log is kept intact.
// An n <= 0 empties the file.
func RotateEvents(gitDir string, n int) error {
	path := filepath.Join(gitDir, eventsRelPath)
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}

	var lines [][]byte
	readErr := visitRawLines(file, func(line []byte) error {
		var ev Event
		if err := json.Unmarshal(line, &ev); err != nil {
			return nil // corrupt line: discarded during rotation
		}
		lines = append(lines, copyRawLine(line))
		return nil
	})
	closeErr := file.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}

	if n > 0 && len(lines) > n {
		lines = lines[len(lines)-n:]
	} else if n <= 0 {
		lines = nil
	}
	return writeRawTemporaryLog(path, lines)
}

// RecentEvents returns the n most recent events of the log (the newest
// first). If the log does not exist it returns an empty list without error.
func RecentEvents(gitDir string, n int) ([]Event, error) {
	path := filepath.Join(gitDir, eventsRelPath)
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []Event{}, nil
		}
		return nil, err
	}
	defer file.Close()

	var events []Event
	readErr := visitRawLines(file, func(line []byte) error {
		var ev Event
		if err := json.Unmarshal(line, &ev); err != nil {
			return nil // corrupt line: ignored, the rest of the log stays valid
		}
		events = append(events, ev)
		return nil
	})
	if readErr != nil {
		return nil, readErr
	}

	// Last n, newest first.
	if n <= 0 || len(events) <= n {
		for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
			events[i], events[j] = events[j], events[i]
		}
		return events, nil
	}
	last := events[len(events)-n:]
	for i, j := 0, n-1; i < j; i, j = i+1, j-1 {
		last[i], last[j] = last[j], last[i]
	}
	return last, nil
}
