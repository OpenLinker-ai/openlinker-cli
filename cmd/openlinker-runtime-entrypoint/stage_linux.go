//go:build linux

package main

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func runAgentStage(binary string) error {
	if os.Geteuid() != 0 || os.Getegid() != 0 {
		return errors.New("official Provider image launcher must start as root before dropping to the Runtime UID")
	}
	environment, err := agentStageEnvironment(fixedProvider)
	if err != nil {
		return err
	}
	cmd := exec.Command(binary)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = environment
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Credential:  &syscall.Credential{Uid: runtimeUID, Gid: runtimeGID, NoSetGroups: true},
		AmbientCaps: []uintptr{6, 7}, // CAP_SETGID, CAP_SETUID
		Pdeathsig:   syscall.SIGTERM,
	}
	return runPreparedCommand(cmd)
}
