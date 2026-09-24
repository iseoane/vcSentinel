package setup

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

const (
	testReleaseAPIURLEnv    = "VCSENTINEL_TEST_RELEASE_API_URL"
	testInstallRootEnv      = "VCSENTINEL_TEST_INSTALL_ROOT"
	testWindowsPathFileEnv  = "VCSENTINEL_TEST_WINDOWS_PATH_FILE"
	testDisableGoInstallEnv = "VCSENTINEL_TEST_DISABLE_GO_INSTALL_FALLBACK"
	defaultReleaseAPIURL    = "https://api.github.com/repos/" + repoOwner + "/" + repoName + "/releases/latest"
)

// releaseAPIURL resolves the release endpoint used by install and upgrade.
// The test-only override is deliberately limited to loopback HTTP so a test
// cannot redirect a release operation to an arbitrary network service.
func releaseAPIURL() (string, error) {
	value := os.Getenv(testReleaseAPIURLEnv)
	if value == "" {
		return defaultReleaseAPIURL, nil
	}

	parsed, err := url.Parse(value)
	if err != nil {
		return "", fmt.Errorf("%s must be an absolute http URL on localhost, 127.0.0.1, or ::1: %w", testReleaseAPIURLEnv, err)
	}
	if !parsed.IsAbs() || !strings.EqualFold(parsed.Scheme, "http") || parsed.Host == "" || parsed.User != nil || parsed.Opaque != "" {
		return "", fmt.Errorf("%s must be an absolute http URL on localhost, 127.0.0.1, or ::1", testReleaseAPIURLEnv)
	}

	host := strings.ToLower(parsed.Hostname())
	if host != "localhost" && host != "127.0.0.1" && host != "::1" {
		return "", fmt.Errorf("%s must target localhost, 127.0.0.1, or ::1; got %q", testReleaseAPIURLEnv, host)
	}
	if port := parsed.Port(); port != "" {
		value, err := strconv.Atoi(port)
		if err != nil || value < 1 || value > 65535 {
			return "", fmt.Errorf("%s contains an invalid port %q", testReleaseAPIURLEnv, port)
		}
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("%s must not contain a query or fragment", testReleaseAPIURLEnv)
	}

	return value, nil
}

// installRoot resolves the directory that owns the installed binary. An
// override is accepted only below the process's effective temporary
// directory; the normal platform locations remain unchanged when it is absent.
func installRoot() (string, error) {
	value := os.Getenv(testInstallRootEnv)
	if value != "" {
		return validateDisposablePath(testInstallRootEnv, value)
	}
	return defaultInstallRoot()
}

func defaultInstallRoot() (string, error) {
	if runtime.GOOS != "windows" {
		return "/usr/local/bin", nil
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("could not identify the user's home directory: %w", err)
	}
	return filepath.Join(homeDir, ".vcsentinel", "bin"), nil
}

func installBinaryPath() (string, error) {
	root, err := installRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, goBinaryName()), nil
}

func shellPathLine() (string, error) {
	root, err := installRoot()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(`export PATH="%s:$PATH"`, filepath.ToSlash(root)), nil
}

func windowsPathFileOverride() (string, error) {
	value := os.Getenv(testWindowsPathFileEnv)
	if value == "" {
		return "", nil
	}
	return validateDisposablePath(testWindowsPathFileEnv, value)
}

func fallbackSetting() (bool, error) {
	value := os.Getenv(testDisableGoInstallEnv)
	if value == "" {
		return fallbackGoInstall, nil
	}
	if value != "1" {
		return false, fmt.Errorf("%s must be exactly 1 when set", testDisableGoInstallEnv)
	}
	return false, nil
}

// validateSetupTestOverrides validates every test-only setup seam before a
// public setup operation can perform network, filesystem, or fallback work.
func validateSetupTestOverrides() error {
	if _, err := releaseAPIURL(); err != nil {
		return err
	}
	if _, err := installRoot(); err != nil {
		return err
	}
	if _, err := windowsPathFileOverride(); err != nil {
		return err
	}
	if _, err := fallbackSetting(); err != nil {
		return err
	}
	return nil
}

func validateDisposablePath(envName, value string) (string, error) {
	if !filepath.IsAbs(value) {
		return "", fmt.Errorf("%s must be an absolute path under the effective temporary directory", envName)
	}

	candidate := filepath.Clean(value)
	tempDir, err := filepath.Abs(os.TempDir())
	if err != nil {
		return "", fmt.Errorf("could not resolve the effective temporary directory for %s: %w", envName, err)
	}

	resolvedTempDir, err := resolvePathForValidation(tempDir)
	if err != nil {
		return "", fmt.Errorf("could not resolve the effective temporary directory for %s: %w", envName, err)
	}
	resolvedCandidate, err := resolvePathForValidation(candidate)
	if err != nil {
		return "", fmt.Errorf("could not resolve %s: %w", envName, err)
	}
	if !pathWithin(resolvedTempDir, resolvedCandidate) {
		return "", fmt.Errorf("%s must be under the effective temporary directory %q", envName, tempDir)
	}

	return candidate, nil
}

func resolvePathForValidation(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	absolute = filepath.Clean(absolute)

	current := absolute
	missing := make([]string, 0, 4)
	for {
		_, err := os.Lstat(current)
		switch {
		case err == nil:
			resolved, err := filepath.EvalSymlinks(current)
			if err != nil {
				return "", err
			}
			for index := len(missing) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, missing[index])
			}
			return filepath.Clean(resolved), nil
		case os.IsNotExist(err):
			parent := filepath.Dir(current)
			if parent == current {
				return absolute, nil
			}
			missing = append(missing, filepath.Base(current))
			current = parent
		default:
			return "", err
		}
	}
}

func pathWithin(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}
