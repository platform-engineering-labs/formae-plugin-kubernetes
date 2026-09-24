//go:build unit

package helm

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/config"
)

func TestLiveClusterUIDUnifiesKubeconfigAndOIDCDNSAlias(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "fixture"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	var uid atomic.Value
	uid.Store("original-uid")
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/namespaces/kube-system" {
			t.Errorf("unexpected API request %s", r.URL.Path)
			w.WriteHeader(500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"apiVersion":"v1","kind":"Namespace","metadata":{"name":"kube-system","uid":%q}}`, uid.Load().(string))
	}))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	server.StartTLS()
	defer server.Close()
	kubeconfig := fixtureKubeconfig(t, server)
	ca := base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	aliasURL := strings.Replace(server.URL, "127.0.0.1", "localhost", 1)
	raw := fmt.Sprintf(`{"Auth":{"Type":"Oidc","Endpoint":%q,"CertificateAuthority":%q,"Audience":"urn:formae:kubernetes:39c24d1d-3815-4817-9242-4032be46601b"}}`, aliasURL, ca)
	oidc, err := config.FromTargetConfig([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	oidc.SetAuthDependencies(config.AuthDependencies{OidcSource: helmTestBroker(func(context.Context, string) (string, error) { return helmJWT("identity"), nil })})
	ctx, cancel := config.NewOperationContext(context.WithValue(context.Background(), helmOperationKey{}, "identity"))
	defer cancel()
	first, err := resolveTestFlightScope(ctx, kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	second, err := resolveTestFlightScope(ctx, oidc)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("selector/DNS aliases split physical UID: %q != %q", first, second)
	}
	uid.Store("replacement-uid")
	replaced, err := resolveTestFlightScope(ctx, oidc)
	if err != nil {
		t.Fatal(err)
	}
	if replaced == second {
		t.Fatal("cached UID hid replaced cluster at unchanged endpoint")
	}
	uid.Store("")
	if _, err := resolveTestFlightScope(ctx, oidc); err == nil {
		t.Fatal("missing UID fell back to selector or endpoint")
	}
}
