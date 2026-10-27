//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type ipRouteNames struct{ tables, protocols map[string]string }

func unsupportedNumericIP(output string, err error) bool {
	text := output + " " + err.Error()
	if !strings.Contains(text, "-N") && !strings.Contains(text, "'N'") {
		return false
	}
	text = strings.ToLower(text)
	return strings.Contains(text, "unknown option") || strings.Contains(text, "is unknown") || strings.Contains(text, "unrecognized option") || strings.Contains(text, "invalid option") || strings.Contains(text, "illegal option")
}

// Older router iproute2 builds lack -N. Translate their configured names back
// to numbers; never bypass ownership checks just because numeric output failed.
func (d *directWANDialer) ipInspector(ctx context.Context, family string) func(...string) (string, error) {
	numeric := true
	var names ipRouteNames
	return func(args ...string) (string, error) {
		if numeric {
			numericArgs := append([]string{family, "-N"}, args...)
			output, err := d.run(ctx, "ip", numericArgs...)
			if err == nil || !unsupportedNumericIP(output, err) {
				return output, err
			}
			load := d.names
			if load == nil {
				load = readIPRouteNames
			}
			names, err = load()
			if err != nil {
				return "", fmt.Errorf("无法读取系统路由名称：%w", err)
			}
			numeric = false
		}
		output, err := d.run(ctx, "ip", append([]string{family}, args...)...)
		if err != nil {
			return output, err
		}
		return normalizeIPRouteNames(output, names), nil
	}
}

func parseIPRouteNames(target map[string]string, text string) error {
	for _, line := range strings.Split(text, "\n") {
		line, _, _ = strings.Cut(line, "#")
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		id, err := strconv.ParseUint(fields[0], 0, 32)
		if err != nil {
			continue
		}
		value := strconv.FormatUint(id, 10)
		if previous, ok := target[fields[1]]; ok && previous != value {
			return fmt.Errorf("路由名称 %s 对应多个编号", fields[1])
		}
		target[fields[1]] = value
	}
	return nil
}

func readIPRouteNames() (ipRouteNames, error) {
	names := ipRouteNames{tables: map[string]string{"unspec": "0", "default": "253", "main": "254", "local": "255"}, protocols: map[string]string{"unspec": "0", "redirect": "1", "kernel": "2", "boot": "3", "static": "4"}}
	for _, base := range []string{"/usr/lib/iproute2", "/usr/share/iproute2", "/etc/iproute2"} {
		for _, kind := range []string{"rt_tables", "rt_protos"} {
			target := names.tables
			if kind == "rt_protos" {
				target = names.protocols
			}
			paths := []string{filepath.Join(base, kind)}
			extra, err := filepath.Glob(filepath.Join(base, kind+".d", "*.conf"))
			if err != nil {
				return names, err
			}
			paths = append(paths, extra...)
			for _, path := range paths {
				data, err := os.ReadFile(path)
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				if err != nil {
					return names, err
				}
				if err = parseIPRouteNames(target, string(data)); err != nil {
					return names, err
				}
			}
		}
	}
	return names, nil
}

func normalizeIPRouteNames(raw string, names ipRouteNames) string {
	lines := strings.Split(raw, "\n")
	for i, line := range lines {
		fields := strings.Fields(line)
		for j := 0; j+1 < len(fields); j++ {
			var values map[string]string
			switch fields[j] {
			case "lookup", "table":
				values = names.tables
			case "proto":
				values = names.protocols
			default:
				continue
			}
			if number, err := strconv.ParseUint(fields[j+1], 0, 32); err == nil {
				fields[j+1] = strconv.FormatUint(number, 10)
			} else if value, ok := values[fields[j+1]]; ok {
				fields[j+1] = value
			}
		}
		lines[i] = strings.Join(fields, " ")
	}
	return strings.Join(lines, "\n")
}
