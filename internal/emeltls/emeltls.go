// Package emeltls provides a shared HTTP transport for c2g091p01.emel.pt
// that skips certificate expiry checks while still verifying the certificate
// chain and server name.
package emeltls

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"time"
)

var transport = func() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.TLSClientConfig = &tls.Config{
		InsecureSkipVerify: true,
		VerifyConnection: func(cs tls.ConnectionState) error {
			opts := x509.VerifyOptions{
				DNSName:       cs.ServerName,
				Intermediates: x509.NewCertPool(),
				// Use a time within the cert's validity period to bypass expiry checks
				// while still verifying the chain and server name.
				CurrentTime: cs.PeerCertificates[0].NotBefore.Add(time.Second),
			}
			for _, cert := range cs.PeerCertificates[1:] {
				opts.Intermediates.AddCert(cert)
			}
			_, err := cs.PeerCertificates[0].Verify(opts)
			return err
		},
	}
	return t
}()

// Transport returns the shared HTTP transport with certificate expiry checks
// disabled.
func Transport() *http.Transport {
	return transport
}
