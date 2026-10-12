//go:build !linux

package main

import "os/exec"

func detachUpdateProcess(command *exec.Cmd)    {}
func prepareUpdateInstaller(command *exec.Cmd) {}
