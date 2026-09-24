package setup

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const globalConfigTemplate = `version: "1.0"
active_agent: "auto"
agents:
  claude:
    model: "claude-5-sonnet"
    reasoning_effort: "high"
    # 'commit' profile ('vcsentinel slice' commit messages): low reasoning
    # because naming a commit does not need the reasoning of the rest of the
    # task. Example (uncomment and adjust):
    # profiles:
    #   commit:
    #     reasoning_effort: "low"
  opencode:
    model: "deepseek-v4-flash"
    reasoning_effort: "max"
    # profiles:
    #   commit:
    #     reasoning_effort: "low"
`

// perProjectConfigTemplate is the template init writes into the repo.
// Deliberately does NOT pin active_agent/agents to literal values: doing so
// would overwrite, in every repository, the preferences defined in the global
// config (defaults -> global -> per-project), leaving the global yml with no
// real effect. Only what the user uncomments here overrides the global
// config for this repo.
const perProjectConfigTemplate = `version: "1.0"
# Per-project vcSentinel configuration. Override here only what this repo
# needs different from your global config (~/.vcsentinel/vcsentinel.yml,
# created by 'vcsentinel install'). Anything you do not define is resolved from
# there.
#
# Deterministic verification WITHOUT agent: if you define these lists, 'pr
# review' runs the commands directly in the system shell and NEVER consults
# the agent (the agent is only offered when nothing is configured).
# Uncomment and adjust:
# lint_commands:
#   - "go vet ./..."
# test_commands:
#   - "go test ./..."
# build_commands:
#   - "go build ./..."
#
# Package-based validation: enable scope only on commands that truly support
# it. go test accepts import paths; gofmt, go vet and go build remain complete.
# validation:
#   capabilities:
#     unit_test:
#       command: "go test ./..."
#       supports_scope: true
#       scoped_command: "go test {packages}"
#   profiles:
#     standard: [unit_test]
#   mode: worktree
#
# Commit message language for 'vcsentinel slice'. Defaults to
# "en". Without pinning it, the agent picked at random and mixed languages
# within the same fragmentation.
# commit_language: "en"
#
# Repository request: allows external semantic generation, but it is NOT
# personal consent. The local unversioned grant is also required via
# 'vcsentinel consent-diff grant'.
request_external_agent_diff: false
#
# CodeGraph context for the reviewer: only metadata of affected test paths.
# Also requires the local consent above.
review:
  codegraph_context: false
#
# Example (uncomment and adjust):
# active_agent: "claude"
# agents:
#   claude:
#     model: "claude-opus"
#     reasoning_effort: "high"
#     profiles:
#       commit:
#         reasoning_effort: "low"
# (The 'commit' profile defines the model/effort that 'vcsentinel slice' uses to
# generate commit messages; if you do not define it, the base model/effort of
# the agent is used with no behavior change.)
`

func RunFullInstall() error {
	if err := validateSetupTestOverrides(); err != nil {
		return err
	}
	fallback, err := fallbackSetting()
	if err != nil {
		return err
	}

	fmt.Println("⬇️ Downloading the latest version from GitHub...")

	PrepareGitHubToken()

	release, err := fetchLatestRelease()
	if err != nil {
		if fallback {
			if errFallback := InstallViaGoInstall(); errFallback != nil {
				return fmt.Errorf("%v\nIn addition, the go install fallback failed: %v", err, errFallback)
			}
			return nil
		}
		return err
	}

	asset, err := pickAssetForOS(release.Assets)
	if err != nil {
		return err
	}

	tmpFile, err := os.CreateTemp("", "vcsentinel-download-*")
	if err != nil {
		return fmt.Errorf("could not create the temporary download file: %w", err)
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(tmpPath)

	if err := downloadBinary(asset, tmpPath); err != nil {
		if fallback {
			if errFallback := InstallViaGoInstall(); errFallback != nil {
				return fmt.Errorf("%v\nIn addition, the go install fallback failed: %v", err, errFallback)
			}
			return nil
		}
		return err
	}

	var destination string
	switch runtime.GOOS {
	case "windows":
		destination, err = installWindows(tmpPath)
	default:
		destination, err = installLinux(tmpPath)
	}
	if err != nil {
		return err
	}

	if err := createGlobalConfig(); err != nil {
		return err
	}

	fmt.Printf("✅ Installed at: %s\n", destination)
	return nil
}

// InstallViaGoInstall is the installation fallback when the release download
// fails (for example, a private repository without a token). Builds and
// installs from source with go install and leaves the binary in the same
// destination as a normal installation.
func InstallViaGoInstall() error {
	fmt.Println("⬇️ Download unavailable. Retrying with go install (builds from source)...")

	output, err := runGoInstall()
	if err != nil {
		return fmt.Errorf("go install failed (are GOPRIVATE and git credentials configured?): %w\n%s", err, output)
	}

	gopath, err := goEnvGOPATH()
	if err != nil {
		return err
	}
	binary := filepath.Join(gopath, "bin", goBinaryName())

	if err := installCompiledBinary(binary); err != nil {
		return err
	}
	return nil
}

// runGoInstall builds and installs the latest version with go install. For
// private repositories it sets GOPRIVATE (avoids consulting sum.golang.org)
// and hands git credentials over via GIT_CONFIG_COUNT, without touching the
// user's global configuration.
//
// It first tries the latest release (@latest) and, if that version does not
// contain the package (a release predating a structural change), it falls
// back to the main branch.
func runGoInstall() ([]byte, error) {
	PrepareGitHubToken()

	output, err := goInstallPackage("github.com/ISeoane-Quental/vcSentinel/cmd/vcsentinel@latest")
	if err != nil && strings.Contains(string(output), "does not contain package") {
		fmt.Println("⚠️ The latest release does not include the current package. Trying the main branch...")
		output, err = goInstallPackage("github.com/ISeoane-Quental/vcSentinel/cmd/vcsentinel@main")
	}
	return output, err
}

// goInstallPackage runs go install of a package with the environment prepared
// for private repositories (GOPRIVATE + temporary git credentials).
func goInstallPackage(pkg string) ([]byte, error) {
	cmd := exec.Command("go", "install", pkg)
	cmd.Env = append(os.Environ(), "GOPRIVATE=github.com/ISeoane-Quental/*")

	if token := envGitHubToken(); token != "" {
		cmd.Env = append(cmd.Env,
			"GIT_CONFIG_COUNT=1",
			"GIT_CONFIG_KEY_0=url.https://x-access-token:"+token+"@github.com/.insteadOf",
			"GIT_CONFIG_VALUE_0=https://github.com/",
		)
	}

	return cmd.CombinedOutput()
}

// installCompiledBinary installs an already-built binary into the system
// destination and creates the global configuration, just like the normal flow.
func installCompiledBinary(binary string) error {
	var destination string
	var err error
	switch runtime.GOOS {
	case "windows":
		destination, err = installWindows(binary)
	default:
		destination, err = installLinux(binary)
	}
	if err != nil {
		return err
	}

	if err := createGlobalConfig(); err != nil {
		return err
	}

	fmt.Printf("✅ Installed at: %s (via go install)\n", destination)
	return nil
}

// goEnvGOPATH returns the configured GOPATH.
func goEnvGOPATH() (string, error) {
	cmd := exec.Command("go", "env", "GOPATH")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("could not query GOPATH: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// goBinaryName returns the name of the binary produced by go install.
func goBinaryName() string {
	if runtime.GOOS == "windows" {
		return "vcsentinel.exe"
	}
	return "vcsentinel"
}

func windowsBinaryPath(homeDir string) string {
	return filepath.Join(homeDir, ".vcsentinel", "bin", "vcsentinel.exe")
}

func installWindows(tmpPath string) (string, error) {
	dir, err := installRoot()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("could not create the directory %s: %w", dir, err)
	}

	destination := filepath.Join(dir, goBinaryName())
	if err := moveAndReplace(tmpPath, destination); err != nil {
		return "", fmt.Errorf("could not install the binary at %s: %w", destination, err)
	}

	if err := addWindowsPath(dir); err != nil {
		return "", err
	}

	return destination, nil
}

func needsWindowsPathUpdate(currentPath string, dir string) bool {
	wanted := filepath.Clean(dir)
	for _, entry := range strings.Split(currentPath, ";") {
		if strings.EqualFold(filepath.Clean(strings.TrimSpace(entry)), wanted) {
			return false
		}
	}
	return true
}

func addWindowsPath(dir string) error {
	currentPath, err := windowsUserPath()
	if err != nil {
		return err
	}
	if !needsWindowsPathUpdate(currentPath, dir) {
		return nil
	}

	newPath := dir
	if currentPath != "" {
		newPath = currentPath + ";" + dir
	}
	pathFile, err := windowsPathFileOverride()
	if err != nil {
		return err
	}
	if pathFile != "" {
		if err := writeWindowsPathFile(pathFile, newPath); err != nil {
			return err
		}
	} else {
		command := fmt.Sprintf("[Environment]::SetEnvironmentVariable('Path', '%s', 'User')", newPath)
		cmd := exec.Command("powershell", "-NoProfile", "-Command", command)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("could not add %s to the user PATH: %w", dir, err)
		}
	}

	fmt.Printf("🛣️ Path added to the user PATH: %s\n", dir)

	return nil
}

func windowsUserPath() (string, error) {
	pathFile, err := windowsPathFileOverride()
	if err != nil {
		return "", err
	}
	if pathFile != "" {
		content, err := os.ReadFile(pathFile)
		if err != nil {
			if os.IsNotExist(err) {
				return "", nil
			}
			return "", fmt.Errorf("could not read the user PATH file %s: %w", pathFile, err)
		}
		return strings.TrimRight(string(content), "\r\n"), nil
	}

	cmd := exec.Command("powershell", "-NoProfile", "-Command", "[Environment]::GetEnvironmentVariable('Path','User')")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("could not query the user PATH: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func writeWindowsPathFile(path, value string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("could not create the user PATH file directory %s: %w", filepath.Dir(path), err)
	}

	suffix := ""
	if content, err := os.ReadFile(path); err == nil {
		if strings.HasSuffix(string(content), "\r\n") {
			suffix = "\r\n"
		} else if strings.HasSuffix(string(content), "\n") {
			suffix = "\n"
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("could not read the user PATH file %s: %w", path, err)
	}

	if err := os.WriteFile(path, []byte(value+suffix), 0644); err != nil {
		return fmt.Errorf("could not write the user PATH file %s: %w", path, err)
	}
	return nil
}

func linuxBinaryPath() string {
	return "/usr/local/bin/vcsentinel"
}

func installLinux(tmpPath string) (string, error) {
	destination, err := installBinaryPath()
	if err != nil {
		return "", err
	}
	if os.Getenv(testInstallRootEnv) != "" {
		if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
			return "", fmt.Errorf("could not create the directory %s: %w", filepath.Dir(destination), err)
		}
	}

	if err := moveAndReplace(tmpPath, destination); err != nil {
		if err := runSudoMove(tmpPath, destination); err != nil {
			return "", fmt.Errorf("could not install the binary at %s. Run the installation with superuser permissions (for example: 'sudo mv %s %s' and then 'sudo chmod 0755 %s'): %w", destination, tmpPath, destination, destination, err)
		}
	}

	if err := os.Chmod(destination, 0755); err != nil {
		if err := runSudoChmod(destination); err != nil {
			return "", fmt.Errorf("could not grant execution permissions to %s: %w", destination, err)
		}
	}

	if err := addShellPathBlock(); err != nil {
		return "", err
	}

	return destination, nil
}

func runSudoMove(source string, destination string) error {
	cmd := exec.Command("sudo", "mv", source, destination)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("the sudo attempt to move the binary failed: %w", err)
	}
	return nil
}

func runSudoChmod(destination string) error {
	cmd := exec.Command("sudo", "chmod", "0755", destination)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("the sudo attempt to grant permissions failed: %w", err)
	}
	return nil
}

func addShellPathBlock() error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("could not identify the user's home directory: %w", err)
	}

	zshrc := filepath.Join(homeDir, ".zshrc")
	bashrc := filepath.Join(homeDir, ".bashrc")

	paths := make([]string, 0, 2)
	if fileExists(zshrc) {
		paths = append(paths, zshrc)
	}
	if fileExists(bashrc) {
		paths = append(paths, bashrc)
	}
	if len(paths) == 0 {
		paths = append(paths, zshrc)
	}

	line, err := shellPathLine()
	if err != nil {
		return err
	}

	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("could not read %s: %w", path, err)
		}
		if strings.Contains(string(content), line) {
			continue
		}

		block := fmt.Sprintf("\n# vcSentinel\n%s\n", line)
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			return fmt.Errorf("could not update %s: %w", path, err)
		}
		if _, err := f.WriteString(block); err != nil {
			f.Close()
			return fmt.Errorf("could not write to %s: %w", path, err)
		}
		f.Close()
	}

	return nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// IsInitialized reports whether the worktree has the per-project
// configuration (.vcsentinel/vcsentinel.yml), that is, whether it already
// went through 'vcsentinel init'.
func IsInitialized(worktreePath string) bool {
	path := filepath.Join(worktreePath, ".vcsentinel", "vcsentinel.yml")
	return fileExists(path)
}

// createGlobalConfig materializes the global configuration file in
// ~/.vcsentinel/vcsentinel.yml if it does not exist yet. It does not
// overwrite it.
func createGlobalConfig() error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("could not identify the user's home directory: %w", err)
	}

	dir := filepath.Join(homeDir, ".vcsentinel")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("could not create the directory %s: %w", dir, err)
	}

	path := filepath.Join(dir, "vcsentinel.yml")
	if fileExists(path) {
		return nil
	}
	if err := os.WriteFile(path, []byte(globalConfigTemplate), 0644); err != nil {
		return fmt.Errorf("could not create the global configuration file %s: %w", path, err)
	}
	return nil
}

// CreatePerProjectConfig materializes the per-project configuration file in
// <worktree>/.vcsentinel/vcsentinel.yml if it does not exist yet. It does
// not overwrite it.
func CreatePerProjectConfig(worktreePath string) error {
	path := filepath.Join(worktreePath, ".vcsentinel", "vcsentinel.yml")
	if fileExists(path) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("could not create the directory %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(perProjectConfigTemplate), 0644); err != nil {
		return fmt.Errorf("could not create the per-project configuration file %s: %w", path, err)
	}
	return nil
}

func moveAndReplace(source string, destination string) error {
	if err := os.Rename(source, destination); err == nil {
		return nil
	}
	content, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	if err := os.WriteFile(destination, content, 0755); err != nil {
		return err
	}
	return os.Remove(source)
}
