package setup

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const windowsManualUpgradePackage = "github.com/ISeoane-Quental/vcSentinel/cmd/vcsentinel@latest"

func RunUpgradeFromGitHub() error {
	if err := validateSetupTestOverrides(); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		return reportWindowsManualUpgrade()
	}

	fallback, err := fallbackSetting()
	if err != nil {
		return err
	}

	fmt.Println("🔄 Looking for the latest version...")

	PrepareGitHubToken()

	release, err := fetchLatestRelease()
	if err != nil {
		if fallback {
			if errFallback := UpgradeViaGoInstall(); errFallback != nil {
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

	currentBinary, err := locateCurrentBinary()
	if err != nil {
		return err
	}

	fmt.Printf("⬇️ Downloading %s...\n", release.TagName)

	tmpFile, err := os.CreateTemp(filepath.Dir(currentBinary), upgradeTempPattern())
	if err != nil {
		return fmt.Errorf("could not create the temporary download file: %w", err)
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(tmpPath)

	if err := downloadBinary(asset, tmpPath); err != nil {
		if fallback {
			if errFallback := UpgradeViaGoInstall(); errFallback != nil {
				return fmt.Errorf("%v\nIn addition, the go install fallback failed: %v", err, errFallback)
			}
			return nil
		}
		return err
	}
	if err := validateStagedBinary(tmpPath); err != nil {
		return err
	}

	var backup string
	switch runtime.GOOS {
	case "windows":
		backup, err = replaceWindowsBinary(tmpPath, currentBinary)
	default:
		err = replaceLinuxBinary(tmpPath, currentBinary)
	}
	if err != nil {
		return err
	}

	if err := verifyBinary(currentBinary); err != nil {
		if backup != "" {
			return fmt.Errorf("the binary may be corrupt and your backup is at %s: %w", backup, err)
		}
		return fmt.Errorf("the binary may be corrupt: %w", err)
	}

	fmt.Printf("✅ Updated to %s\n", release.TagName)
	return nil
}

// UpgradeViaGoInstall is the upgrade fallback when the release download fails
// (for example, a private repository without a token). Builds the latest
// version with go install and replaces the running binary.
func UpgradeViaGoInstall() error {
	if runtime.GOOS == "windows" {
		return reportWindowsManualUpgrade()
	}

	fmt.Println("🔄 Download unavailable. Retrying with go install (builds from source)...")

	currentBinary, err := locateCurrentBinary()
	if err != nil {
		return err
	}

	output, err := runGoInstall()
	if err != nil {
		return fmt.Errorf("go install failed (are GOPRIVATE and git credentials configured?): %w\n%s", err, output)
	}

	gopath, err := goEnvGOPATH()
	if err != nil {
		return err
	}
	binary := filepath.Join(gopath, "bin", goBinaryName())
	if err := validateStagedBinary(binary); err != nil {
		return err
	}

	var backup string
	switch runtime.GOOS {
	case "windows":
		backup, err = replaceWindowsBinary(binary, currentBinary)
	default:
		err = replaceLinuxBinary(binary, currentBinary)
	}
	if err != nil {
		return err
	}

	if err := verifyBinary(currentBinary); err != nil {
		if backup != "" {
			return fmt.Errorf("the binary may be corrupt and your backup is at %s: %w", backup, err)
		}
		return fmt.Errorf("the binary may be corrupt: %w", err)
	}

	fmt.Println("✅ Updated via go install")
	return nil
}

func reportWindowsManualUpgrade() error {
	installDir, err := installRoot()
	if err != nil {
		return err
	}

	fmt.Println("⚠️ Automatic upgrade did not happen: Windows locks the running .exe, so vcSentinel cannot replace itself.")
	fmt.Println("This is a source-based manual upgrade and requires Go. Run this copy-pastable PowerShell command:")
	fmt.Println(renderWindowsManualUpgradeCommand(installDir))
	return fmt.Errorf("automatic upgrade did not happen on Windows: the running .exe is locked and cannot replace itself")
}

func renderWindowsManualUpgradeCommand(installDir string) string {
	quotedInstallDir := "'" + strings.ReplaceAll(installDir, "'", "''") + "'"
	return fmt.Sprintf("$previousGOBIN = $env:GOBIN; try { $env:GOBIN = %s; go install %s } finally { if ($null -eq $previousGOBIN) { Remove-Item Env:GOBIN -ErrorAction SilentlyContinue } else { $env:GOBIN = $previousGOBIN } }", quotedInstallDir, windowsManualUpgradePackage)
}

func upgradeTempPattern() string {
	if runtime.GOOS == "windows" {
		return "vcsentinel-upgrade-*.exe"
	}
	return "vcsentinel-upgrade-*"
}

func validateStagedBinary(path string) error {
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0755); err != nil {
			return fmt.Errorf("could not grant execution permissions to the downloaded binary: %w", err)
		}
	}
	if err := verifyBinary(path); err != nil {
		return fmt.Errorf("the downloaded binary failed validation: %w", err)
	}
	return nil
}

func locateCurrentBinary() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("could not locate the running binary: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		return resolved, nil
	}
	return exe, nil
}

func replaceWindowsBinary(tmpPath string, currentBinary string) (string, error) {
	backupPath := currentBinary + ".old"
	if err := os.Rename(currentBinary, backupPath); err != nil {
		return "", fmt.Errorf("could not back up the current binary to %s: %w", backupPath, err)
	}

	if err := os.Rename(tmpPath, currentBinary); err != nil {
		os.Rename(backupPath, currentBinary)
		return "", fmt.Errorf("could not install the new version at %s: %w", currentBinary, err)
	}

	if err := os.Remove(backupPath); err != nil {
		fmt.Printf("⚠️ Could not remove the backup %s (it is locked). You can delete it manually: %v\n", backupPath, err)
	}
	return backupPath, nil
}

func replaceLinuxBinary(tmpPath string, currentBinary string) error {
	if err := os.Rename(tmpPath, currentBinary); err != nil {
		return fmt.Errorf("could not replace the current binary at %s: %w", currentBinary, err)
	}
	if err := os.Chmod(currentBinary, 0755); err != nil {
		return fmt.Errorf("could not grant execution permissions to the new binary: %w", err)
	}
	return nil
}

func verifyBinary(currentBinary string) error {
	cmd := exec.Command(currentBinary, "--version")
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("the new binary did not answer --version: %w", err)
	}
	version := strings.TrimSpace(string(out))
	if version != "" {
		fmt.Printf("ℹ️ New installed version: %s\n", version)
	}
	return nil
}
