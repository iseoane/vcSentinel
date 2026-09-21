package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const releaseFileName = "release.yml"

type asset struct {
	goos   string
	goarch string
}

func main() {
	if err := generateAssets(); err != nil {
		fmt.Printf("❌ %v\n", err)
		os.Exit(1)
	}
}

func generateAssets() error {
	version, assets, err := readRelease(releaseFileName)
	if err != nil {
		return err
	}

	if err := checkPublishedVersion(version); err != nil {
		return err
	}

	outputDir := filepath.Join("bin", version)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("could not create the directory %s: %w", outputDir, err)
	}

	names := make([]string, 0, len(assets))
	for _, a := range assets {
		name := "vcsentinel-" + a.goos + "-" + a.goarch
		if a.goos == "windows" {
			name += ".exe"
		}
		names = append(names, name)
	}

	for i, a := range assets {
		fmt.Printf("🔨 Building vcsentinel-%s-%s ...\n", a.goos, a.goarch)
		if err := buildAsset(version, a, names[i]); err != nil {
			return err
		}
	}

	fmt.Printf("✅ Assets generated in bin/%s/: %s\n", version, strings.Join(names, ", "))
	return nil
}

// checkPublishedVersion aborts asset generation when the release.yml version
// is already published or older than the latest published one. It queries the
// latest release tag with the gh CLI; if there is no previous release (first
// publication) or gh is unavailable, it allows continuing.
func checkPublishedVersion(version string) error {
	tag, err := getPublishedTag()
	if err != nil {
		fmt.Printf("⚠️ Could not query the latest published release: %v\n", err)
		return nil
	}
	if tag == "" {
		return nil
	}

	published := strings.TrimPrefix(strings.TrimSpace(tag), "v")
	if versionLessOrEqual(version, published) {
		return fmt.Errorf(
			"the version %q is not higher than the already published one (%s). You must bump the version in %s before publishing",
			version, published, releaseFileName,
		)
	}
	fmt.Printf("✅ The version %s is higher than the published one (%s).", version, published)
	return nil
}

// getPublishedTag returns the tag_name of the latest release via gh CLI
// (reuses gh's authenticated session). Returns an empty string if no release
// has been published yet.
func getPublishedTag() (string, error) {
	cmd := exec.Command("gh", "release", "view", "--json", "tagName", "--jq", ".tagName")
	out, err := cmd.CombinedOutput()
	if err != nil {
		output := strings.TrimSpace(string(out))
		if strings.Contains(output, "not found") || strings.Contains(output, "Not Found") {
			return "", nil
		}
		return "", fmt.Errorf("gh release view failed: %v (output: %s)", err, output)
	}
	return strings.TrimSpace(string(out)), nil
}

// versionLessOrEqual compares two semver versions (major.minor.patch). It
// returns true if new <= published. If either one lacks a recognizable
// numeric format, it compares by length and lexicographically to avoid
// blocking unusual versions.
func versionLessOrEqual(newVersion string, publishedVersion string) bool {
	n := versionComponents(newVersion)
	p := versionComponents(publishedVersion)
	limit := len(n)
	if len(p) > limit {
		limit = len(p)
	}
	ni, pi := 0, 0
	for i := range limit {
		ni, pi = 0, 0
		if i < len(n) {
			ni = n[i]
		}
		if i < len(p) {
			pi = p[i]
		}
		if ni < pi {
			return true
		}
		if ni > pi {
			return false
		}
	}
	return true
}

// versionComponents converts "1.2.3" (or "v1.2.3-beta") into [1, 2, 3],
// ignoring "v" prefixes and non-numeric suffixes. Non-numeric components
// count as 0.
func versionComponents(v string) []int {
	parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(v), "v"), ".")
	components := make([]int, 0, len(parts))
	for _, part := range parts {
		num := 0
		for _, r := range part {
			if r < '0' || r > '9' {
				break
			}
			num = num*10 + int(r-'0')
		}
		components = append(components, num)
	}
	return components
}

func buildAsset(version string, a asset, name string) error {
	outputPath := filepath.Join("bin", version, name)
	cmd := exec.Command("go", "build",
		"-ldflags", fmt.Sprintf("-s -w -X main.version=%s", version),
		"-o", outputPath,
		"./cmd/vcsentinel/main.go",
	)
	cmd.Env = append(os.Environ(), "GOOS="+a.goos, "GOARCH="+a.goarch)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("building vcsentinel-%s-%s failed: %w\n%s", a.goos, a.goarch, err, output)
	}
	return nil
}

func readRelease(path string) (string, []asset, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil, fmt.Errorf("%s was not found in the current directory. Run this command from the repository root", path)
		}
		return "", nil, fmt.Errorf("could not read %s: %w", path, err)
	}

	var version string
	var assets []asset
	var pendingGoos string

	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "version:") {
			version = extractValue(line)
			continue
		}
		if strings.HasPrefix(line, "- goos:") {
			pendingGoos = extractValue(strings.TrimSpace(strings.TrimPrefix(line, "-")))
			continue
		}
		if strings.HasPrefix(line, "goarch:") && pendingGoos != "" {
			assets = append(assets, asset{goos: pendingGoos, goarch: extractValue(line)})
			pendingGoos = ""
		}
	}

	if version == "" {
		return "", nil, fmt.Errorf("the \"version\" key was not found in %s", path)
	}
	if len(assets) == 0 {
		return "", nil, fmt.Errorf("no assets were defined (\"goos\"/\"goarch\" blocks) in %s", path)
	}

	return version, assets, nil
}

func extractValue(line string) string {
	_, value, _ := strings.Cut(line, ":")
	value = strings.TrimSpace(value)
	value = strings.Trim(value, `"'`)
	return value
}
