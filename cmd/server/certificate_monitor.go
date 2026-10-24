package main

import (
	"context"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/local/remotegate/internal/config"
)

type certificateTask struct {
	domain  config.DomainSettings
	target  domainCheckTarget
	checked time.Time
}

// Only configured, enabled HTTPS endpoints are monitored. A wildcard certificate
// never substitutes for inspecting the actual endpoint. Oldest observations run
// first so large installations cannot starve entries beyond the first page.
func certificateTasks(domains []config.DomainSettings, mappings []config.Mapping, now time.Time) []certificateTask {
	out := []certificateTask{}
	for _, d := range domains {
		seen := map[string]bool{}
		add := func(host string, port int) {
			if port == 0 {
				port = 443
			}
			key := host + ":" + strconv.Itoa(port)
			if seen[key] {
				return
			}
			seen[key] = true
			var observation *config.PublicHTTPSCheck
			for _, c := range d.HTTPSChecks {
				if c.Host == host && c.Port == port {
					copy := c
					observation = &copy
					break
				}
			}
			if observation == nil && d.PublicHTTPS != nil && d.PublicHTTPS.Host == host && d.PublicHTTPS.Port == port {
				observation = d.PublicHTTPS
			}
			var checked time.Time
			if observation != nil {
				checked = observation.CheckedAt
				interval := 24 * time.Hour
				if !observation.Valid {
					interval = time.Hour
				}
				if !checked.After(now.Add(time.Minute)) && now.Sub(checked) < interval {
					return
				}
			}
			out = append(out, certificateTask{d, domainCheckTarget{Host: host, Port: port, HTTPS: true}, checked})
		}
		add(d.BaseDomain, d.RootHTTPSPort)
		for _, m := range mappings {
			if m.Enabled && m.PublicScheme != "http" && (m.Host == d.BaseDomain || stringsHasDomain(m.Host, d.BaseDomain)) {
				add(m.Host, m.PublicPort)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].checked.Before(out[j].checked) })
	return out
}

func stringsHasDomain(host, base string) bool {
	return strings.HasSuffix(host, "."+base)
}

func (a *app) maintainCertificates(ctx context.Context) {
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		tasks := certificateTasks(a.store.Domains(), a.store.Snapshot().Mappings, time.Now())
		if len(tasks) > 8 {
			tasks = tasks[:8]
		}
		for _, task := range tasks {
			if ctx.Err() != nil {
				return
			}
			checkCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
			_, checks, _ := checkDomainEndpoints(checkCtx, task.domain, []domainCheckTarget{task.target})
			cancel()
			if ctx.Err() != nil {
				return
			}
			if err := a.store.SetDomainHTTPSChecks(task.domain.ID, checks); err != nil {
				log.Printf("HTTPS 自动检测保存失败：%v", err)
			}
		}
		timer.Reset(time.Minute)
	}
}
