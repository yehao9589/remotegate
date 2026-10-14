//go:build !linux

package main

import (
	"errors"
	"os/exec"
)

func detachUpdateProcess(command *exec.Cmd)    {}
func prepareUpdateInstaller(command *exec.Cmd) {}

func availableStorage(string) (uint64, error) { return 0, errors.New("storage check requires Linux") }
