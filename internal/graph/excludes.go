package graph

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// resolveGlobalExcludes resolves, in the parent process where the environment
// is intact, the effective global excludes file whose absence in the sanitized
// child makes globally-ignored paths read as untracked. It returns an absolute
// path, or "" when no effective file exists. Only existence is reported: a
// configured-but-missing file behaves in the parent exactly as no file at all,
// so there is nothing to pass down.
//
// The result travels to the child as an explicit "-c core.excludesFile=<path>"
// argument, the same resolve-outside/pass-explicit-value shape as the
// interpreter-directory fix. No parent variable reaches the child.
func resolveGlobalExcludes(gitBin string) string {
	return selectGlobalExcludes(readExcludesConfig(gitBin), parentHome(), parentXDG())
}

// readExcludesConfig reads the user's core.excludesFile setting with the
// parent environment intact. An unset key exits 1, which simply means "".
func readExcludesConfig(gitBin string) string {
	output, err := exec.Command(gitBin, "config", "--get", "core.excludesFile").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func parentHome() string {
	if home, err := os.UserHomeDir(); err == nil {
		return home
	}
	return ""
}

func parentXDG() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return xdg
	}
	return ""
}

// selectGlobalExcludes picks the effective file: the configured value with a
// leading ~ expanded against the parent home (the child has no HOME, so it
// could not expand it), or otherwise the default global ignore location. A
// configured path that does not exist, and a ~other-user path no lookup can
// resolve, both yield "": the parent git ignores them the same way. The
// repository-local .git/info/exclude needs no handling here: git finds it via
// the git dir, which the child resolves on its own.
func selectGlobalExcludes(configured, home, xdg string) string {
	if configured != "" {
		return excludesIfExists(expandExcludes(configured, home))
	}
	base := xdg
	if base == "" {
		if home == "" {
			return ""
		}
		base = filepath.Join(home, ".config")
	}
	return excludesIfExists(filepath.Join(base, "git", "ignore"))
}

func expandExcludes(path, home string) string {
	if path == "~" {
		return home
	}
	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		if home == "" {
			return ""
		}
		return filepath.Join(home, rest)
	}
	if strings.HasPrefix(path, "~") {
		return ""
	}
	if filepath.IsAbs(path) {
		return path
	}
	if home == "" {
		return ""
	}
	return filepath.Join(home, path)
}

// argsStatePorcelain returns the status invocation for the sanitized child:
// the plain call when no global excludes file resolves, or the same call with
// the parent-resolved file passed as an explicit -c so the child shares the
// parent's notion of dirty. A nil resolver keeps the call unchanged.
func argsStatePorcelain(resolver func(string) string, gitBin string) []string {
	base := []string{"status", "--porcelain"}
	if resolver == nil {
		return base
	}
	if excludes := resolver(gitBin); excludes != "" {
		return []string{"-c", "core.excludesFile=" + excludes, "status", "--porcelain"}
	}
	return base
}

func excludesIfExists(path string) string {
	if path == "" {
		return ""
	}
	if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
		return ""
	}
	return path
}
