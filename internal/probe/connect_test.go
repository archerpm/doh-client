package probe

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"net/netip"
	"reflect"
	"testing"
	"time"
)

type dialFunc func(context.Context, string, string) (net.Conn, error)

func (f dialFunc) DialContext(c context.Context, n, a string) (net.Conn, error) { return f(c, n, a) }

func pipeTLS(t *testing.T, host string) (Dialer, *tls.Config, <-chan string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: host}, DNSNames: []string{host}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	sni := make(chan string, 1)
	dial := dialFunc(func(ctx context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" || address != "[2001:db8::1]:443" {
			t.Errorf("dial %s %s", network, address)
		}
		client, server := net.Pipe()
		go func() {
			defer server.Close()
			s := tls.Server(server, &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) { sni <- hello.ServerName; return nil, nil }})
			_ = s.HandshakeContext(ctx)
		}()
		return client, nil
	})
	return dial, &tls.Config{RootCAs: roots, MaxVersion: tls.VersionTLS12}, sni
}

func TestConnect(t *testing.T) {
	for _, tc := range []struct{ name, host, tcp, tls string }{
		{"valid", "site.test", "ok", "ok"}, {"wrong hostname", "other.test", "ok", "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dial, cfg, sni := pipeTLS(t, tc.host)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			cfg.InsecureSkipVerify = true // Connect must still verify certificates.
			r := Connect(ctx, dial, cfg, "p", "site.test", netip.MustParseAddr("2001:db8::1"))
			if r.TCP != tc.tcp || r.TLS != tc.tls {
				t.Fatalf("%+v", r)
			}
			if got := <-sni; got != "site.test" {
				t.Fatalf("SNI=%s", got)
			}
			if cfg.ServerName != "" || !cfg.InsecureSkipVerify {
				t.Fatal("caller TLS config mutated")
			}
		})
	}
	for _, tc := range []struct {
		name, ip, tcp string
		cancel        bool
	}{
		{"dial failure", "192.0.2.1", "failed", false}, {"sinkhole", "0.0.0.0", "skipped", false}, {"cancelled", "192.0.2.1", "pending", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancel {
				cancel()
			}
			r := Connect(ctx, dialFunc(func(context.Context, string, string) (net.Conn, error) { calls++; return nil, errors.New("fake") }), nil, "p", "site.test", netip.MustParseAddr(tc.ip))
			if r.TCP != tc.tcp || r.TLS != "skipped" {
				t.Fatalf("%+v", r)
			}
			if tc.tcp != "failed" && calls != 0 {
				t.Fatal("unexpected dial")
			}
		})
	}
}

func TestAddresses(t *testing.T) {
	for _, tc := range []struct {
		name     string
		in, want []string
	}{
		{"empty", nil, []string{}},
		{"bounded", []string{"bad", "0.0.0.0", "::", "192.0.2.1", "::ffff:192.0.2.1", "192.0.2.2", "192.0.2.3", "2001:db8::1", "2001:db8::2", "2001:db8::3"}, []string{"192.0.2.1", "192.0.2.2", "2001:db8::1", "2001:db8::2"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := []string{}
			for _, a := range Addresses(tc.in) {
				got = append(got, a.String())
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("%v", got)
			}
		})
	}
}
