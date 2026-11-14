package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"example.com/go-gateway-reference/internal/meshapi"
	"example.com/go-gateway-reference/internal/tlsutil"
)

// CA 是演示用的网格证书颁发机构。
//
// 根证书持久化在 data 目录；叶子证书短生命周期（默认 10 分钟），
// sidecar 在到期前自动轮换。
type CA struct {
	mu     sync.Mutex
	dir    string
	logger *slog.Logger

	rootCert *x509.Certificate
	rootKey  *ecdsa.PrivateKey
	rootPEM  string

	issued      map[string]meshapi.CertRecord
	issuedTotal int64
}

// NewCA 加载或生成根 CA。
func NewCA(dir string, logger *slog.Logger) (*CA, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create CA directory: %w", err)
	}

	ca := &CA{
		dir:    dir,
		logger: logger,
		issued: make(map[string]meshapi.CertRecord),
	}

	certPath := filepath.Join(dir, "root.pem")
	keyPath := filepath.Join(dir, "root-key.pem")

	certPEM, certErr := os.ReadFile(certPath)
	keyPEM, keyErr := os.ReadFile(keyPath)

	switch {
	case certErr == nil && keyErr == nil:
		if err := ca.load(certPEM, keyPEM); err != nil {
			return nil, err
		}
		logger.Info("root CA loaded", "path", certPath)
		return ca, nil
	case errors.Is(certErr, os.ErrNotExist) && errors.Is(keyErr, os.ErrNotExist):
		if err := ca.generate(); err != nil {
			return nil, err
		}
		logger.Info("root CA generated", "path", certPath)
		return ca, nil
	default:
		return nil, fmt.Errorf("incomplete CA state in %s (root.pem=%v, root-key.pem=%v)", dir, certErr, keyErr)
	}
}

func (ca *CA) load(certPEM, keyPEM []byte) error {
	cert, err := tlsutil.ParseCertificatePEM(certPEM)
	if err != nil {
		return fmt.Errorf("parse root certificate: %w", err)
	}
	if !cert.IsCA {
		return errors.New("root certificate is not a CA")
	}

	keyAny, err := tlsutil.ParsePrivateKeyPEM(keyPEM)
	if err != nil {
		return fmt.Errorf("parse root key: %w", err)
	}

	key, ok := keyAny.(*ecdsa.PrivateKey)
	if !ok {
		return errors.New("root key must be an ECDSA private key")
	}

	ca.rootCert = cert
	ca.rootKey = key
	ca.rootPEM = string(certPEM)
	return nil
}

func (ca *CA) generate() error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}

	serial, err := randomSerial()
	if err != nil {
		return err
	}

	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   "mesh-root-ca",
			Organization: []string{"go-gateway-reference"},
		},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return err
	}

	certPEM := tlsutil.EncodeCertPEM(der)

	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	keyPEM := tlsutil.EncodeKeyPEM(keyDER)

	if err := os.WriteFile(filepath.Join(ca.dir, "root-key.pem"), []byte(keyPEM), 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(ca.dir, "root.pem"), []byte(certPEM), 0o600); err != nil {
		return err
	}

	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return err
	}

	ca.rootCert = cert
	ca.rootKey = key
	ca.rootPEM = certPEM
	return nil
}

// RootPEM 返回根证书 PEM。
func (ca *CA) RootPEM() string {
	ca.mu.Lock()
	defer ca.mu.Unlock()
	return ca.rootPEM
}

// Issue 签发一张叶子证书。
func (ca *CA) Issue(spiffeID string, ttl time.Duration) (certPEM, keyPEM string, expires time.Time, serial string, err error) {
	if err := validateSpiffeID(spiffeID); err != nil {
		return "", "", time.Time{}, "", err
	}

	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	if ttl < time.Minute {
		ttl = time.Minute
	}
	if ttl > time.Hour {
		ttl = time.Hour
	}

	uri, err := url.Parse(spiffeID)
	if err != nil {
		return "", "", time.Time{}, "", err
	}

	service, _ := tlsutil.ServiceFromSpiffeID(spiffeID)

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", time.Time{}, "", err
	}

	serialNumber, err := randomSerial()
	if err != nil {
		return "", "", time.Time{}, "", err
	}

	now := time.Now()
	notAfter := now.Add(ttl)

	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName:   service,
			Organization: []string{"go-gateway-reference"},
		},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{service},
		URIs:                  []*url.URL{uri},
	}

	ca.mu.Lock()
	der, err := x509.CreateCertificate(rand.Reader, template, ca.rootCert, &key.PublicKey, ca.rootKey)
	if err != nil {
		ca.mu.Unlock()
		return "", "", time.Time{}, "", err
	}

	certPEM = tlsutil.EncodeCertPEM(der)

	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		ca.mu.Unlock()
		return "", "", time.Time{}, "", err
	}
	keyPEM = tlsutil.EncodeKeyPEM(keyDER)

	serial = serialNumber.Text(16)
	ca.issued[serial] = meshapi.CertRecord{
		Serial:   serial,
		SpiffeID: spiffeID,
		IssuedAt: now,
		NotAfter: notAfter,
	}
	ca.issuedTotal++

	// 只保留最近 500 条签发记录，避免长期运行后无限增长。
	if len(ca.issued) > 500 {
		records := make([]meshapi.CertRecord, 0, len(ca.issued))
		for _, record := range ca.issued {
			records = append(records, record)
		}
		sort.Slice(records, func(i, j int) bool {
			return records[i].IssuedAt.After(records[j].IssuedAt)
		})
		for _, record := range records[500:] {
			delete(ca.issued, record.Serial)
		}
	}

	ca.mu.Unlock()

	ca.logger.Info("certificate issued",
		"spiffe_id", spiffeID,
		"serial", serial,
		"expires_at", notAfter,
	)

	return certPEM, keyPEM, notAfter, serial, nil
}

// Info 返回 CA 公开信息与签发记录。
func (ca *CA) Info() meshapi.CAInfo {
	ca.mu.Lock()
	defer ca.mu.Unlock()

	records := make([]meshapi.CertRecord, 0, len(ca.issued))
	for _, record := range ca.issued {
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].IssuedAt.After(records[j].IssuedAt)
	})

	return meshapi.CAInfo{
		Subject:   ca.rootCert.Subject.String(),
		NotBefore: ca.rootCert.NotBefore,
		NotAfter:  ca.rootCert.NotAfter,
		Issued:    records,
	}
}

// IssuedTotal 返回累计签发数量。
func (ca *CA) IssuedTotal() int64 {
	ca.mu.Lock()
	defer ca.mu.Unlock()
	return ca.issuedTotal
}

func validateSpiffeID(spiffeID string) error {
	parsed, err := url.Parse(spiffeID)
	if err != nil {
		return fmt.Errorf("invalid SPIFFE ID: %w", err)
	}

	if parsed.Scheme != "spiffe" || parsed.Host != tlsutil.TrustDomain {
		return fmt.Errorf("SPIFFE ID must use spiffe://%s", tlsutil.TrustDomain)
	}

	service, ok := tlsutil.ServiceFromSpiffeID(spiffeID)
	if !ok || service == "" {
		return errors.New("SPIFFE ID must contain /sa/<service>")
	}

	namespace, ok := tlsutil.NamespaceFromSpiffeID(spiffeID)
	if !ok || namespace == "" {
		return errors.New("SPIFFE ID must contain /ns/<namespace>")
	}

	return nil
}

func randomSerial() (*big.Int, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return nil, err
	}

	// 确保最高位为 0，避免被解析成负数。
	buf[0] &= 0x7f

	return new(big.Int).SetBytes(buf), nil
}
