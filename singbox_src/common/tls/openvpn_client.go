package tls

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"os"
	"strings"

	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
)

func NewOpenVPNClient(ctx context.Context, logger logger.ContextLogger, options option.OpenVPNTLSOptions) (Config, error) {
	ca := options.CA
	if ca == "" && options.CAPath != "" {
		data, err := os.ReadFile(options.CAPath)
		if err != nil {
			return nil, E.Cause(err, "read ca_path")
		}
		ca = string(data)
	}
	certificate := options.Certificate
	if certificate == "" && options.CertificatePath != "" {
		data, err := os.ReadFile(options.CertificatePath)
		if err != nil {
			return nil, E.Cause(err, "read certificate_path")
		}
		certificate = string(data)
	}
	key := options.Key
	if key == "" && options.KeyPath != "" {
		data, err := os.ReadFile(options.KeyPath)
		if err != nil {
			return nil, E.Cause(err, "read key_path")
		}
		key = string(data)
	}
	if strings.TrimSpace(ca) == "" {
		return nil, E.New("openvpn: missing ca certificate")
	}
	if block, _ := pem.Decode([]byte(ca)); block == nil {
		return nil, E.New("openvpn: ca is not valid PEM")
	}
	hasCert := strings.TrimSpace(certificate) != "" || strings.TrimSpace(key) != ""
	if hasCert {
		if strings.TrimSpace(certificate) == "" || strings.TrimSpace(key) == "" {
			return nil, E.New("openvpn: certificate and key must both be set")
		}
		if block, _ := pem.Decode([]byte(certificate)); block == nil {
			return nil, E.New("openvpn: certificate is not valid PEM")
		}
		if block, _ := pem.Decode([]byte(key)); block == nil {
			return nil, E.New("openvpn: key is not valid PEM")
		}
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(ca)) {
		return nil, E.New("openvpn: failed to parse ca certificate")
	}
	var tlsConfig tls.Config
	tlsConfig.RootCAs = roots
	tlsConfig.InsecureSkipVerify = true
	if options.CipherSuites != nil {
	find:
		for _, cipherSuite := range options.CipherSuites {
			for _, tlsCipherSuite := range tls.CipherSuites() {
				if cipherSuite == tlsCipherSuite.Name {
					tlsConfig.CipherSuites = append(tlsConfig.CipherSuites, tlsCipherSuite.ID)
					continue find
				}
			}
			return nil, E.New("unknown cipher_suite: ", cipherSuite)
		}
	}
	tlsConfig.VerifyConnection = func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) == 0 {
			return E.New("openvpn: server did not provide certificate")
		}
		cert := cs.PeerCertificates[0]
		intermediates := x509.NewCertPool()
		for _, intermediate := range cs.PeerCertificates[1:] {
			intermediates.AddCert(intermediate)
		}
		_, err := cert.Verify(x509.VerifyOptions{
			Roots:         roots,
			Intermediates: intermediates,
			KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		})
		if err != nil {
			return err
		}
		if options.VerifyX509Name != "" {
			cn := cert.Subject.CommonName
			switch options.VerifyX509NameMode {
			case "name-prefix":
				if !strings.HasPrefix(cn, options.VerifyX509Name) {
					return E.New("openvpn: server CN ", cn, " does not match prefix ", options.VerifyX509Name)
				}
			case "name-suffix":
				if !strings.HasSuffix(cn, options.VerifyX509Name) {
					return E.New("openvpn: server CN ", cn, " does not match suffix ", options.VerifyX509Name)
				}
			default:
				if cn != options.VerifyX509Name {
					return E.New("openvpn: server CN ", cn, " does not match ", options.VerifyX509Name)
				}
			}
		}
		return nil
	}
	if hasCert {
		cert, err := tls.X509KeyPair([]byte(certificate), []byte(key))
		if err != nil {
			return nil, E.Cause(err, "openvpn: parse client certificate/key")
		}
		tlsConfig.Certificates = []tls.Certificate{cert}
	}
	var config Config = &STDClientConfig{ctx, &tlsConfig, false, 0, false}
	if options.KernelRx || options.KernelTx {
		if !C.IsLinux {
			return nil, E.New("kTLS is only supported on Linux")
		}
		config = &KTLSClientConfig{
			Config:   config,
			logger:   logger,
			kernelTx: options.KernelTx,
			kernelRx: options.KernelRx,
		}
	}
	return config, nil
}
