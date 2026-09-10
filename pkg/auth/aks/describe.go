// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package aks

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerservice/armcontainerservice/v6"
	"github.com/platform-engineering-labs/formae/pkg/plugin"
	"k8s.io/client-go/tools/clientcmd"
)

// describeTimeout caps the credential chain walk plus one credentials call.
const describeTimeout = 20 * time.Second

// DescribeCluster returns the cluster's API server endpoint and CA bundle,
// so a target need only name the resource group and cluster.
//
// Azure does not put the CA on the ManagedCluster resource at all: it is
// only reachable through the credentials endpoints, which hand back a whole
// kubeconfig. User credentials are tried first — they are AAD-based, need
// only listClusterUserCredential/action, and survive a cluster with
// disableLocalAccounts set. Admin credentials are the fallback for clusters
// with no AAD integration.
//
// Only Host and CAData are taken from the kubeconfig. Its embedded
// credentials are ignored: the token still comes from the agent's own Azure
// identity through ConfigureTransport.
func (p *Provider) DescribeCluster(ctx context.Context) (string, []byte, error) {
	if p.ResourceGroup == "" || p.ClusterName == "" {
		return "", nil, fmt.Errorf(
			"AKS auth: ResourceGroup and ClusterName are required to look up the " +
				"cluster's endpoint and CA — set both, or set Endpoint and " +
				"CertificateAuthority explicitly")
	}
	subscriptionID := p.SubscriptionID
	if subscriptionID == "" {
		subscriptionID = os.Getenv("AZURE_SUBSCRIPTION_ID")
	}
	if subscriptionID == "" {
		return "", nil, fmt.Errorf(
			"AKS auth: no subscription for cluster %q — set SubscriptionId on the "+
				"target or AZURE_SUBSCRIPTION_ID on the agent", p.ClusterName)
	}

	ctx, cancel := context.WithTimeout(ctx, describeTimeout)
	defer cancel()

	log := plugin.LoggerFromContext(ctx).With("auth", "AKS", "cluster", p.ClusterName)

	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return "", nil, fmt.Errorf("AKS auth: failed to create Azure credential: %w", err)
	}
	client, err := armcontainerservice.NewManagedClustersClient(subscriptionID, cred, nil)
	if err != nil {
		return "", nil, fmt.Errorf("AKS auth: failed to create AKS client: %w", err)
	}

	userResp, userErr := client.ListClusterUserCredentials(ctx, p.ResourceGroup, p.ClusterName, nil)
	if userErr == nil {
		if endpoint, caData, ok := connectionFromKubeconfigs(userResp.Kubeconfigs); ok {
			log.Debug("derived AKS connection details from user credentials", "endpoint", endpoint)
			return endpoint, caData, nil
		}
	}

	adminResp, adminErr := client.ListClusterAdminCredentials(ctx, p.ResourceGroup, p.ClusterName, nil)
	if adminErr == nil {
		if endpoint, caData, ok := connectionFromKubeconfigs(adminResp.Kubeconfigs); ok {
			log.Debug("derived AKS connection details from admin credentials", "endpoint", endpoint)
			return endpoint, caData, nil
		}
	}

	return "", nil, fmt.Errorf(
		"AKS auth: could not read connection details for cluster %q in resource group %q. "+
			"listClusterUserCredential: %v. listClusterAdminCredential: %v. "+
			"Grant Microsoft.ContainerService/managedClusters/listClusterUserCredential/action, "+
			"or set Endpoint and CertificateAuthority explicitly",
		p.ClusterName, p.ResourceGroup, credentialError(userErr), credentialError(adminErr))
}

// connectionFromKubeconfigs pulls the server URL and CA bundle out of the
// first kubeconfig Azure returns. Reports false when no entry carries both.
func connectionFromKubeconfigs(kubeconfigs []*armcontainerservice.CredentialResult) (string, []byte, bool) {
	for _, kc := range kubeconfigs {
		if kc == nil || len(kc.Value) == 0 {
			continue
		}
		restCfg, err := clientcmd.RESTConfigFromKubeConfig(kc.Value)
		if err != nil {
			continue
		}
		if restCfg.Host == "" || len(restCfg.CAData) == 0 {
			continue
		}
		return restCfg.Host, restCfg.CAData, true
	}
	return "", nil, false
}

// credentialError keeps the joined error message readable when one of the
// two calls actually succeeded but returned nothing usable.
func credentialError(err error) string {
	if err == nil {
		return "returned no usable kubeconfig"
	}
	return err.Error()
}
