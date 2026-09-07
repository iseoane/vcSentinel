package main

// Test helper for internal/agentadapter: sleeps for the first numeric
// argument, in seconds (0 if absent), and responds according to the
// invocation mode:
//
//   - "-p <prompt>" prints the prompt received as an argument (legacy
//     transport compatibility);
//   - "-p" WITHOUT an argument, or followed by another flag ("--something"),
//     reads the full prompt from stdin and prints it (claude's stdin mode,
//     including the isolated review/commit variants that add more flags
//     after "-p", e.g. "-p --safe-mode --tools ...");
//   - without "-p" (e.g. opencode's "run") it also reads the prompt from
//     stdin.
//
// Argument mode wins over stdin: if "-p" carries a value that does NOT
// start with "-" and there is also stdin input, the argument is printed so
// that the tests can tell which transport the adapter used.
//
// It verifies the CLI adapter's timeout and prompt transport without
// depending on a real agent.

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

func main() {
	segundos := 0
	posicionP := -1
	for i, arg := range os.Args[1:] {
		if n, err := strconv.Atoi(arg); err == nil {
			segundos = n
			break
		}
		if arg == "-p" {
			posicionP = i + 1
		}
	}
	// VAS_SENTINEL_TEST_SLEEP forces the wait when the adapter builds the
	// arguments and there is no slot for the numeric argument (review mode:
	// the flags are set by reviewCommand).
	if espera := os.Getenv("VAS_SENTINEL_TEST_SLEEP"); espera != "" {
		if n, err := strconv.Atoi(espera); err == nil {
			segundos = n
		}
	}
	time.Sleep(time.Duration(segundos) * time.Second)

	prompt := ""
	if posicionP >= 0 && posicionP < len(os.Args)-1 && !strings.HasPrefix(os.Args[posicionP+1], "-") {
		prompt = os.Args[posicionP+1]
	} else if datos, err := io.ReadAll(os.Stdin); err == nil {
		prompt = string(datos)
	}

	if ruta := os.Getenv("VAS_SENTINEL_TEST_CAPTURE"); ruta != "" {
		dir, _ := os.Getwd()
		datos, _ := json.Marshal(struct {
			Args  []string `json:"args"`
			Dir   string   `json:"dir"`
			Stdin string   `json:"stdin"`
		}{Args: os.Args[1:], Dir: dir, Stdin: prompt})
		_ = os.WriteFile(ruta, datos, 0600)
	}
	// VAS_SENTINEL_TEST_FAIL simulates an agent that fails with an error
	// message on stderr, to check that the adapter captures and propagates
	// that detail instead of discarding it.
	if fallo := os.Getenv("VAS_SENTINEL_TEST_FAIL"); fallo != "" {
		fmt.Fprint(os.Stderr, fallo)
		os.Exit(1)
	}
	if salida := os.Getenv("VAS_SENTINEL_TEST_OUTPUT"); salida != "" {
		fmt.Print(salida)
		return
	}
	fmt.Print(prompt)
}
