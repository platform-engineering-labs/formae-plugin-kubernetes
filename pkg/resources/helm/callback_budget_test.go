//go:build unit

package helm

import (
	"context"
	"github.com/platform-engineering-labs/formae/pkg/plugin"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"helm.sh/helm/v3/pkg/action"
)

func TestChartRetrievalHonorsRemainingCallbackBudget(t *testing.T) {
	t.Setenv("HELM_REPOSITORY_CONFIG", t.TempDir()+"/repositories.yaml")
	t.Setenv("HELM_REPOSITORY_CACHE", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := loadChartWithin(ctx, &action.Configuration{}, &releaseProperties{Chart: server.URL + "/chart.tgz"})
	if err == nil {
		t.Fatal("slow chart succeeded")
	}
	if time.Since(started) > 500*time.Millisecond {
		t.Fatal("chart retrieval exceeded callback budget")
	}
}
func TestBrokerCallbackUsesSingleDeadlineAndReserve(t *testing.T) {
	ctx, cancel, err := brokerCallbackContext(context.Background(), bridgeInfo())
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	deadline, _ := ctx.Deadline()
	left := time.Until(deadline)
	if left > 50*time.Second || left < 49*time.Second {
		t.Fatalf("callback remaining=%s", left)
	}
	parent, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	ctx, cancel, err = brokerCallbackContext(parent, bridgeInfo())
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	d, _ := ctx.Deadline()
	pd, _ := parent.Deadline()
	if !d.Equal(pd) {
		t.Fatal("incoming deadline extended")
	}
}

func TestBrokerCallbackRejectsInvalidMetadataBeforeNetwork(t *testing.T) {
	for _, mutate := range []func(*plugin.OidcOperationInfo){
		func(i *plugin.OidcOperationInfo) { i.BindingID = "" },
		func(i *plugin.OidcOperationInfo) { i.PollInterval = -time.Second },
		func(i *plugin.OidcOperationInfo) { i.ThrottleMaxDelay = time.Duration(math.MaxInt64) },
	} {
		info := bridgeInfo()
		mutate(&info)
		_, cancel, err := brokerCallbackContext(context.Background(), info)
		if cancel != nil {
			cancel()
		}
		if err == nil {
			t.Error("invalid metadata accepted before UID/API reads")
		}
	}
}
func TestStatusActionTimingUsesOriginalAllowance(t *testing.T) {
	if err := validateActionAllowance(bridgeInfo(), 129*time.Second); err == nil {
		t.Fatal("stored short timeout accepted for recovery/service")
	}
	if err := validateActionAllowance(bridgeInfo(), 130*time.Second); err != nil {
		t.Fatal(err)
	}
}
