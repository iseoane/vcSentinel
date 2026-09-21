package consent

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	gitinternal "github.com/ISeoane-Quental/vcSentinel/internal/git"
)

const (
	stateVersion  = 1
	maxGrantBytes = 4096
	appDir        = "vas-sentinel"
	consentDir    = "consent"
)

// ExternalDiffState records only the local scope of the grant; it never
// contains source.
type ExternalDiffState struct {
	Version    int       `json:"version"`
	Granted    bool      `json:"granted"`
	User       string    `json:"user"`
	Repository string    `json:"repository"`
	GrantedAt  time.Time `json:"granted_at,omitempty"`
	Path       string    `json:"-"`
}

func stateFor(path string) (ExternalDiffState, error) {
	commonDir, err := gitinternal.GetGitCommonDir(path)
	if err != nil {
		return ExternalDiffState{}, err
	}
	commonDir, err = filepath.Abs(commonDir)
	if err != nil {
		return ExternalDiffState{}, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ExternalDiffState{}, err
	}
	identity := filepath.Clean(home)
	if runtime.GOOS == "windows" {
		identity = strings.ToLower(identity)
	}
	sum := sha256.Sum256([]byte(identity))
	user := hex.EncodeToString(sum[:8])
	file := filepath.Join(commonDir, appDir, consentDir, "external-diff-"+user+".json")
	return ExternalDiffState{Version: stateVersion, User: user, Repository: commonDir, Path: file}, nil
}

func openSubdirectory(parent *os.Root, name string, create, private bool) (*os.Root, bool, error) {
	info, err := parent.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		if !create {
			return nil, false, nil
		}
		if err := parent.Mkdir(name, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, false, err
		}
		info, err = parent.Lstat(name)
	}
	if err != nil {
		return nil, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, false, fmt.Errorf("insecure consent component: %s", name)
	}
	if private && info.Mode().Perm() != 0700 {
		return nil, false, fmt.Errorf("consent directory without private mode: %s", name)
	}
	root, err := parent.OpenRoot(name)
	if err != nil {
		return nil, false, err
	}
	openStat, err := root.Stat(".")
	if err != nil || !os.SameFile(info, openStat) {
		root.Close()
		return nil, false, fmt.Errorf("tampered consent component: %s", name)
	}
	return root, true, nil
}

func openConsentDir(state ExternalDiffState, create bool) (*os.Root, bool, error) {
	common, err := os.OpenRoot(state.Repository)
	if err != nil {
		return nil, false, err
	}
	defer common.Close()
	app, exists, err := openSubdirectory(common, appDir, create, false)
	if err != nil || !exists {
		return nil, exists, err
	}
	defer app.Close()
	return openSubdirectory(app, consentDir, create, true)
}

func grantName(state ExternalDiffState) string {
	return filepath.Base(state.Path)
}

func readGrant(root *os.Root, state ExternalDiffState) (ExternalDiffState, bool, error) {
	name := grantName(state)
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return state, false, nil
	}
	if err != nil {
		return ExternalDiffState{}, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return ExternalDiffState{}, false, fmt.Errorf("unsafe local grant file")
	}
	file, err := root.Open(name)
	if err != nil {
		return ExternalDiffState{}, false, err
	}
	defer file.Close()
	openStat, err := file.Stat()
	if err != nil || !os.SameFile(info, openStat) {
		return ExternalDiffState{}, false, fmt.Errorf("tampered local grant file")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxGrantBytes+1))
	if err != nil {
		return ExternalDiffState{}, false, err
	}
	if len(data) > maxGrantBytes {
		return ExternalDiffState{}, false, fmt.Errorf("local grant exceeds the allowed size")
	}
	var saved ExternalDiffState
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&saved); err != nil {
		return ExternalDiffState{}, false, fmt.Errorf("invalid local grant: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ExternalDiffState{}, false, fmt.Errorf("local grant contains additional data")
	}
	if saved.Version != state.Version || saved.User != state.User || filepath.Clean(saved.Repository) != state.Repository || !saved.Granted || saved.GrantedAt.IsZero() {
		return ExternalDiffState{}, false, fmt.Errorf("local grant does not match its scope")
	}
	canonical, err := json.MarshalIndent(saved, "", "  ")
	if err != nil || !bytes.Equal(data, append(canonical, '\n')) {
		return ExternalDiffState{}, false, fmt.Errorf("local grant is not canonical or has been tampered with")
	}
	saved.Path = state.Path
	return saved, true, nil
}

func ExternalDiffStatus(path string) (ExternalDiffState, error) {
	state, err := stateFor(path)
	if err != nil {
		return ExternalDiffState{}, err
	}
	root, exists, err := openConsentDir(state, false)
	if err != nil || !exists {
		return state, err
	}
	defer root.Close()
	saved, _, err := readGrant(root, state)
	return saved, err
}

func writeAll(file *os.File, data []byte) error {
	for len(data) > 0 {
		n, err := file.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

func createTemp(root *os.Root) (*os.File, string, error) {
	for attempt := 0; attempt < 8; attempt++ {
		random := make([]byte, 8)
		if _, err := rand.Read(random); err != nil {
			return nil, "", err
		}
		name := ".grant-" + hex.EncodeToString(random) + ".tmp"
		file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return nil, "", err
		}
		if err := file.Chmod(0600); err != nil {
			file.Close()
			root.Remove(name)
			return nil, "", err
		}
		return file, name, nil
	}
	return nil, "", fmt.Errorf("could not reserve an exclusive temp file")
}

func GrantExternalDiff(path string) (ExternalDiffState, error) {
	state, err := stateFor(path)
	if err != nil {
		return ExternalDiffState{}, err
	}
	root, _, err := openConsentDir(state, true)
	if err != nil {
		return ExternalDiffState{}, err
	}
	defer root.Close()
	if saved, exists, err := readGrant(root, state); err != nil || exists {
		return saved, err
	}
	state.Granted = true
	state.GrantedAt = time.Now().UTC()
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return ExternalDiffState{}, err
	}
	data = append(data, '\n')
	if len(data) > maxGrantBytes {
		return ExternalDiffState{}, fmt.Errorf("local grant exceeds the allowed size")
	}
	file, temp, err := createTemp(root)
	if err != nil {
		return ExternalDiffState{}, err
	}
	cleanup := true
	defer func() {
		if cleanup {
			root.Remove(temp)
		}
	}()
	if err := writeAll(file, data); err != nil {
		file.Close()
		return ExternalDiffState{}, err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return ExternalDiffState{}, err
	}
	if err := file.Close(); err != nil {
		return ExternalDiffState{}, err
	}
	if err := root.Link(temp, grantName(state)); err != nil {
		if saved, exists, readErr := readGrant(root, state); readErr == nil && exists {
			return saved, nil
		}
		return ExternalDiffState{}, fmt.Errorf("could not publish the grant without replacing the destination: %w", err)
	}
	if err := root.Remove(temp); err != nil {
		return ExternalDiffState{}, err
	}
	cleanup = false
	published, exists, err := readGrant(root, state)
	if err != nil {
		return ExternalDiffState{}, fmt.Errorf("published grant failed validation: %w", err)
	}
	if !exists {
		return ExternalDiffState{}, fmt.Errorf("published grant disappeared")
	}
	return published, nil
}

func RevokeExternalDiff(path string) error {
	state, err := stateFor(path)
	if err != nil {
		return err
	}
	root, exists, err := openConsentDir(state, false)
	if err != nil || !exists {
		return err
	}
	defer root.Close()
	if _, exists, err := readGrant(root, state); err != nil || !exists {
		return err
	}
	return root.Remove(grantName(state))
}
