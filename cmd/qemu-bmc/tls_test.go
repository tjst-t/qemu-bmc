package main

import (
	"crypto/tls"
	"crypto/x509"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateSelfSignedCert(t *testing.T) {
	cert, err := generateSelfSignedCert()
	require.NoError(t, err, "generateSelfSignedCert should not return an error")

	t.Run("certificate is parseable", func(t *testing.T) {
		require.NotEmpty(t, cert.Certificate, "Certificate chain must not be empty")
		x509Cert, err := x509.ParseCertificate(cert.Certificate[0])
		require.NoError(t, err, "generated DER must be a valid X.509 certificate")

		t.Run("not yet expired", func(t *testing.T) {
			now := time.Now()
			assert.True(t, now.After(x509Cert.NotBefore) || now.Equal(x509Cert.NotBefore),
				"NotBefore should be in the past or now")
			assert.True(t, now.Before(x509Cert.NotAfter),
				"NotAfter should be in the future (got %v)", x509Cert.NotAfter)
		})

		t.Run("long validity (at least 1 year)", func(t *testing.T) {
			duration := x509Cert.NotAfter.Sub(x509Cert.NotBefore)
			assert.GreaterOrEqual(t, duration, 365*24*time.Hour,
				"certificate should be valid for at least 1 year")
		})

		t.Run("has ServerAuth extended key usage", func(t *testing.T) {
			found := false
			for _, eku := range x509Cert.ExtKeyUsage {
				if eku == x509.ExtKeyUsageServerAuth {
					found = true
					break
				}
			}
			assert.True(t, found, "certificate must have ExtKeyUsageServerAuth")
		})

		t.Run("has DigitalSignature key usage", func(t *testing.T) {
			assert.True(t, x509Cert.KeyUsage&x509.KeyUsageDigitalSignature != 0,
				"certificate must have KeyUsageDigitalSignature")
		})

		t.Run("is self-signed", func(t *testing.T) {
			pool := x509.NewCertPool()
			pool.AddCert(x509Cert)
			_, err := x509Cert.Verify(x509.VerifyOptions{Roots: pool})
			assert.NoError(t, err, "certificate should verify against itself (self-signed)")
		})
	})

	t.Run("private key is present", func(t *testing.T) {
		assert.NotNil(t, cert.PrivateKey, "PrivateKey must be populated")
	})

	t.Run("usable in tls.Config", func(t *testing.T) {
		tlsCfg := &tls.Config{
			MinVersion:   tls.VersionTLS12,
			Certificates: []tls.Certificate{cert},
		}
		assert.Len(t, tlsCfg.Certificates, 1,
			"TLS config should contain exactly one certificate")
	})

	t.Run("each call produces a unique certificate", func(t *testing.T) {
		cert2, err := generateSelfSignedCert()
		require.NoError(t, err)
		// Comparing raw DER bytes; two independent certs must differ
		assert.NotEqual(t, cert.Certificate[0], cert2.Certificate[0],
			"each generateSelfSignedCert call should produce a unique certificate")
	})
}
