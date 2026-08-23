//go:build linux

// Linux implementation of the process ownership seam: every owned child is
// born into its own process group (setpgid), so whole-tree termination is a
// signal pair against the negative pgid. Debian is one of the two supported
// platforms; there is no best-effort fallback here.
package process

import (
	"os"
	"os/exec"
	"syscall"
	"time"
)

const defaultKillDelay = 300 * time.Millisecond

type linuxOwner struct {
	config OwnerConfig
}

func newPlatformOwner(config OwnerConfig) Owner {
	return &linuxOwner{config: config}
}

// Assign makes the child its own group leader before it starts, so its pid is
// also the pgid every descendant inherits for its whole lifetime.
func (l *linuxOwner) Assign(cmd *exec.Cmd) (*Tree, error) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	return &Tree{exited: make(chan struct{}), platform: linuxTree{killDelay: l.killDelay()}}, nil
}

func (l *linuxOwner) Terminate(tree *Tree) error { return Terminate(tree) }

func (l *linuxOwner) killDelay() time.Duration {
	if l.config.KillDelay > 0 {
		return l.config.KillDelay
	}
	return defaultKillDelay
}

type linuxTree struct {
	killDelay time.Duration
}

func (linuxTree) attach(proc *os.Process) error { return nil }

// terminate sends SIGTERM to the negative pgid so the cooperative signal
// reaches every descendant too, then follows with SIGKILL to the negative
// pgid after the configured delay. The direct child's pgid equals its pid
// because Assign made it a group leader. Signaling an empty or dead group
// returns ESRCH and is harmless.
//
// Known pgid-reuse window: between the direct child's exit and its reap by
// cmd.Wait, the leader remains an unreaped zombie that keeps its process
// group reserved (an empty group with no live members). MarkExited always
// follows cmd.Wait on the same goroutine, so any signal this method delivers
// inside that window can only reach the already-dead, still-reserved group —
// it can never hit an unrelated process, because the kernel cannot recycle
// the pid while the zombie holds it. Test doubles that construct stub trees
// without ever calling cmd.Wait artificially widen this window; production
// callers do not.
func (t linuxTree) terminate(pid int) error {
	pgid := -pid
	if err := syscall.Kill(pgid, syscall.SIGTERM); err != nil && err != syscall.ESRCH {
		return err
	}
	time.Sleep(t.killDelay)
	if err := syscall.Kill(pgid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
		return err
	}
	return nil
}

// alive probes the process group through signal zero: members still count as
// alive until they are reaped or the whole group is gone. Subject to the same
// pgid-reuse window documented on terminate: an exited-but-unreaped leader
// keeps the group visible here until cmd.Wait reaps it, which is conservative
// in the safe direction (it may report alive for a tree whose only remaining
// member is the reserved zombie).
func (linuxTree) alive(pid int) bool {
	return syscall.Kill(-pid, 0) == nil || syscall.Kill(pid, 0) == nil
}

func (linuxTree) release() {}
