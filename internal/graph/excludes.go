package graph

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// buscarExcludesGlobal resolves, in the parent process where the environment
// is intact, the effective global excludes file whose absence in the sanitized
// child makes globally-ignored paths read as untracked. It returns an absolute
// path, or "" when no effective file exists. Only existence is reported: a
// configured-but-missing file behaves in the parent exactly as no file at all,
// so there is nothing to pass down.
//
// The result travels to the child as an explicit "-c core.excludesFile=<path>"
// argument, the same resolve-outside/pass-explicit-value shape as the
// interpreter-directory fix. No parent variable reaches the child.
func buscarExcludesGlobal(gitBin string) string {
	return seleccionarExcludes(leerConfigExcludes(gitBin), casaPadre(), xdgPadre())
}

// leerConfigExcludes reads the user's core.excludesFile setting with the
// parent environment intact. An unset key exits 1, which simply means "".
func leerConfigExcludes(gitBin string) string {
	salida, err := exec.Command(gitBin, "config", "--get", "core.excludesFile").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(salida))
}

func casaPadre() string {
	if home, err := os.UserHomeDir(); err == nil {
		return home
	}
	return ""
}

func xdgPadre() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return xdg
	}
	return ""
}

// seleccionarExcludes picks the effective file: the configured value with a
// leading ~ expanded against the parent home (the child has no HOME, so it
// could not expand it), or otherwise the default global ignore location. A
// configured path that does not exist, and a ~other-user path no lookup can
// resolve, both yield "": the parent git ignores them the same way. The
// repository-local .git/info/exclude needs no handling here: git finds it via
// the git dir, which the child resolves on its own.
func seleccionarExcludes(configurado, home, xdg string) string {
	if configurado != "" {
		return existeExcludes(expandirExcludes(configurado, home))
	}
	base := xdg
	if base == "" {
		if home == "" {
			return ""
		}
		base = filepath.Join(home, ".config")
	}
	return existeExcludes(filepath.Join(base, "git", "ignore"))
}

func expandirExcludes(ruta, home string) string {
	if ruta == "~" {
		return home
	}
	if resto, ok := strings.CutPrefix(ruta, "~/"); ok {
		if home == "" {
			return ""
		}
		return filepath.Join(home, resto)
	}
	if strings.HasPrefix(ruta, "~") {
		return ""
	}
	if filepath.IsAbs(ruta) {
		return ruta
	}
	if home == "" {
		return ""
	}
	return filepath.Join(home, ruta)
}

// argsEstadoPorcelain returns the status invocation for the sanitized child:
// the plain call when no global excludes file resolves, or the same call with
// the parent-resolved file passed as an explicit -c so the child shares the
// parent's notion of dirty. A nil resolver keeps the call unchanged.
func argsEstadoPorcelain(resolver func(string) string, gitBin string) []string {
	base := []string{"status", "--porcelain"}
	if resolver == nil {
		return base
	}
	if excluye := resolver(gitBin); excluye != "" {
		return []string{"-c", "core.excludesFile=" + excluye, "status", "--porcelain"}
	}
	return base
}

func existeExcludes(ruta string) string {
	if ruta == "" {
		return ""
	}
	if info, err := os.Stat(ruta); err != nil || !info.Mode().IsRegular() {
		return ""
	}
	return ruta
}
