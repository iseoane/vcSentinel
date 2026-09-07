package setup

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPickAssetForSystem(t *testing.T) {
	tests := []struct {
		name      string
		goos      string
		goarch    string
		assets    []ReleaseAsset
		want      string
		wantError bool
	}{
		{
			name:   "windows amd64 finds its asset",
			goos:   "windows",
			goarch: "amd64",
			assets: []ReleaseAsset{{Name: "sentinel-windows-amd64.exe", BrowserDownloadURL: "https://example/sentinel-windows-amd64.exe"}},
			want:   "sentinel-windows-amd64.exe",
		},
		{
			name:   "windows amd64 is case-insensitive",
			goos:   "windows",
			goarch: "amd64",
			assets: []ReleaseAsset{{Name: "SENTINEL-WINDOWS-AMD64.EXE"}},
			want:   "SENTINEL-WINDOWS-AMD64.EXE",
		},
		{
			name:   "linux amd64 finds its asset",
			goos:   "linux",
			goarch: "amd64",
			assets: []ReleaseAsset{{Name: "sentinel-linux-amd64"}},
			want:   "sentinel-linux-amd64",
		},
		{
			name:   "linux arm64 finds its asset",
			goos:   "linux",
			goarch: "arm64",
			assets: []ReleaseAsset{{Name: "sentinel-linux-arm64"}},
			want:   "sentinel-linux-arm64",
		},
		{
			name:      "windows without a windows asset fails with the expected pattern",
			goos:      "windows",
			goarch:    "amd64",
			assets:    []ReleaseAsset{{Name: "sentinel-linux-amd64"}, {Name: "sentinel-linux-arm64"}},
			wantError: true,
		},
		{
			name:      "empty assets fails",
			goos:      "linux",
			goarch:    "amd64",
			assets:    nil,
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			asset, err := pickAssetForSystem(tt.goos, tt.goarch, tt.assets)
			if tt.wantError {
				if err == nil {
					t.Fatalf("expected error, got none")
				}
				if tt.goos == "windows" && !strings.Contains(err.Error(), "sentinel-windows-amd64.exe") {
					t.Errorf("the error must include the expected pattern, got: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if asset.Name != tt.want {
				t.Errorf("asset.Name = %q, want %q", asset.Name, tt.want)
			}
		})
	}
}

func TestDownloadBinary(t *testing.T) {
	t.Run("successful download writes the exact content", func(t *testing.T) {
		const wantContent = "FAKE-BINARY"
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, err := w.Write([]byte(wantContent)); err != nil {
				t.Errorf("could not write the response: %v", err)
			}
		}))
		defer server.Close()

		destination := filepath.Join(t.TempDir(), "sentinel.exe")
		if err := downloadBinary(ReleaseAsset{BrowserDownloadURL: server.URL}, destination); err != nil {
			t.Fatalf("downloadBinary returned error: %v", err)
		}

		content, err := os.ReadFile(destination)
		if err != nil {
			t.Fatalf("could not read the destination: %v", err)
		}
		if string(content) != wantContent {
			t.Errorf("content = %q, want %q", content, wantContent)
		}
	})

	t.Run("uses the asset API URL when available", func(t *testing.T) {
		const wantContent = "VIA-API"
		var receivedURL string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			receivedURL = r.URL.Path
			if _, err := w.Write([]byte(wantContent)); err != nil {
				t.Errorf("could not write the response: %v", err)
			}
		}))
		defer server.Close()

		destination := filepath.Join(t.TempDir(), "sentinel.exe")
		asset := ReleaseAsset{
			BrowserDownloadURL: "https://github.com/unused-download",
			URL:                server.URL + "/assets/123",
		}
		if err := downloadBinary(asset, destination); err != nil {
			t.Fatalf("downloadBinary returned error: %v", err)
		}

		if receivedURL != "/assets/123" {
			t.Errorf("the download must use the asset API URL, got %q", receivedURL)
		}
		content, err := os.ReadFile(destination)
		if err != nil {
			t.Fatalf("could not read the destination: %v", err)
		}
		if string(content) != wantContent {
			t.Errorf("content = %q, want %q", content, wantContent)
		}
	})

	t.Run("sends Bearer when GITHUB_TOKEN is set", func(t *testing.T) {
		const wantToken = "token-private-123"
		gotToken := false
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if got := r.Header.Get("Authorization"); got == "Bearer "+wantToken {
				gotToken = true
			}
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		original := os.Getenv("GITHUB_TOKEN")
		os.Setenv("GITHUB_TOKEN", wantToken)
		defer os.Setenv("GITHUB_TOKEN", original)

		destination := filepath.Join(t.TempDir(), "sentinel.exe")
		if err := downloadBinary(ReleaseAsset{BrowserDownloadURL: server.URL}, destination); err != nil {
			t.Fatalf("downloadBinary returned error: %v", err)
		}
		if !gotToken {
			t.Error("the download did not send the Authorization header with the token")
		}
	})

	t.Run("404 response returns an error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.NotFound(w, r)
		}))
		defer server.Close()

		destination := filepath.Join(t.TempDir(), "sentinel.exe")
		err := downloadBinary(ReleaseAsset{BrowserDownloadURL: server.URL}, destination)
		if err == nil {
			t.Fatalf("expected error with HTTP 404")
		}
		if !strings.Contains(err.Error(), "404") {
			t.Errorf("the error must mention HTTP status 404, got: %v", err)
		}
	})
}

type fakeTransport struct {
	response     *http.Response
	networkError error
	checkRequest func(r *http.Request)
}

func (t *fakeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.checkRequest != nil {
		t.checkRequest(req)
	}
	if t.networkError != nil {
		return nil, t.networkError
	}
	return t.response, nil
}

func setFakeHTTPClient(t *testing.T, transport http.RoundTripper) {
	t.Helper()
	original := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: transport}
	t.Cleanup(func() { http.DefaultClient = original })
}

func jsonResponse(t *testing.T, body string, status int) *http.Response {
	t.Helper()
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestFetchLatestRelease(t *testing.T) {
	t.Run("valid release", func(t *testing.T) {
		body := `{"tag_name":"v1.2.3","assets":[{"name":"sentinel-windows-amd64.exe","browser_download_url":"https://example/x"}]}`
		transport := &fakeTransport{response: jsonResponse(t, body, http.StatusOK)}
		transport.checkRequest = func(r *http.Request) {
			if r.Header.Get("User-Agent") == "" {
				t.Errorf("the request must include User-Agent")
			}
		}
		setFakeHTTPClient(t, transport)

		release, err := fetchLatestRelease()
		if err != nil {
			t.Fatalf("fetchLatestRelease returned error: %v", err)
		}
		if release.TagName != "v1.2.3" {
			t.Errorf("TagName = %q, want v1.2.3", release.TagName)
		}
		if len(release.Assets) != 1 || release.Assets[0].Name != "sentinel-windows-amd64.exe" {
			t.Errorf("unexpected assets: %+v", release.Assets)
		}
	})

	t.Run("HTTP 404 returns an unpublished-release error", func(t *testing.T) {
		setFakeHTTPClient(t, &fakeTransport{response: jsonResponse(t, "{}", http.StatusNotFound)})
		_, err := fetchLatestRelease()
		if err == nil {
			t.Fatalf("expected error with HTTP 404")
		}
		if !strings.Contains(err.Error(), "no published release") {
			t.Errorf("the error must mention the unpublished release, got: %v", err)
		}
	})

	t.Run("HTTP 500 returns a status error", func(t *testing.T) {
		setFakeHTTPClient(t, &fakeTransport{response: jsonResponse(t, "{}", http.StatusInternalServerError)})
		_, err := fetchLatestRelease()
		if err == nil {
			t.Fatalf("expected error with HTTP 500")
		}
		if !strings.Contains(err.Error(), "500") {
			t.Errorf("the error must mention status 500, got: %v", err)
		}
	})

	t.Run("invalid JSON returns a parse error", func(t *testing.T) {
		setFakeHTTPClient(t, &fakeTransport{response: jsonResponse(t, "not json", http.StatusOK)})
		_, err := fetchLatestRelease()
		if err == nil {
			t.Fatalf("expected error with invalid JSON")
		}
		if !strings.Contains(err.Error(), "parse the response") {
			t.Errorf("the error must mention parsing the JSON, got: %v", err)
		}
	})

	t.Run("missing tag_name returns an error", func(t *testing.T) {
		setFakeHTTPClient(t, &fakeTransport{response: jsonResponse(t, `{"assets":[]}`, http.StatusOK)})
		_, err := fetchLatestRelease()
		if err == nil {
			t.Fatalf("expected error without tag_name")
		}
		if !strings.Contains(err.Error(), "tag_name") {
			t.Errorf("the error must mention tag_name, got: %v", err)
		}
	})

	t.Run("network error returns a network error", func(t *testing.T) {
		setFakeHTTPClient(t, &fakeTransport{networkError: errors.New("connection refused")})
		_, err := fetchLatestRelease()
		if err == nil {
			t.Fatalf("expected a network error")
		}
		if !strings.Contains(err.Error(), "network error") {
			t.Errorf("the error must mention the network, got: %v", err)
		}
	})
}

// TestPromptForGitHubTokenReadsWithoutEcho checks that the token enters
// through the injection seam (without going through bufio.Stdin in the test)
// and that it never appears on the process standard output.
func TestPromptForGitHubTokenReadsWithoutEcho(t *testing.T) {
	t.Run("injected token is resolved without appearing on stdout", func(t *testing.T) {
		originalToken := os.Getenv("GITHUB_TOKEN")
		os.Unsetenv("GITHUB_TOKEN")
		defer os.Setenv("GITHUB_TOKEN", originalToken)

		originalTerminal := isStdinTerminal
		isStdinTerminal = func() bool { return true }
		defer func() { isStdinTerminal = originalTerminal }()

		const fakeToken = "secret-token-xyz-789"
		originalReader := readTokenWithoutEcho
		readTokenWithoutEcho = func() (string, error) { return fakeToken, nil }
		defer func() { readTokenWithoutEcho = originalReader }()

		originalStdout := os.Stdout
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatalf("could not create the pipe: %v", err)
		}
		os.Stdout = w

		token := promptForGitHubToken()

		w.Close()
		os.Stdout = originalStdout
		output, _ := io.ReadAll(r)

		if token != fakeToken {
			t.Errorf("token = %q, want %q", token, fakeToken)
		}
		if os.Getenv("GITHUB_TOKEN") != fakeToken {
			t.Errorf("GITHUB_TOKEN was not set to the injected token")
		}
		if strings.Contains(string(output), fakeToken) {
			t.Errorf("the token must not appear on standard output, output: %q", output)
		}
	})

	t.Run("reader error sets no token and returns empty", func(t *testing.T) {
		originalToken := os.Getenv("GITHUB_TOKEN")
		os.Unsetenv("GITHUB_TOKEN")
		defer os.Setenv("GITHUB_TOKEN", originalToken)

		originalTerminal := isStdinTerminal
		isStdinTerminal = func() bool { return true }
		defer func() { isStdinTerminal = originalTerminal }()

		originalReader := readTokenWithoutEcho
		readTokenWithoutEcho = func() (string, error) { return "", errors.New("no terminal available") }
		defer func() { readTokenWithoutEcho = originalReader }()

		if token := promptForGitHubToken(); token != "" {
			t.Errorf("token = %q, want empty on reader error", token)
		}
		if os.Getenv("GITHUB_TOKEN") != "" {
			t.Errorf("GITHUB_TOKEN must not be set when the reader fails")
		}
	})

	t.Run("real mechanism fails closed when stdin is not a terminal", func(t *testing.T) {
		// Does not inject readTokenWithoutEcho: it exercises the real
		// readTokenWithoutEchoFromSystem (term.ReadPassword) against a stdin
		// that is not a terminal, which is exactly the case that used to read
		// silently with echo instead of failing.
		originalStdin := os.Stdin
		reader, writer, err := os.Pipe()
		if err != nil {
			t.Fatalf("could not create the pipe: %v", err)
		}
		os.Stdin = reader
		defer func() { os.Stdin = originalStdin }()

		if _, err := writer.WriteString("token-that-must-not-be-read\n"); err != nil {
			t.Fatalf("could not write to the pipe: %v", err)
		}
		writer.Close()

		token, err := readTokenWithoutEchoFromSystem()
		if err == nil {
			t.Fatalf("expected error when the echo cannot be disabled, token=%q", token)
		}
		if token != "" {
			t.Errorf("token = %q, expected empty when the no-echo mechanism fails", token)
		}
	})

	t.Run("without an interactive terminal the reader is not invoked", func(t *testing.T) {
		originalTerminal := isStdinTerminal
		isStdinTerminal = func() bool { return false }
		defer func() { isStdinTerminal = originalTerminal }()

		originalReader := readTokenWithoutEcho
		invoked := false
		readTokenWithoutEcho = func() (string, error) { invoked = true; return "should-not-be-used", nil }
		defer func() { readTokenWithoutEcho = originalReader }()

		promptForGitHubToken()

		if invoked {
			t.Errorf("the reader must not be invoked without an interactive terminal")
		}
	})
}

func TestPickAssetForOS(t *testing.T) {
	name := "sentinel-" + runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		name += ".exe"
	}

	asset, err := pickAssetForOS([]ReleaseAsset{{Name: name, BrowserDownloadURL: "https://example/" + name}})
	if err != nil {
		t.Fatalf("pickAssetForOS returned error: %v", err)
	}
	if asset.Name != name {
		t.Errorf("asset.Name = %q, want %q", asset.Name, name)
	}
}

func validReleaseWithAsset() string {
	name := "sentinel-" + runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return `{"tag_name":"v1.2.3","assets":[{"name":"` + name + `","browser_download_url":"http://127.0.0.1:1/download"}]}`
}

// disableGoInstallFallback turns off the go install retry during a test and
// restores it at the end, so network/download errors do not run real builds.
func disableGoInstallFallback(t *testing.T) {
	t.Helper()
	previous := fallbackGoInstall
	fallbackGoInstall = false
	t.Cleanup(func() { fallbackGoInstall = previous })
}

func TestRunFullInstall(t *testing.T) {
	t.Run("network error stops the installation", func(t *testing.T) {
		disableGoInstallFallback(t)
		setFakeHTTPClient(t, &fakeTransport{networkError: errors.New("connection refused")})
		err := RunFullInstall()
		if err == nil {
			t.Fatalf("expected a network error")
		}
		if !strings.Contains(err.Error(), "network error") {
			t.Errorf("the error must mention the network, got: %v", err)
		}
	})

	t.Run("no asset for the system stops the installation", func(t *testing.T) {
		body := `{"tag_name":"v1.2.3","assets":[{"name":"sentinel-plan9-amd64","browser_download_url":"https://example/x"}]}`
		setFakeHTTPClient(t, &fakeTransport{response: jsonResponse(t, body, http.StatusOK)})
		err := RunFullInstall()
		if err == nil {
			t.Fatalf("expected error without an asset for the system")
		}
		if !strings.Contains(err.Error(), "no release asset") {
			t.Errorf("the error must mention the asset selection, got: %v", err)
		}
	})

	t.Run("failed download stops the installation", func(t *testing.T) {
		disableGoInstallFallback(t)
		setFakeHTTPClient(t, &fakeTransport{response: jsonResponse(t, validReleaseWithAsset(), http.StatusOK)})
		err := RunFullInstall()
		if err == nil {
			t.Fatalf("expected a download error")
		}
		if !strings.Contains(err.Error(), "download") {
			t.Errorf("the error must mention the download, got: %v", err)
		}
	})
}

func TestRunUpgradeFromGitHub(t *testing.T) {
	t.Run("network error stops the upgrade", func(t *testing.T) {
		disableGoInstallFallback(t)
		setFakeHTTPClient(t, &fakeTransport{networkError: errors.New("connection refused")})
		err := RunUpgradeFromGitHub()
		if err == nil {
			t.Fatalf("expected a network error")
		}
		if !strings.Contains(err.Error(), "network error") {
			t.Errorf("the error must mention the network, got: %v", err)
		}
	})

	t.Run("no asset for the system stops the upgrade", func(t *testing.T) {
		body := `{"tag_name":"v1.2.3","assets":[{"name":"sentinel-plan9-amd64","browser_download_url":"https://example/x"}]}`
		setFakeHTTPClient(t, &fakeTransport{response: jsonResponse(t, body, http.StatusOK)})
		err := RunUpgradeFromGitHub()
		if err == nil {
			t.Fatalf("expected error without an asset for the system")
		}
		if !strings.Contains(err.Error(), "no release asset") {
			t.Errorf("the error must mention the asset selection, got: %v", err)
		}
	})

	t.Run("failed download stops the upgrade before replacing", func(t *testing.T) {
		disableGoInstallFallback(t)
		setFakeHTTPClient(t, &fakeTransport{response: jsonResponse(t, validReleaseWithAsset(), http.StatusOK)})
		err := RunUpgradeFromGitHub()
		if err == nil {
			t.Fatalf("expected a download error")
		}
		if !strings.Contains(err.Error(), "download") {
			t.Errorf("the error must mention the download, got: %v", err)
		}
	})
}
