package update

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const MaxPackageSize int64 = 128 << 20

type Manifest struct {
	PackageName    string   `json:"packageName"`
	PackageVersion string   `json:"packageVersion"`
	AgentVersion   string   `json:"agentVersion"`
	SHA256         string   `json:"sha256"`
	Size           int64    `json:"size"`
	Architectures  []string `json:"architectures"`
	Artifact       string   `json:"artifact,omitempty"`
	Grant          string   `json:"grant,omitempty"`
	Rollback       bool     `json:"rollback,omitempty"`
}

var versionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([1-9][0-9]*))?$`)
var hashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func versionParts(value string) ([4]uint64, error) {
	var result [4]uint64
	match := versionPattern.FindStringSubmatch(value)
	if match == nil || len(value) > 64 {
		return result, errors.New("无法识别插件版本，请先手动安装新版插件")
	}
	for i := range result {
		if match[i+1] != "" {
			v, err := strconv.ParseUint(match[i+1], 10, 64)
			if err != nil {
				return result, err
			}
			result[i] = v
		}
	}
	return result, nil
}

func Compare(a, b string) (int, error) {
	x, err := versionParts(a)
	if err != nil {
		return 0, err
	}
	y, err := versionParts(b)
	if err != nil {
		return 0, err
	}
	for i := range x {
		if x[i] > y[i] {
			return 1, nil
		}
		if x[i] < y[i] {
			return -1, nil
		}
	}
	return 0, nil
}

func (m Manifest) Validate() error {
	if _, err := versionParts(m.PackageVersion); err != nil {
		return err
	}
	if !strings.Contains(m.PackageVersion, "-") || m.AgentVersion != strings.Split(m.PackageVersion, "-")[0] || m.PackageName != "luci-app-remotegate" || !hashPattern.MatchString(m.SHA256) || m.Size <= 0 || m.Size > MaxPackageSize {
		return errors.New("服务端安装包清单无效，请更新服务端镜像")
	}
	if m.Artifact != "" && !hashPattern.MatchString(m.Artifact) {
		return errors.New("安装包标识无效")
	}
	if m.Grant != "" && !hashPattern.MatchString(m.Grant) {
		return errors.New("更新授权无效")
	}
	if m.Rollback && (m.Artifact == "" || m.Grant == "") {
		return errors.New("版本回退需要后台授权")
	}
	return nil
}

type Status struct {
	ID          string    `json:"id,omitempty"`
	Phase       string    `json:"phase"`
	FromVersion string    `json:"fromVersion,omitempty"`
	ToVersion   string    `json:"toVersion,omitempty"`
	Message     string    `json:"message,omitempty"`
	StartedAt   time.Time `json:"startedAt,omitempty"`
	UpdatedAt   time.Time `json:"updatedAt,omitempty"`
}

func (s Status) Active() bool {
	switch s.Phase {
	case "checking", "downloading", "installing", "restarting":
		return time.Since(s.StartedAt) < 10*time.Minute
	}
	return false
}

type Check struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

type Preflight struct {
	OK           bool    `json:"ok"`
	Architecture string  `json:"architecture"`
	Checks       []Check `json:"checks"`
}
