package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/ISeoane-Quental/vcSentinel/internal/change"
	"github.com/ISeoane-Quental/vcSentinel/internal/git"
)

type stagedCheckReport struct {
	Scope              string `json:"scope"`
	Worktree           string `json:"worktree"`
	AuthoredLines      int    `json:"authored_lines"`
	InformationalLines int    `json:"informational_lines"`
	State              string `json:"state"`
	Accepted           bool   `json:"accepted"`
	Rejected           bool   `json:"rejected"`
	MeasurementFailed  bool   `json:"measurement_failed"`
	CohesionClusters   int    `json:"cohesion_clusters,omitempty"`
	Warning            string `json:"warning,omitempty"`
	Recommendation     string `json:"recommendation,omitempty"`
	Error              string `json:"error,omitempty"`
}

type stagedVolumeMeasurement func() (git.PendingVolume, error)
type stagedCohesionMeasurement func([]string) (change.CohesionResult, error)

func runStagedCheck(path string, jsonOut bool) int {
	return runStagedCheckWith(os.Stdout, path, jsonOut, git.MeasureStagedVolume, computeStagedCohesion)
}

func runStagedCheckWith(
	w io.Writer,
	path string,
	jsonOut bool,
	measure stagedVolumeMeasurement,
	cohesion stagedCohesionMeasurement,
) int {
	volume, err := measure()
	if err != nil {
		report := stagedCheckReport{
			Scope:             "staged",
			Worktree:          path,
			State:             "ERROR",
			MeasurementFailed: true,
			Error:             err.Error(),
		}
		if jsonOut {
			return writeStagedCheckJSON(w, report)
		}
		fmt.Fprintf(w, "❌ Staged measurement failed: %v\n", err)
		return 1
	}

	report := stagedCheckReport{
		Scope:              "staged",
		Worktree:           path,
		AuthoredLines:      volume.Blocking,
		InformationalLines: volume.Informational,
		State:              volume.State,
		Accepted:           volume.Blocking <= git.ReviewableLinesLimit,
	}
	if !report.Accepted {
		report.Rejected = true
		report.Recommendation = "vcsentinel slice plan --json"
	}

	if len(volume.Paths) > 0 {
		cohesionResult, cohesionErr := cohesion(volume.Paths)
		switch {
		case cohesionErr != nil:
			report.Warning = fmt.Sprintf("Cohesion analysis was unavailable; staged volume enforcement still applies: %v", cohesionErr)
		case cohesionResult.SuggestSplit:
			report.CohesionClusters = cohesionResult.Clusters
			report.Warning = fmt.Sprintf(
				"Warning: the staged candidate contains %d independent cohesion clusters. Run `vcsentinel slice plan --json` to prepare a reviewable split; no files were staged or changed.",
				cohesionResult.Clusters,
			)
		}
	}

	if jsonOut {
		return writeStagedCheckJSON(w, report)
	}
	return printStagedCheck(w, report)
}

func printStagedCheck(w io.Writer, report stagedCheckReport) int {
	fmt.Fprintf(w, "Staged commit candidate: %d authored lines [%s]\n", report.AuthoredLines, report.State)
	if report.InformationalLines > 0 {
		fmt.Fprintf(w, "The candidate also contains %d informational lines; they do not count toward the review budget.\n", report.InformationalLines)
	}
	if report.Warning != "" {
		fmt.Fprintln(w, report.Warning)
	}
	if report.Rejected {
		fmt.Fprintf(w, "❌ Staged commit rejected: %d authored lines exceed the %d-line review budget.\n", report.AuthoredLines, git.ReviewableLinesLimit)
		fmt.Fprintf(w, "Run `%s` to prepare a reviewable split.\n", report.Recommendation)
		return 1
	}
	fmt.Fprintln(w, "✅ Staged commit candidate is within the review budget.")
	return 0
}

func writeStagedCheckJSON(w io.Writer, report stagedCheckReport) int {
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fmt.Fprintf(w, "❌ Could not serialize staged check report: %v\n", err)
		return 1
	}
	if _, err := fmt.Fprintln(w, string(data)); err != nil {
		return 1
	}
	if report.MeasurementFailed || report.Rejected {
		return 1
	}
	return 0
}

func computeStagedCohesion(paths []string) (change.CohesionResult, error) {
	return change.Cohesion(paths, func(args ...string) (string, error) {
		output, err := exec.Command("git", args...).Output()
		return string(output), err
	})
}
