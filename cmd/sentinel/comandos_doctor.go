package main

import (
	"fmt"
	"io"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentadapter"
	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
	"github.com/ISeoane-Quental/vas.sentinel/internal/doctor"
)

// doctorProbeBudget bounds each agent probe: long enough for a healthy agent
// to answer one word, short enough to fail fast instead of burning the
// ten-minute review budget the command exists to protect.
const doctorProbeBudget = 60 * time.Second

// parseDoctorArgs accepts only --check-updates. Any other argument is a
// rejection so a mistyped flag never looks accepted.
func parseDoctorArgs(args []string) (bool, error) {
	checkUpdates := false
	for _, arg := range args {
		switch arg {
		case "--check-updates":
			checkUpdates = true
		default:
			return false, fmt.Errorf("sentinel doctor accepts --check-updates, received %q", arg)
		}
	}
	return checkUpdates, nil
}

// ejecutarDoctor runs the advisory preflight and always exits 0: findings
// are warnings in the report, never process failures.
func ejecutarDoctor(w io.Writer, worktreePath, currentVersion string, args []string) int {
	checkUpdates, err := parseDoctorArgs(args)
	if err != nil {
		fmt.Fprintln(w, err)
		return 1
	}
	return ejecutarDoctorCon(w, worktreePath, currentVersion, checkUpdates, productionDoctorEnv(worktreePath))
}

// ejecutarDoctorCon renders one preflight report. The environment is a
// parameter (following ejecutarCheckCon) so tests never probe real agents.
func ejecutarDoctorCon(w io.Writer, worktreePath, currentVersion string, checkUpdates bool, env doctor.Env) int {
	if env.Probe == nil {
		env.Probe = productionDoctorEnv(worktreePath).Probe
	}
	rep := doctor.Run(worktreePath, doctor.Options{CurrentVersion: currentVersion, CheckUpdates: checkUpdates, Env: env})
	fmt.Fprint(w, rep.Text())
	return 0
}

// productionDoctorEnv wires the live probe: each configured agent answers
// the minimal prompt through the adapter built for its kind.
func productionDoctorEnv(worktreePath string) doctor.Env {
	return doctor.Env{
		Probe: func(agent, prompt string) (string, error) {
			cfg := config.CargarConfiguracionLocal(worktreePath)
			ad, err := agentadapter.ProbeAdapterFor(cfg, agent, doctorProbeBudget)
			if err != nil {
				return "", err
			}
			return ad.EjecutarPrompt(prompt)
		},
	}
}
