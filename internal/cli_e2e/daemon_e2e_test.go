package cli_e2e

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"
)

const (
	cliDaemonReadyTimeout   = 10 * time.Second
	cliDaemonExitTimeout    = 10 * time.Second
	cliDaemonCleanupTimeout = 5 * time.Second
	cliDaemonNotRunningText = "📭 No daemon is running for this repository.\n"
)

// TestCLIRunsDaemonProcessLifecycle exercises the public daemon lifecycle as
// separate processes. The start process owns only the disposable repository
// prepared by cliRunner; status and stop are independent public invocations.
func TestCLIRunsDaemonProcessLifecycle(t *testing.T) {
	runner := newCLIRunner(t)
	before := captureCLIExternalState(t, runner)
	defer assertCLIExternalStateUnchanged(t, runner, before)

	seedCLIRepository(t, runner, "test: seed daemon fixture")
	runPublicInit(t, runner)
	stageRunsFakeAgent(t, runner)
	writeProjectConfig(t, runner, runsPublicConfig())

	daemon := startCLIDaemonProcess(t, runner)
	readyContext, cancelReady := context.WithTimeout(context.Background(), cliDaemonReadyTimeout)
	readyLine, readyErr := daemon.waitForReady(readyContext)
	cancelReady()
	if readyErr != nil {
		if cleanupErr := daemon.terminate(); cleanupErr != nil {
			t.Fatalf("daemon start failed and cleanup failed: %v\n%s", cleanupErr, daemon.diagnostic())
		}
		diagnostic := daemon.diagnostic()
		if cliDaemonTransportUnavailable(diagnostic) {
			t.Skipf("repository-local daemon transport unavailable on this platform: %s", strings.TrimSpace(diagnostic))
		}
		t.Fatalf("daemon did not publish readiness: %v\n%s", readyErr, diagnostic)
	}

	endpoint := cliDaemonEndpointFromReadyLine(t, readyLine)
	status := runner.run("runs", "daemon", "status")
	if status.ExitCode != 0 {
		t.Fatalf("public daemon status failed:\n%s", status.Diagnostic())
	}
	statusText := status.Stdout + "\n" + status.Stderr
	for _, vocabulary := range []string{
		"daemon for this repository",
		"pid ",
		"started ",
		"host ",
		"transport ",
	} {
		if !strings.Contains(strings.ToLower(statusText), vocabulary) {
			t.Fatalf("public daemon status omitted live/transport vocabulary %q:\n%s", vocabulary, status.Diagnostic())
		}
	}
	if !strings.Contains(statusText, endpoint) {
		t.Fatalf("public daemon status omitted the ready endpoint %q:\n%s", endpoint, status.Diagnostic())
	}

	stop := runner.run("runs", "daemon", "stop")
	if stop.ExitCode != 0 {
		t.Fatalf("public daemon stop failed:\n%s", stop.Diagnostic())
	}
	stopText := strings.ToLower(stop.Stdout + "\n" + stop.Stderr)
	for _, vocabulary := range []string{"stopped", "orphaned"} {
		if !strings.Contains(stopText, vocabulary) {
			t.Fatalf("public daemon stop omitted vocabulary %q:\n%s", vocabulary, stop.Diagnostic())
		}
	}

	exitContext, cancelExit := context.WithTimeout(context.Background(), cliDaemonExitTimeout)
	exitErr := daemon.waitForExit(exitContext)
	cancelExit()
	if exitErr != nil {
		_ = daemon.terminate()
		t.Fatalf("daemon start process did not exit cleanly: %v\n%s", exitErr, daemon.diagnostic())
	}
	outputContext, cancelOutput := context.WithTimeout(context.Background(), cliDaemonCleanupTimeout)
	if outputErr := daemon.waitForOutput(outputContext); outputErr != nil {
		cancelOutput()
		t.Fatalf("daemon start output was not drained: %v\n%s", outputErr, daemon.diagnostic())
	}
	cancelOutput()

	statusAfterStop := runner.run("runs", "daemon", "status")
	assertCLIDaemonNotRunning(t, "status after stop", statusAfterStop)
	stopAfterStop := runner.run("runs", "daemon", "stop")
	assertCLIDaemonNotRunning(t, "stop after stop", stopAfterStop)
}

// cliDaemonProcess owns exactly the process started by the test. Its readers
// keep both pipes drained while the daemon is alive, preventing a child-side
// write from blocking readiness or shutdown.
type cliDaemonProcess struct {
	command    *exec.Cmd
	ready      chan string
	waitDone   chan struct{}
	waitErr    error
	stdoutDone chan struct{}
	stderrDone chan struct{}
	stdout     bytes.Buffer
	stderr     bytes.Buffer
}

func startCLIDaemonProcess(t *testing.T, runner *cliRunner) *cliDaemonProcess {
	t.Helper()

	command := exec.Command(runner.binary, "runs", "daemon", "start")
	command.Dir = runner.repository
	command.Env = append([]string(nil), runner.env...)
	stdoutPipe, err := command.StdoutPipe()
	if err != nil {
		t.Fatalf("create daemon stdout pipe: %v", err)
	}
	stderrPipe, err := command.StderrPipe()
	if err != nil {
		_ = stdoutPipe.Close()
		t.Fatalf("create daemon stderr pipe: %v", err)
	}
	if err := command.Start(); err != nil {
		_ = stdoutPipe.Close()
		_ = stderrPipe.Close()
		t.Fatalf("start public daemon process: %v", err)
	}

	process := &cliDaemonProcess{
		command:    command,
		ready:      make(chan string, 1),
		waitDone:   make(chan struct{}),
		stdoutDone: make(chan struct{}),
		stderrDone: make(chan struct{}),
	}
	go process.readStdout(stdoutPipe)
	go process.readStderr(stderrPipe)
	go func() {
		process.waitErr = command.Wait()
		close(process.waitDone)
	}()

	t.Cleanup(func() {
		if err := process.terminate(); err != nil {
			t.Errorf("clean up daemon child process: %v\n%s", err, process.diagnostic())
		}
	})
	return process
}

func (p *cliDaemonProcess) readStdout(pipe io.Reader) {
	defer close(p.stdoutDone)
	scanner := bufio.NewScanner(pipe)
	scanner.Buffer(make([]byte, 1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		p.stdout.WriteString(line)
		p.stdout.WriteByte('\n')
		if strings.Contains(strings.ToLower(line), "daemon ready") {
			select {
			case p.ready <- line:
			default:
			}
		}
	}
}

func (p *cliDaemonProcess) readStderr(pipe io.Reader) {
	defer close(p.stderrDone)
	_, _ = io.Copy(&p.stderr, pipe)
}

func (p *cliDaemonProcess) waitForReady(ctx context.Context) (string, error) {
	select {
	case line := <-p.ready:
		return line, nil
	case <-p.waitDone:
		// Prefer a readiness line that raced with process completion before
		// treating the exit as a startup failure.
		select {
		case line := <-p.ready:
			return line, nil
		default:
		}
		_ = p.waitForOutput(context.Background())
		return "", fmt.Errorf("daemon start exited before readiness: %v", p.waitErr)
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (p *cliDaemonProcess) waitForExit(ctx context.Context) error {
	if err := p.awaitCompletion(ctx); err != nil {
		return err
	}
	return p.waitErr
}

func (p *cliDaemonProcess) awaitCompletion(ctx context.Context) error {
	select {
	case <-p.waitDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *cliDaemonProcess) waitForOutput(ctx context.Context) error {
	select {
	case <-p.stdoutDone:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-p.stderrDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *cliDaemonProcess) terminate() error {
	select {
	case <-p.waitDone:
	default:
		if p.command.Process != nil {
			_ = p.command.Process.Kill()
		}
	}
	cleanupContext, cancel := context.WithTimeout(context.Background(), cliDaemonCleanupTimeout)
	defer cancel()
	if err := p.awaitCompletion(cleanupContext); err != nil {
		return fmt.Errorf("wait for daemon child termination: %w", err)
	}
	if err := p.waitForOutput(cleanupContext); err != nil {
		return fmt.Errorf("drain daemon child output: %w", err)
	}
	return nil
}

func (p *cliDaemonProcess) diagnostic() string {
	return fmt.Sprintf("stdout:\n%s\nstderr:\n%s", p.stdout.String(), p.stderr.String())
}

func cliDaemonEndpointFromReadyLine(t *testing.T, line string) string {
	t.Helper()
	marker := "daemon ready:"
	lower := strings.ToLower(line)
	index := strings.Index(lower, marker)
	if index < 0 {
		t.Fatalf("daemon readiness line %q does not contain %q", line, marker)
	}
	fields := strings.Fields(line[index+len(marker):])
	if len(fields) == 0 || !strings.Contains(fields[0], "://") {
		t.Fatalf("daemon readiness line %q does not contain a transport endpoint", line)
	}
	return fields[0]
}

func assertCLIDaemonNotRunning(t *testing.T, operation string, result commandResult) {
	t.Helper()
	if result.ExitCode != 2 {
		t.Fatalf("public daemon %s exit code = %d, want 2:\n%s", operation, result.ExitCode, result.Diagnostic())
	}
	if result.Stdout != cliDaemonNotRunningText || result.Stderr != "" {
		t.Fatalf("public daemon %s output = (%q, %q), want deterministic no-daemon output %q", operation, result.Stdout, result.Stderr, cliDaemonNotRunningText)
	}
}

func cliDaemonTransportUnavailable(diagnostic string) bool {
	return strings.Contains(strings.ToLower(diagnostic), "cannot bind the repository endpoint")
}
