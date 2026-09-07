package git

import (
	"path/filepath"
)

// ciPaths lists the recognized continuous-integration markers. The
// GitHub Actions glob covers .github/workflows/*.yml and *.yaml; Jenkinsfile
// is a literal glob (no wildcards) that matches the exact file.
var ciPaths = []string{
	".github/workflows/*.yml",
	".github/workflows/*.yaml",
	".gitlab-ci.yml",
	".circleci/config.yml",
	".azure-pipelines.yml",
	"Jenkinsfile",
}

// DetectCI reports whether the worktree has continuous-integration
// configuration (GitHub Actions, GitLab CI, CircleCI, Azure Pipelines, or
// Jenkins). It is the signal behind the "CI missing" warning of the dual
// verification.
func DetectCI(worktree string) bool {
	for _, pattern := range ciPaths {
		matches, err := filepath.Glob(filepath.Join(worktree, pattern))
		if err == nil && len(matches) > 0 {
			return true
		}
	}
	return false
}
