package main

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
)

// ejecutarPr crea un pull request con gh. Antes de crear el PR limpia las
// fichas de auditoría huérfanas (commits reescritos por rebase/amend/squash):
// un PR nuevo sobre main solo debería cargar auditorías que siguen vivas.
func ejecutarPr(worktree string, args []string) {
	if _, err := exec.LookPath("gh"); err != nil {
		fmt.Println("? gh (GitHub CLI) no está en el PATH. Instálalo o crea el PR manualmente.")
		os.Exit(1)
	}

	gitDir, err := git.ObtenerGitDir()
	if err != nil {
		fmt.Printf("? %v\n", err)
		os.Exit(1)
	}
	eliminados, err := purgarHuerfanas(gitDir)
	if err != nil {
		fmt.Printf("? No se pudieron purgar fichas huérfanas: %v\n", err)
		os.Exit(1)
	}
	if len(eliminados) == 0 {
		fmt.Println("? Limpieza previa: no hay fichas huérfanas.")
	} else {
		fmt.Printf("? Limpieza previa: eliminadas %d fichas de commits que ya no existen.\n", len(eliminados))
		for _, sha := range eliminados {
			fmt.Printf("  - %s\n", sha)
		}
	}

	cmdArgs := append([]string{"pr", "create"}, args...)
	cmd := exec.Command("gh", cmdArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Printf("? gh pr create terminó con error (código %d).\n", exitCodeDeError(err))
		os.Exit(1)
	}
}
