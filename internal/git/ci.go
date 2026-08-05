package git

import (
	"path/filepath"
)

// rutasCI son los marcadores de integración continua reconocidos. El glob de
// GitHub Actions cubre .github/workflows/*.yml y *.yaml; Jenkinsfile es un
// glob literal (sin comodines) que coincide con el archivo exacto.
var rutasCI = []string{
	".github/workflows/*.yml",
	".github/workflows/*.yaml",
	".gitlab-ci.yml",
	".circleci/config.yml",
	".azure-pipelines.yml",
	"Jenkinsfile",
}

// DetectarCI indica si el worktree tiene configuración de integración continua
// (GitHub Actions, GitLab CI, CircleCI, Azure Pipelines o Jenkins). Es la
// señal del aviso de "CI ausente" de la verificación dual.
func DetectarCI(worktree string) bool {
	for _, patron := range rutasCI {
		coincidencias, err := filepath.Glob(filepath.Join(worktree, patron))
		if err == nil && len(coincidencias) > 0 {
			return true
		}
	}
	return false
}
