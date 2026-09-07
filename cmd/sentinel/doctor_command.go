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

// runDoctor runs the advisory preflight and always exits 0: findings
// are warnings in the report, never process failures.
func runDoctor(w io.Writer, worktreePath, currentVersion string, args []string) int {
	checkUpdates, err := parseDoctorArgs(args)
	if err != nil {
		fmt.Fprintln(w, err)
		return 1
	}
	return runDoctorWith(w, worktreePath, currentVersion, checkUpdates, productionDoctorEnv(worktreePath))
}

// runDoctorWith renders one preflight report. The environment is a
// parameter (following runCheckWith) so tests never probe real agents.
func runDoctorWith(w io.Writer, worktreePath, currentVersion string, checkUpdates bool, env doctor.Env) int {
	if env.Probe == nil {
		env.Probe = productionDoctorEnv(worktreePath).Probe
	}
	rep := doctor.Run(worktreePath, doctor.Options{CurrentVersion: currentVersion, CheckUpdates: checkUpdates, Env: env})
	fmt.Fprint(w, rep.Text())
	return 0
}

// productionDoctorEnv wires the live probe: each configured agent answers
// the minimal prompt through the adapter built for its kind. A call still
// running when the budget expires is a ProbeTimeout, not the adapter's error:
// provider refusals arrive fast, so anything slower than the bound means the
// agent is slow or wedged. The late answer is discarded; the adapter's own
// internal budget bounds the abandoned call.
func productionDoctorEnv(worktreePath string) doctor.Env {
	return doctor.Env{
		Probe: func(agent, prompt string) (string, error) {
			cfg := config.LoadLocalConfig(worktreePath)
			ad, err := agentadapter.ProbeAdapterFor(cfg, agent, doctorProbeBudget)
			if err != nil {
				return "", err
			}
			type outcome struct {
				answer string
				err    error
			}
			done := make(chan outcome, 1)
			go func() {
				answer, err := ad.RunPrompt(prompt)
				done <- outcome{answer, err}
			}()
			select {
			case r := <-done:
				return r.answer, r.err
			case <-time.After(doctorProbeBudget):
				return "", &doctor.ProbeTimeout{Agent: agent, Budget: doctorProbeBudget}
			}
		},
	}
}
