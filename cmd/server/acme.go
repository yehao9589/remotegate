package main

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/go-acme/lego/v4/certcrypto"
	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/challenge"
	"github.com/go-acme/lego/v4/lego"
	"github.com/go-acme/lego/v4/providers/dns/cloudflare"
	"github.com/go-acme/lego/v4/providers/dns/dnspod"

	"github.com/go-acme/lego/v4/registration"
)

type acmeAccount struct {
	Email        string
	Registration *registration.Resource
	KeyPEM       string
	key          crypto.PrivateKey
}

func (u *acmeAccount) GetEmail() string                        { return u.Email }
func (u *acmeAccount) GetRegistration() *registration.Resource { return u.Registration }
func (u *acmeAccount) GetPrivateKey() crypto.PrivateKey        { return u.key }
func obtainACME(dir string, s certificateState) (string, string, error) {
	path := filepath.Join(dir, fmt.Sprintf("acme-account-%x.json", sha256.Sum256([]byte(s.Email+"|"+caDirectory(s)+"|"+s.Domain))))
	u := &acmeAccount{Email: s.Email}
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return "", "", err
	}
	if len(b) > 0 {
		if err = json.Unmarshal(b, u); err != nil {
			return "", "", err
		}
		block, _ := pem.Decode([]byte(u.KeyPEM))
		if block == nil {
			return "", "", os.ErrInvalid
		}
		u.key, err = x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return "", "", err
		}
	} else {
		u.key, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return "", "", err
		}
		raw, e := x509.MarshalPKCS8PrivateKey(u.key)
		if e != nil {
			return "", "", e
		}
		u.KeyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: raw}))
	}
	// Save the account key before registration, so retries reuse the same identity.
	save := func() error {
		raw, e := json.MarshalIndent(u, "", "  ")
		if e != nil {
			return e
		}
		if e = os.MkdirAll(dir, 0700); e != nil {
			return e
		}
		if e = os.WriteFile(path+".tmp", raw, 0600); e != nil {
			return e
		}
		return os.Rename(path+".tmp", path)
	}
	if err = save(); err != nil {
		return "", "", err
	}
	cfg := lego.NewConfig(u)
	cfg.CADirURL = caDirectory(s)
	if s.KeyType != "" {
		cfg.Certificate.KeyType = certcrypto.KeyType(s.KeyType)
	}
	cfg.HTTPClient = &http.Client{Timeout: 45 * time.Second}
	client, err := lego.NewClient(cfg)
	if err != nil {
		return "", "", err
	}
	var provider challenge.Provider
	switch s.Provider {
	case "alidns":
		provider, err = newAliDNSChallenge(s.AccessKey, s.SecretKey)
	case "dnspod":
		cfg := dnspod.NewDefaultConfig()
		cfg.LoginToken = s.AccessKey
		provider, err = dnspod.NewDNSProviderConfig(cfg)
	case "cloudflare":
		cfg := cloudflare.NewDefaultConfig()
		cfg.AuthToken = s.AccessKey
		cfg.ZoneToken = s.SecretKey
		if cfg.ZoneToken == "" {
			cfg.ZoneToken = s.AccessKey
		}
		provider, err = cloudflare.NewDNSProviderConfig(cfg)
	default:
		return "", "", fmt.Errorf("unsupported DNS provider: %s", s.Provider)
	}
	if err != nil {
		return "", "", err
	}
	if err = client.Challenge.SetDNS01Provider(provider); err != nil {
		return "", "", err
	}
	if u.Registration == nil {
		if s.EABKeyID != "" && s.EABHMAC != "" {
			u.Registration, err = client.Registration.RegisterWithExternalAccountBinding(registration.RegisterEABOptions{TermsOfServiceAgreed: s.TermsAccepted, Kid: s.EABKeyID, HmacEncoded: s.EABHMAC})
		} else {
			u.Registration, err = client.Registration.Register(registration.RegisterOptions{TermsOfServiceAgreed: s.TermsAccepted})
		}
		if err != nil {
			return "", "", err
		}
		if err = save(); err != nil {
			return "", "", err
		}
	}
	result, err := client.Certificate.Obtain(certificate.ObtainRequest{Domains: requestedNames(s), Bundle: true})
	if err != nil {
		return "", "", err
	}
	return string(result.Certificate), string(result.PrivateKey), nil
}
