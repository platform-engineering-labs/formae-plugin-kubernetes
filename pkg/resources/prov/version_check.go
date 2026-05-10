// (C) 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package prov

import (
	"context"
	"log"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/config"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/k8sversion"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/transport"
	"github.com/tidwall/gjson"
)

// CheckPayloadGates inspects a raw JSON payload against every @K8sVersion
// gate registered for the given resource type. Returns the first
// introducedIn / removedIn violation, or nil if all set fields are
// compatible with the cluster's K8s version.
//
// `deprecatedIn` gates do not return errors; they are logged as warnings.
//
// The function short-circuits when the resource has no gated fields, so it
// is safe (and cheap) to call from every provisioner's Create/Update path.
//
// K8s version resolution priority (handled by config.ResolveK8sVersion):
//  1. cfg.KubernetesVersion (target config override)
//  2. FORMAE_K8S_VERSION environment variable
//  3. Live cluster discovery via discovery.ServerVersion()
func CheckPayloadGates(
	ctx context.Context,
	resourceType string,
	rawProperties []byte,
	client *transport.Client,
	cfg *config.Config,
) error {
	paths := k8sversion.PathsForResource(resourceType)
	if len(paths) == 0 {
		return nil
	}

	version, err := config.ResolveK8sVersion(ctx, cfg, client.Discovery())
	if err != nil {
		// Discovery failure is not the gate's fault — let the apply proceed
		// and surface the underlying connectivity error there.
		return nil
	}

	for _, fieldPath := range paths {
		res := gjson.GetBytes(rawProperties, fieldPath)
		if !res.Exists() {
			continue
		}
		// Array-iterating paths (e.g. "spec.containers.#.resizePolicy")
		// always exist when the iteration root exists, even if every
		// element lacks the field. gjson then returns an empty / all-null
		// array. Skip the gate in that case so the user-facing error
		// only fires when the gated field is actually set.
		if res.IsArray() {
			anyValue := false
			for _, elem := range res.Array() {
				if elem.Exists() && elem.Type != gjson.Null {
					anyValue = true
					break
				}
			}
			if !anyValue {
				continue
			}
		}
		gate, ok := k8sversion.Lookup(resourceType, fieldPath)
		if !ok {
			continue
		}
		if err := k8sversion.CheckField(resourceType, fieldPath, version); err != nil {
			return err
		}
		// CheckField handles introducedIn + removedIn. DeprecatedIn is a
		// soft signal: log a warning and continue.
		if gate.DeprecatedIn != "" && config.CompareK8sVersions(version, gate.DeprecatedIn) >= 0 {
			ref := ""
			if gate.Reference != "" {
				ref = " (see " + gate.Reference + ")"
			}
			log.Printf("WARNING: field %q on %s was deprecated in Kubernetes %s%s",
				fieldPath, resourceType, gate.DeprecatedIn, ref)
		}
	}
	return nil
}
