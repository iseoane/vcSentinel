package setup

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"golang.org/x/term"
)

const repoOwner = "ISeoane-Quental"
const repoName = "vas.sentinel"

// fallbackGoInstall enables retrying with go install when the release
// download fails. Tests disable it so error paths do not run real builds.
var fallbackGoInstall = true

// envGitHubToken returns the GitHub token configured in the environment. A
// private repository requires authentication both to query the release and to
// download its assets: the client uses this token in both requests.
func envGitHubToken() string {
	return os.Getenv("GITHUB_TOKEN")
}

// PrepareGitHubToken makes sure the install/upgrade operation has credentials
// for private repositories. Resolution order:
//  1. GITHUB_TOKEN environment variable (if already set, does nothing).
//  2. Token from gh CLI (gh auth token) when gh is authenticated.
//  3. Interactive prompt asking the user whether they have a token.
//
// Returns the resolved token (possibly empty if there are no credentials).
func PrepareGitHubToken() string {
	if envGitHubToken() != "" {
		return envGitHubToken()
	}
	if token := tokenFromGhCLI(); token != "" {
		os.Setenv("GITHUB_TOKEN", token)
		return token
	}
	return promptForGitHubToken()
}

// tokenFromGhCLI obtains the token from gh CLI when the user is already
// authenticated in the repository (for example, because they publish
// releases). Returns an empty string if gh is unavailable or not logged in.
func tokenFromGhCLI() string {
	cmd := exec.Command("gh", "auth", "token")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// promptForGitHubToken asks for the token on the keyboard when it could not
// be resolved any other way. Returns an empty string if the user has no token
// (or there is no interactive terminal). The read never echoes the secret: it
// degrades to "no token" instead of silently reading with echo when there is
// no interactive terminal in between (pipes, CI).
func promptForGitHubToken() string {
	if !isStdinTerminal() {
		return ""
	}
	fmt.Println("🔑 The repository is private and no GITHUB_TOKEN was found in the environment.")
	fmt.Println("   If you have a GitHub token, paste it below (empty to continue without a token):")
	fmt.Print("   Token: ")

	token, err := readTokenWithoutEcho()
	if err != nil {
		fmt.Println()
		fmt.Printf("⚠️  Could not read the token without echo (%v). Continuing without a token.\n", err)
		return ""
	}
	token = strings.TrimSpace(token)
	if token != "" {
		os.Setenv("GITHUB_TOKEN", token)
	}
	return token
}

// isStdinTerminal reports whether standard input is an interactive terminal.
// It prevents the token prompt from blocking on tests, pipes, or non
// interactive execution. A variable (not a function) so tests can inject it
// without depending on a real terminal.
var isStdinTerminal = func() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// readTokenWithoutEcho is the injection seam: in production it delegates to
// the real mechanism that disables terminal echo; tests replace it to avoid
// depending on a real terminal.
var readTokenWithoutEcho = readTokenWithoutEchoFromSystem

// readTokenWithoutEchoFromSystem reads the token without the terminal
// repeating it on screen, using term.ReadPassword (same call family as
// ssh/sudo: TCGETS/TCSETS on Unix, console mode on Windows) over the stdin
// descriptor. It fails CLOSED on purpose: if it cannot disable the echo (for
// example, because stdin is not a real terminal even though it passed the
// isStdinTerminal check) it returns an error and does not read a single byte.
// Silently reading with echo in the face of that failure is exactly the
// defect this function exists to avoid: an exposed secret is worse than no
// secret.
func readTokenWithoutEchoFromSystem() (string, error) {
	bytes, err := term.ReadPassword(int(os.Stdin.Fd()))
	if err != nil {
		return "", fmt.Errorf("could not disable the terminal echo: %w", err)
	}
	return string(bytes), nil
}

type ReleaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	// URL is the GitHub API address of the asset. It is the reliable download
	// route on private repositories, where browser_download_url answers 404
	// even with a token.
	URL string `json:"url"`
}

type ReleaseInfo struct {
	TagName string         `json:"tag_name"`
	Assets  []ReleaseAsset `json:"assets"`
}

func fetchLatestRelease() (ReleaseInfo, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", repoOwner, repoName)

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return ReleaseInfo{}, fmt.Errorf("could not build the request to GitHub: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "vas-sentinel")
	if token := envGitHubToken(); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ReleaseInfo{}, fmt.Errorf("network error while querying the latest release on GitHub: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusUnauthorized {
		return ReleaseInfo{}, fmt.Errorf("no published release was found on %s/%s (HTTP %d). The repository must have a published release with assets; if it is private, configure the GITHUB_TOKEN environment variable", repoOwner, repoName, resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return ReleaseInfo{}, fmt.Errorf("GitHub answered with HTTP status %d while querying the latest release", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return ReleaseInfo{}, fmt.Errorf("could not read the response from GitHub: %w", err)
	}

	var release ReleaseInfo
	if err := json.Unmarshal(body, &release); err != nil {
		return ReleaseInfo{}, fmt.Errorf("could not parse the response from GitHub: %w", err)
	}
	if release.TagName == "" {
		return ReleaseInfo{}, fmt.Errorf("the queried release does not contain a valid tag_name")
	}

	return release, nil
}

// LatestReleaseTag reports the latest published release tag (for example
// "v0.2.0"). It performs a network call: its only reader is doctor
// --check-updates, which is off by default.
func LatestReleaseTag() (string, error) {
	release, err := fetchLatestRelease()
	if err != nil {
		return "", err
	}
	return release.TagName, nil
}

func pickAssetForOS(assets []ReleaseAsset) (ReleaseAsset, error) {
	return pickAssetForSystem(runtime.GOOS, runtime.GOARCH, assets)
}

func pickAssetForSystem(goos string, goarch string, assets []ReleaseAsset) (ReleaseAsset, error) {
	expectedName := "sentinel-" + goos + "-" + goarch
	if goos == "windows" {
		expectedName += ".exe"
	}

	for _, asset := range assets {
		if strings.EqualFold(asset.Name, expectedName) {
			return asset, nil
		}
	}

	names := make([]string, 0, len(assets))
	for _, asset := range assets {
		names = append(names, asset.Name)
	}
	return ReleaseAsset{}, fmt.Errorf("no release asset was found for your system (%s/%s). Expected the pattern %q. Available assets: %s", goos, goarch, expectedName, strings.Join(names, ", "))
}

// downloadBinary downloads a release asset. On private repositories it uses
// the asset's API URL (Accept: application/octet-stream) instead of the
// browser_download_url, which answers 404 in that case.
func downloadBinary(asset ReleaseAsset, destPath string) error {
	url := asset.BrowserDownloadURL
	if asset.URL != "" {
		url = asset.URL
	}

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("could not build the download request: %w", err)
	}
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("User-Agent", "vas-sentinel")
	if token := envGitHubToken(); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("network error while downloading the binary: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("the binary download failed with HTTP status %d", resp.StatusCode)
	}

	dest, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("could not create the destination file %s: %w", destPath, err)
	}

	_, err = io.Copy(dest, resp.Body)
	if cerr := dest.Close(); err == nil && cerr != nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("could not write the binary to %s: %w", destPath, err)
	}

	return nil
}
