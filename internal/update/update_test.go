package update

import (
	"strings"
	"testing"
	"time"
)

func TestNumericPackageVersionOrdering(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"0.1.10-1", "0.1.9-9", 1}, {"0.1.8-2", "0.1.8-1", 1},
		{"0.1.8-1", "0.1.8-1", 0}, {"0.1.8-1", "0.2.0-1", -1}, {"0.1.8-1", "0.1.8", 1},
	} {
		got, err := Compare(tc.a, tc.b)
		if err != nil || got != tc.want {
			t.Fatalf("%s / %s: %d %v", tc.a, tc.b, got, err)
		}
	}
	for _, bad := range []string{"", "latest", "0.01.8-1", "0.1.8-0", "0.1.8; reboot", "999999999999999999999.1.1-1"} {
		if _, err := Compare(bad, "0.1.8-1"); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestManifestCannotSubstituteAnotherPackage(t *testing.T) {
	good := Manifest{PackageName: "luci-app-remotegate", PackageVersion: "0.1.8-1", AgentVersion: "0.1.8", SHA256: strings.Repeat("a", 64), Size: 25, Architectures: []string{"x86_64"}}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Manifest){
		func(m *Manifest) { m.PackageName = "luci-app-other" }, func(m *Manifest) { m.Size = MaxPackageSize + 1 },
		func(m *Manifest) { m.SHA256 = "not-a-checksum" }, func(m *Manifest) { m.AgentVersion = "0.1.7" },
	} {
		m := good
		mutate(&m)
		if m.Validate() == nil {
			t.Fatal("accepted invalid manifest", m)
		}
	}
	if (Status{Phase: "installing", StartedAt: time.Now().Add(-11 * time.Minute)}).Active() {
		t.Fatal("expired update stays active")
	}
}
