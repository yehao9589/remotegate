//go:build linux

package main

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func fixtureIPNames() (ipRouteNames, error) {
	return ipRouteNames{tables: map[string]string{"main": "254", "local": "255", "rg_direct": directTable}, protocols: map[string]string{"rg_owner": directProtocol, "static": "4"}}, nil
}

func TestLegacyIPWithoutNumericFlagHandlesIPv4IPv6AndAliases(t *testing.T) {
	for _, family := range []string{"-4", "-6"} {
		for _, aliases := range []bool{false, true} {
			table, proto := directTable, directProtocol
			if aliases {
				table, proto = "rg_direct", "rg_owner"
			}
			unsupported, mutations := 0, 0
			d := &directWANDialer{names: fixtureIPNames, run: func(_ context.Context, _ string, args ...string) (string, error) {
				line := strings.Join(args, " ")
				if args[0] != family {
					t.Fatal("Wrong IP family", line)
				}
				if strings.Contains(line, "-N") {
					unsupported++
					return `Option "-N" is unknown, try "ip -help".`, errors.New("exit status 255")
				}
				if strings.Contains(line, "rule show") {
					return "0: from all lookup local\n1: from all fwmark 0x52470001/0xffffffff lookup " + table, nil
				}
				if strings.Contains(line, "route show table ") {
					return "default dev wan0 proto " + proto + " metric 10", nil
				}
				if strings.Contains(line, "rule add") {
					t.Fatal("Duplicated an existing owned rule", line)
				}
				mutations++
				return "", nil
			}}
			if err := d.configureFamily(context.Background(), family, wanRoute{Interface: "wan0"}); err != nil {
				t.Fatal(family, aliases, err)
			}
			if unsupported != 1 || mutations != 2 {
				t.Fatal("Legacy IP was not handled once without weakening ownership checks", unsupported, mutations)
			}
		}
	}
}

func TestLegacyIPMissingTableStillRejectsForeignAndUnknownAliases(t *testing.T) {
	for _, scenario := range []struct {
		name, all string
		allowed   bool
	}{
		{"absent", "default dev eth0 table main proto static", true},
		{"owned alias", "default dev wan0 table rg_direct proto rg_owner metric 10", true},
		{"foreign alias", "default dev eth0 table rg_direct proto static", false},
		{"unknown alias", "default dev eth0 table unresolved proto static", false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			mutations := 0
			d := &directWANDialer{names: fixtureIPNames, run: func(_ context.Context, _ string, args ...string) (string, error) {
				line := strings.Join(args, " ")
				switch {
				case strings.Contains(line, "-N"):
					return `Option "-N" is unknown`, errors.New("exit status 255")
				case strings.Contains(line, "rule show"):
					return "0: from all lookup local", nil
				case strings.Contains(line, "route show table all"):
					return scenario.all, nil
				case strings.Contains(line, "route show table "):
					return "Dump terminated", errors.New("exit status 2")
				default:
					mutations++
					return "", nil
				}
			}}
			err := d.configureFamily(context.Background(), "-4", wanRoute{Interface: "wan0"})
			if scenario.allowed && (err != nil || mutations == 0) {
				t.Fatal(err, mutations)
			}
			if !scenario.allowed && (err == nil || mutations != 0) {
				t.Fatal("Modified unverified route table", err, mutations)
			}
		})
	}
}

func TestNumericIPDoesNotRetryPermissionFailuresOrBypassUnreadableNames(t *testing.T) {
	calls, loaded := 0, false
	denied := errors.New("operation not permitted")
	d := &directWANDialer{names: func() (ipRouteNames, error) { loaded = true; return ipRouteNames{}, denied }, run: func(_ context.Context, _ string, args ...string) (string, error) { calls++; return "", denied }}
	if err := d.configureFamily(context.Background(), "-4", wanRoute{Interface: "wan0"}); !errors.Is(err, denied) || calls != 1 || loaded {
		t.Fatal(err, calls, loaded)
	}
	d.run = func(_ context.Context, _ string, args ...string) (string, error) {
		calls++
		return `Option "-N" is unknown`, errors.New("exit status 255")
	}
	if err := d.configureFamily(context.Background(), "-4", wanRoute{Interface: "wan0"}); !errors.Is(err, denied) || calls != 2 || !loaded {
		t.Fatal(err, calls, loaded)
	}
}

func TestRouteNameParsingRejectsAmbiguityAndNormalizesOnlyRouteFields(t *testing.T) {
	names, _ := fixtureIPNames()
	if err := parseIPRouteNames(names.tables, "# comment\n138039 rg_other # inline\nbad row\n"); err != nil {
		t.Fatal(err)
	}
	if err := parseIPRouteNames(names.protocols, "0xf2 rg_hex"); err != nil {
		t.Fatal(err)
	}
	normalized := normalizeIPRouteNames("1: from all fwmark 0x52470001 lookup rg_other\ndefault dev rg_other table rg_direct proto rg_hex", names)
	if normalized != "1: from all fwmark 0x52470001 lookup 138039\ndefault dev rg_other table 138039 proto 242" {
		t.Fatal(normalized)
	}
	if err := parseIPRouteNames(names.tables, "999 rg_direct"); err == nil {
		t.Fatal("Ambiguous table name accepted")
	}
}
