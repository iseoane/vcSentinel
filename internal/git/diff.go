package git

import (
	"bytes"
	"os/exec"
	"strings"
)

func CheckDiffLimits() (int, string, error) {
	cmd := exec.Command("git", "diff", "HEAD")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return 0, "ERROR", err
	}

	lines := strings.Split(out.String(), "\n")
	addedLinesCount := 0

	for _, line := range lines {
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			addedLinesCount++
		}
	}

	if addedLinesCount >= 200 && addedLinesCount <= 400 {
		return addedLinesCount, "PUNTO_OPTIMO", nil
	} else if addedLinesCount > 400 {
		return addedLinesCount, "CRITICO", nil
	}
	return addedLinesCount, "PEQUENO", nil
}
