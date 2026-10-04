package main

import (
	"context"
	"errors"
	"sync"
	"time"

	openapi "github.com/alibabacloud-go/darabonba-openapi/v2/client"
	"github.com/alibabacloud-go/tea/dara"
	alidns "github.com/go-acme/alidns-20150109/v4/client"
	"github.com/go-acme/lego/v4/challenge/dns01"
)

// Track the exact record IDs created by this job. Never delete other TXT records.
type aliDNSChallenge struct {
	mu      sync.Mutex
	records map[string]string
	add     func(string, string, string) (string, error)
	remove  func(string) error
	zone    func(string) (string, error)
}

func newAliDNSChallenge(access, secret string) (*aliDNSChallenge, error) {
	cfg := new(openapi.Config).SetAccessKeyId(access).SetAccessKeySecret(secret).SetRegionId("cn-hangzhou").SetReadTimeout(20000).SetConnectTimeout(20000)
	client, err := alidns.NewClient(cfg)
	if err != nil {
		return nil, err
	}
	p := &aliDNSChallenge{records: map[string]string{}, zone: dns01.FindZoneByFqdn}
	p.add = func(zone, rr, value string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		res, e := alidns.AddDomainRecordWithContext(ctx, client, new(alidns.AddDomainRecordRequest).SetDomainName(zone).SetRR(rr).SetType("TXT").SetValue(value).SetTTL(600), &dara.RuntimeOptions{})
		if e != nil {
			return "", e
		}
		if res == nil || res.Body == nil || res.Body.RecordId == nil {
			return "", errors.New("AliDNS did not return a record ID")
		}
		return *res.Body.RecordId, nil
	}
	p.remove = func(id string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, e := alidns.DeleteDomainRecordWithContext(ctx, client, new(alidns.DeleteDomainRecordRequest).SetRecordId(id), &dara.RuntimeOptions{})
		return e
	}
	return p, nil
}
func (p *aliDNSChallenge) Timeout() (time.Duration, time.Duration) {
	return 3 * time.Minute, 5 * time.Second
}
func (p *aliDNSChallenge) Present(domain, token, keyAuth string) error {
	info := dns01.GetChallengeInfo(domain, keyAuth)
	zone, err := p.zone(info.EffectiveFQDN)
	if err != nil {
		return err
	}
	rr, err := dns01.ExtractSubDomain(info.EffectiveFQDN, zone)
	if err != nil {
		return err
	}
	id, err := p.add(dns01.UnFqdn(zone), rr, info.Value)
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.records[domain+"|"+token] = id
	p.mu.Unlock()
	return nil
}
func (p *aliDNSChallenge) CleanUp(domain, token, keyAuth string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	key := domain + "|" + token
	id, ok := p.records[key]
	if !ok {
		return nil
	}
	if err := p.remove(id); err != nil {
		return err
	}
	delete(p.records, key)
	return nil
}
