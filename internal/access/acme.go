package access

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"filepanel/internal/files"
	"fmt"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/acme"
)

// ACME account and certificate keys live only in the protected database. The
// profile ZIP exporter uses a strict allowlist and never includes these settings.
func (m *Manager) accountKey() (crypto.Signer, error) {
	hash := sha256.Sum256([]byte(m.opts.DirectoryURL))
	keyName := "access_account_" + hex.EncodeToString(hash[:8])
	raw, ok, err := m.db.Setting(keyName)
	if err != nil {
		return nil, err
	}
	if ok {
		block, _ := pem.Decode([]byte(raw))
		if block == nil {
			return nil, errors.New("证书账户私钥损坏")
		}
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		signer, ok := key.(crypto.Signer)
		if !ok {
			return nil, errors.New("证书账户私钥类型无效")
		}
		return signer, nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	data := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	release, err := files.HoldSpace(m.space, uint64(len(data)*2+65536))
	if err != nil {
		return nil, err
	}
	defer release()
	if err = m.db.SetSetting(keyName, string(data)); err != nil {
		return nil, err
	}
	return key, nil
}

// issue uses x/crypto's ACME v2 implementation, explicitly selecting HTTP-01.
// No TLS-ALPN fallback, DNS provider API, or on-demand issuance from arbitrary SNI.
func (m *Manager) issue(ctx context.Context, in Input) (string, error) {
	key, err := m.accountKey()
	if err != nil {
		return "", err
	}
	client := &acme.Client{Key: key, DirectoryURL: m.opts.DirectoryURL, HTTPClient: &http.Client{Timeout: 30 * time.Second}}
	account := &acme.Account{}
	if in.Email != "" {
		account.Contact = []string{"mailto:" + in.Email}
	}
	_, err = client.Register(ctx, account, func(string) bool { return in.AcceptTerms })
	if err != nil && !errors.Is(err, acme.ErrAccountAlreadyExists) {
		return "", err
	}
	domain := hostname(in.URL)
	order, err := client.AuthorizeOrder(ctx, acme.DomainIDs(domain))
	if err != nil {
		return "", err
	}
	for _, authURL := range order.AuthzURLs {
		a, err := client.GetAuthorization(ctx, authURL)
		if err != nil {
			return "", err
		}
		if a.Status == acme.StatusValid {
			continue
		}
		var challenge *acme.Challenge
		for _, c := range a.Challenges {
			if c.Type == "http-01" {
				challenge = c
				break
			}
		}
		if challenge == nil {
			return "", errors.New("CA 未提供 HTTP-01 挑战")
		}
		response, err := client.HTTP01ChallengeResponse(challenge.Token)
		if err != nil {
			return "", err
		}
		path := client.HTTP01ChallengePath(challenge.Token)
		m.mu.Lock()
		m.challenges[domain+path] = response
		m.mu.Unlock()
		err = func() error {
			defer func() { m.mu.Lock(); delete(m.challenges, domain+path); m.mu.Unlock() }()
			if _, err := client.Accept(ctx, challenge); err != nil {
				return err
			}
			_, err := client.WaitAuthorization(ctx, authURL)
			return err
		}()
		if err != nil {
			return "", err
		}
	}
	order, err = client.WaitOrder(ctx, order.URI)
	if err != nil {
		return "", err
	}
	certKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", err
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{DNSNames: []string{domain}}, certKey)
	if err != nil {
		return "", err
	}
	chain, _, err := client.CreateOrderCert(ctx, order.FinalizeURL, csr, true)
	if err != nil {
		return "", err
	}
	var bundle strings.Builder
	for _, der := range chain {
		_ = pem.Encode(&bundle, &pem.Block{Type: "CERTIFICATE", Bytes: der})
	}
	der, err := x509.MarshalPKCS8PrivateKey(certKey)
	if err != nil {
		return "", err
	}
	_ = pem.Encode(&bundle, &pem.Block{Type: "PRIVATE KEY", Bytes: der})
	cert, err := parseBundle(bundle.String())
	if err != nil {
		return "", err
	}
	if err = cert.Leaf.VerifyHostname(domain); err != nil {
		return "", err
	}
	if !cert.Leaf.NotAfter.After(m.opts.Now()) || cert.Leaf.NotBefore.After(m.opts.Now()) {
		return "", fmt.Errorf("CA 返回了当前时间不可用的证书")
	}
	return bundle.String(), nil
}
