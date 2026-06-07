package bootstrap

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/discovery"
	memcache "k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/restmapper"
)

// applier applies arbitrary multi-document YAML via Server-Side Apply using a
// dynamic client and a deferred discovery RESTMapper. The mapper is reset
// after applying CRDs so freshly-registered kinds become mappable in the same
// reconcile (design §5 ordering: CRDs before the resources that use them).
type applier struct {
	dyn    dynamic.Interface
	mapper *restmapper.DeferredDiscoveryRESTMapper
	cache  discovery.CachedDiscoveryInterface
	log    *slog.Logger
}

func newApplier(dyn dynamic.Interface, disco discovery.DiscoveryInterface, log *slog.Logger) *applier {
	cache := memcache.NewMemCacheClient(disco)
	return &applier{
		dyn:    dyn,
		mapper: restmapper.NewDeferredDiscoveryRESTMapper(cache),
		cache:  cache,
		log:    log,
	}
}

// resetMapper drops cached discovery so CRDs applied this reconcile become
// resolvable. Cheap relative to a reconcile interval.
func (a *applier) resetMapper() {
	a.cache.Invalidate()
	a.mapper.Reset()
}

// applyYAML splits data into documents and SSA-applies each. Empty/whitespace
// or comment-only documents (e.g. TODO placeholder manifests) are skipped, so
// a not-yet-filled tier manifest is a safe no-op. Returns the count applied.
func (a *applier) applyYAML(ctx context.Context, data []byte) (int, error) {
	dec := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	applied := 0
	sawCRD := false
	for {
		raw := map[string]any{}
		if err := dec.Decode(&raw); err != nil {
			if err == io.EOF {
				break
			}
			return applied, fmt.Errorf("decode manifest document: %w", err)
		}
		if len(raw) == 0 {
			continue
		}
		obj := &unstructured.Unstructured{Object: raw}
		if obj.GetKind() == "" || obj.GetAPIVersion() == "" {
			continue
		}
		if obj.GetKind() == "CustomResourceDefinition" {
			sawCRD = true
		}
		if err := a.applyObject(ctx, obj); err != nil {
			return applied, err
		}
		applied++
	}
	// If this batch registered CRDs, refresh discovery so a later batch (or the
	// next tier) can map the new kinds.
	if sawCRD {
		a.resetMapper()
	}
	return applied, nil
}

// applyObject maps an unstructured object to its GVR and SSA-applies it.
func (a *applier) applyObject(ctx context.Context, obj *unstructured.Unstructured) error {
	gvk := obj.GroupVersionKind()
	mapping, err := a.mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		// One retry after a discovery refresh handles the "CRD applied earlier
		// in this same document stream" case.
		if meta.IsNoMatchError(err) {
			a.resetMapper()
			mapping, err = a.mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
		}
		if err != nil {
			return fmt.Errorf("map %s: %w", gvk.String(), err)
		}
	}

	var ri dynamic.ResourceInterface
	if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
		ns := obj.GetNamespace()
		if ns == "" {
			ns = SystemNamespace
			obj.SetNamespace(ns)
		}
		ri = a.dyn.Resource(mapping.Resource).Namespace(ns)
	} else {
		ri = a.dyn.Resource(mapping.Resource)
	}

	_, err = ri.Apply(ctx, obj.GetName(), obj, metav1.ApplyOptions{
		FieldManager: FieldManager,
		Force:        true,
	})
	if err != nil {
		return fmt.Errorf("apply %s %s/%s: %w", gvk.Kind, obj.GetNamespace(), obj.GetName(), err)
	}
	a.log.Debug("applied",
		"kind", gvk.Kind,
		"name", obj.GetName(),
		"namespace", obj.GetNamespace(),
	)
	return nil
}

// isPlaceholder reports whether a manifest is comment-only / empty, meaning the
// tier payload has not been filled in yet (TODO). Used only for clearer logs.
func isPlaceholder(data []byte) bool {
	for _, line := range strings.Split(string(data), "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		return false
	}
	return true
}
