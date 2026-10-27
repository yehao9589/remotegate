package routercompat

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPackageArchitectureTakesPriorityOverKernel(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("router shell checks require POSIX shell")
	}
	for _, tc := range []struct {
		name, arch, kernel, want string
		reject                   bool
	}{
		{"OpenWrt x86", "arch all 1\narch x86_64 10", "x86_64", "amd64", false},
		{"ImmortalWrt ARM64", "arch all 1\narch aarch64_cortex-a53 10", "aarch64", "arm64", false},
		{"iStoreOS ARM64", "arch aarch64_generic 10", "aarch64", "arm64", false},
		{"QWRT 32-bit userland", "arch all 1\narch aarch64_generic 5\narch arm_cortex-a53_neon-vfpv4 10", "aarch64", "armv7", false},
		{"OpenWrt ARMv7", "arch arm_cortex-a7_neon-vfpv4 10", "armv7l", "armv7", false},
		{"older opkg fallback", "", "armv8l", "armv7", false},
		{"missing package ABI fallback", "arch all 1\narch noarch 1", "aarch64", "arm64", false},
		{"unsupported userland never uses host kernel", "arch mips_24kc 10", "x86_64", "", true},
		{"ARMv6 is not ARMv7", "arch arm_arm1176jzf-s_vfp 10", "armv7l", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "opkg"), []byte("#!/bin/sh\nprintf '%s\n' "+shellTestQuote(tc.arch)+"\n"), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "uname"), []byte("#!/bin/sh\necho "+tc.kernel+"\n"), 0755); err != nil {
				t.Fatal(err)
			}
			c := exec.Command("sh", "-eu", "-c", ArchitectureShell+"\nremotegate_arch")
			c.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
			out, err := c.Output()
			if tc.reject {
				if err == nil {
					t.Fatalf("accepted unsupported ABI: %s", out)
				}
				return
			}
			if err != nil || strings.TrimSpace(string(out)) != tc.want {
				t.Fatalf("got %q, %v; want %s", out, err, tc.want)
			}
		})
	}
}
func shellTestQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

func TestStandaloneInstallerUsesSharedArchitectureDetection(t *testing.T) {
	raw, err := os.ReadFile("../../deploy/openwrt/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ReplaceAll(string(raw), "\r\n", "\n"), ArchitectureShell) {
		t.Fatal("standalone installer architecture detection drifted")
	}
}
