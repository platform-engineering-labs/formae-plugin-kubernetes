//go:build unit

package transport

import (
	"context"
	"testing"

	"github.com/platform-engineering-labs/formae/pkg/plugin"
)

func TestCacheSeparatesCAAndIdentityOnSameEndpoint(t *testing.T) {
	for _, pair := range [][2]string{
		{`{"Auth":{"Type":"EKS","Endpoint":"https://cluster","CertificateAuthority":"Y2E=","ClusterName":"prod","Region":"us-east-1","Profile":"a"}}`, `{"Auth":{"Type":"EKS","Endpoint":"https://cluster","CertificateAuthority":"Y2E=","ClusterName":"prod","Region":"us-east-1","Profile":"b"}}`},
		{`{"Auth":{"Type":"EKS","Endpoint":"https://cluster","CertificateAuthority":"Y2E=","ClusterName":"prod","Region":"us-east-1"}}`, `{"Auth":{"Type":"EKS","Endpoint":"https://cluster","CertificateAuthority":"Y2Iy","ClusterName":"prod","Region":"us-east-1"}}`},
	} {
		b := &stubBuilder{}
		cache := newClientCache(b.build, plugin.OidcOperationMetadata)
		first, err := cache.Get(context.Background(), mustConfig(t, pair[0]))
		if err != nil {
			t.Fatal(err)
		}
		second, err := cache.Get(context.Background(), mustConfig(t, pair[1]))
		if err != nil {
			t.Fatal(err)
		}
		if first == second {
			t.Error("distinct auth configurations reused client/token cache")
		}
	}
}
