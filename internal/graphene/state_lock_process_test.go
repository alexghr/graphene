package graphene

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"testing"
)

const stateLockProcessHelperEnv = "GRAPHENE_STATE_LOCK_PROCESS_HELPER"

func TestStateLockRejectsConcurrentProcess(t *testing.T) {
	repo := newTestRepo(t)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(executable, "-test.run=^TestStateLockProcessHelper$")
	cmd.Env = append(os.Environ(), stateLockProcessHelperEnv+"="+repo.dir)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	t.Cleanup(func() {
		if waited {
			return
		}
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "locked\n" {
		t.Fatalf("lock helper ready = %q, %v; stderr:\n%s", line, err, stderr.String())
	}
	if _, err := (Git{Dir: repo.dir}).AcquireStateLock(); !errors.Is(err, ErrStateLocked) {
		t.Fatalf("AcquireStateLock() during helper = %v, want ErrStateLocked", err)
	}

	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	err = cmd.Wait()
	waited = true
	if err != nil {
		t.Fatalf("lock helper: %v\nstderr:\n%s", err, stderr.String())
	}

	lock, err := (Git{Dir: repo.dir}).AcquireStateLock()
	if err != nil {
		t.Fatalf("AcquireStateLock() after helper exit: %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestStateLockProcessHelper(t *testing.T) {
	repoDir := os.Getenv(stateLockProcessHelperEnv)
	if repoDir == "" {
		return
	}
	lock, err := (Git{Dir: repoDir}).AcquireStateLock()
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(os.Stdout, "locked")
	if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
}
