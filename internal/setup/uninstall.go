package setup

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// repositoryHooksNotice is the uninstall disclaimer pinned by tests: the
// repository pre-commit hook installed by init is Git-managed state and stays
// out of uninstall's blast radius.
const repositoryHooksNotice = "   The pre-commit hook installed in each repository (.git/hooks/pre-commit) is not removed: it manages git hooks, not vcSentinel hooks."

// RunFullUninstall undoes the global installation: removes the binary, takes
// the directory off the user PATH, cleans the global configuration and
// restores the shell files. It does not touch per-project configurations.
func RunFullUninstall() error {
	fmt.Println("🗑️ Uninstalling vcSentinel...")

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("could not identify the user's home directory: %w", err)
	}

	switch runtime.GOOS {
	case "windows":
		if err := uninstallWindows(homeDir); err != nil {
			return err
		}
	default:
		if err := uninstallLinux(); err != nil {
			return err
		}
	}

	if err := removeGlobalConfig(homeDir); err != nil {
		return err
	}

	if err := removeShellPathBlock(); err != nil {
		return err
	}

	fmt.Println("✅ vcSentinel uninstalled.")
	fmt.Println(repositoryHooksNotice)
	return nil
}

func uninstallWindows(homeDir string) error {
	dir := filepath.Join(homeDir, ".vcsentinel", "bin")
	destination := windowsBinaryPath(homeDir)

	if err := removeBinary(destination); err != nil {
		return err
	}

	if err := removeWindowsPath(dir); err != nil {
		return err
	}

	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("could not remove the directory %s: %w", dir, err)
	}
	return nil
}

func uninstallLinux() error {
	if err := removeBinary(linuxBinaryPath()); err != nil {
		return err
	}
	return nil
}

// removeBinary deletes the binary if it exists. If it is in use (typical on
// Windows), it suggests the manual command.
func removeBinary(path string) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		fmt.Printf("ℹ️ No binary was found at %s. Continuing with the rest.\n", path)
		return nil
	}

	if err := os.Remove(path); err != nil {
		if runtime.GOOS == "windows" {
			return fmt.Errorf("could not remove %s (it is probably in use). Close the process and delete the file manually: %w", path, err)
		}
		return fmt.Errorf("could not remove %s: %w", path, err)
	}
	fmt.Printf("🗑️ Binary removed: %s\n", path)
	return nil
}

// removeWindowsPath removes the directory from the user PATH using
// PowerShell, in the same format it was added with.
func removeWindowsPath(dir string) error {
	currentPath, err := windowsUserPath()
	if err != nil {
		return err
	}
	if !needsWindowsPathUpdate(currentPath, dir) {
		return nil
	}

	command := fmt.Sprintf("$env:Path = (($env:Path -split ';') | Where-Object { $_ -ne '%s' }) -join ';'; [Environment]::SetEnvironmentVariable('Path', $env:Path, 'User')", dir)
	cmd := exec.Command("powershell", "-NoProfile", "-Command", command)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("could not remove %s from the user PATH: %w", dir, err)
	}

	fmt.Printf("🛣️ Path removed from the user PATH: %s\n", dir)
	return nil
}

// removeShellPathBlock removes the /usr/local/bin export block from the shell
// files it was added to.
func removeShellPathBlock() error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("could not identify the user's home directory: %w", err)
	}

	zshrc := filepath.Join(homeDir, ".zshrc")
	bashrc := filepath.Join(homeDir, ".bashrc")

	const line = `export PATH="/usr/local/bin:$PATH"`
	block := "\n# vcSentinel\n" + line + "\n"

	for _, path := range []string{zshrc, bashrc} {
		content, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("could not read %s: %w", path, err)
		}
		text := string(content)
		if !strings.Contains(text, block) {
			continue
		}

		clean := strings.ReplaceAll(text, block, "")
		if err := os.WriteFile(path, []byte(clean), 0644); err != nil {
			return fmt.Errorf("could not update %s: %w", path, err)
		}
		fmt.Printf("🗑️ vcSentinel block removed from: %s\n", path)
	}

	return nil
}

// removeGlobalConfig deletes the global configuration and the .vcsentinel
// directory if it was left empty.
func removeGlobalConfig(homeDir string) error {
	dir := filepath.Join(homeDir, ".vcsentinel")
	path := filepath.Join(dir, "vcsentinel.yml")

	if _, err := os.Stat(path); err == nil {
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("could not remove the global configuration %s: %w", path, err)
		}
		fmt.Printf("🗑️ Global configuration removed: %s\n", path)
	}

	if entries, err := os.ReadDir(dir); err == nil && len(entries) == 0 {
		if err := os.Remove(dir); err != nil {
			return fmt.Errorf("could not remove the directory %s: %w", dir, err)
		}
	}
	return nil
}
