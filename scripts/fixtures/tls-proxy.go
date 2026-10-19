//go:build ignore

// Development-only TLS proxy for cross-host release smoke. Not packaged.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"flag"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"time"
)

func main() {
	listen := flag.String("listen", ":443", "")
	upstream := flag.String("upstream", "http://127.0.0.1:80", "")
	host := flag.String("host", "", "")
	dir := flag.String("dir", ".", "")
	flag.Parse()
	cert, keyPath := *dir+"/cert.pem", *dir+"/tls.key"
	if _, e := os.Stat(cert); os.IsNotExist(e) {
		key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if e != nil {
			log.Fatal(e)
		}
		serial, e := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
		if e != nil {
			log.Fatal(e)
		}
		t := x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "FilePanel isolated test CA"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(48 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IPAddresses: []net.IP{net.ParseIP(*host)}, DNSNames: []string{"localhost"}}
		der, e := x509.CreateCertificate(rand.Reader, &t, &t, &key.PublicKey, key)
		if e != nil {
			log.Fatal(e)
		}
		private, e := x509.MarshalPKCS8PrivateKey(key)
		if e != nil {
			log.Fatal(e)
		}
		if e = os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0644); e != nil {
			log.Fatal(e)
		}
		if e = os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private}), 0600); e != nil {
			log.Fatal(e)
		}
	}
	u, e := url.Parse(*upstream)
	if e != nil {
		log.Fatal(e)
	}
	s := http.Server{Addr: *listen, Handler: httputil.NewSingleHostReverseProxy(u), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second}
	log.Fatal(s.ListenAndServeTLS(cert, keyPath))
}
