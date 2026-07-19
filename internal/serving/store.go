// Package serving hosts the shared query service and the compiled-model
// store that the MCP and REST adapters sit on. Adapters contain no query
// logic: they translate protocol in, protocol out.
package serving

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/KubedAI/semantic-operator/api/v1alpha1"
	"github.com/KubedAI/semantic-operator/internal/planner"
)

// Store holds the compiled models currently published by the operator, keyed
// by ConfigMap name. The ConfigMap name is unique per resource, so two
// resources that declare the same Ossie model name do not overwrite each
// other. Name-based lookups treat a duplicated model name as ambiguous rather
// than silently serving one of them. It is updated by a ConfigMap informer and
// read by every request, so reads take an RLock only.
type Store struct {
	mu     sync.RWMutex
	cms    map[string]*planner.CompiledModel // configmap name -> compiled model
	synced bool
}

// WatchCallbacks receives informer-derived state transitions. Callbacks must be
// cheap and non-blocking because they run on informer event handlers.
type WatchCallbacks struct {
	OnModelCount func(int)
	OnSync       func(bool)
}

// NewStore returns an empty store.
func NewStore() *Store {
	return &Store{cms: map[string]*planner.CompiledModel{}}
}

// MarkSynced records that the backing informer cache has completed its initial
// sync, so the server can safely report readiness.
func (s *Store) MarkSynced() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.synced = true
}

// Synced reports whether the backing informer cache has completed its initial
// sync.
func (s *Store) Synced() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.synced
}

// Count returns the number of compiled models currently loaded.
func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.cms)
}

// byName returns the models published under a given model name. Caller holds
// at least an RLock.
func (s *Store) byName(name string) []*planner.CompiledModel {
	var out []*planner.CompiledModel
	for _, m := range s.cms {
		if m.Name == name {
			out = append(out, m)
		}
	}
	return out
}

// Get returns the compiled model published under name, but only when exactly
// one resource publishes it. Zero or more than one returns ok=false; use
// Ambiguous to tell those two cases apart.
func (s *Store) Get(name string) (*planner.CompiledModel, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if ms := s.byName(name); len(ms) == 1 {
		return ms[0], true
	}
	return nil, false
}

// Ambiguous reports whether more than one resource publishes the given model
// name. Adapters surface this as an error instead of guessing.
func (s *Store) Ambiguous(name string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.byName(name)) > 1
}

// ByName returns every model published under the given name, sorted by
// namespace then resource, so an ambiguity error can say which resources
// collide.
func (s *Store) ByName(name string) []*planner.CompiledModel {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ms := s.byName(name)
	sort.Slice(ms, func(i, j int) bool {
		if ms[i].Namespace != ms[j].Namespace {
			return ms[i].Namespace < ms[j].Namespace
		}
		return ms[i].Resource < ms[j].Resource
	})
	return ms
}

// Names returns the distinct model names, sorted, for stable listings.
func (s *Store) Names() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	seen := map[string]bool{}
	var out []string
	for _, m := range s.cms {
		if !seen[m.Name] {
			seen[m.Name] = true
			out = append(out, m.Name)
		}
	}
	sort.Strings(out)
	return out
}

// All returns every published model, sorted by name then version, so listings
// show duplicates rather than hiding them.
func (s *Store) All() []*planner.CompiledModel {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*planner.CompiledModel, 0, len(s.cms))
	for _, m := range s.cms {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Version < out[j].Version
	})
	return out
}

// Single returns the only model when exactly one resource has published;
// adapters use it so single-model deployments do not name the model per call.
func (s *Store) Single() (*planner.CompiledModel, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.cms) != 1 {
		return nil, false
	}
	for _, m := range s.cms {
		return m, true
	}
	return nil, false
}

// Put ingests a published ConfigMap, keyed by its unique name.
func (s *Store) Put(cmName string, blob []byte) error {
	var m planner.CompiledModel
	if err := json.Unmarshal(blob, &m); err != nil {
		return fmt.Errorf("configmap %s: bad compiled model: %w", cmName, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cms[cmName] = &m
	return nil
}

// Delete removes the model published by a deleted ConfigMap.
func (s *Store) Delete(cmName string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.cms, cmName)
}

// WatchConfigMaps runs an informer over operator-published ConfigMaps in the
// namespace and keeps the store current. Blocks until ctx is done.
func WatchConfigMaps(ctx context.Context, namespace string, store *Store, log *slog.Logger, callbacks *WatchCallbacks) error {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		// Local development: fall back to kubeconfig.
		cfg, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			clientcmd.NewDefaultClientConfigLoadingRules(), nil).ClientConfig()
		if err != nil {
			return fmt.Errorf("no in-cluster config and no kubeconfig: %w", err)
		}
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return err
	}
	selector := "app.kubernetes.io/managed-by=" + v1alpha1.ManagedByValue
	factory := informers.NewSharedInformerFactoryWithOptions(cs, 10*time.Minute,
		informers.WithNamespace(namespace),
		informers.WithTweakListOptions(func(o *metav1.ListOptions) { o.LabelSelector = selector }))

	inf := factory.Core().V1().ConfigMaps().Informer()
	ingest := func(obj any) {
		cm, ok := obj.(*corev1.ConfigMap)
		if !ok {
			return
		}
		blob, ok := cm.Data[v1alpha1.CompiledModelKey]
		if !ok {
			return
		}
		if err := store.Put(cm.Name, []byte(blob)); err != nil {
			log.Error("ingesting compiled model", "configmap", cm.Name, "err", err)
			return
		}
		if callbacks != nil && callbacks.OnModelCount != nil {
			callbacks.OnModelCount(store.Count())
		}
		log.Info("loaded compiled model", "configmap", cm.Name,
			"model", cm.Labels[v1alpha1.LabelModel], "version", cm.Labels[v1alpha1.LabelVersion])
	}
	_, err = inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ingest,
		UpdateFunc: func(_, newObj any) { ingest(newObj) },
		DeleteFunc: func(obj any) {
			if cm, ok := obj.(*corev1.ConfigMap); ok {
				store.Delete(cm.Name)
				if callbacks != nil && callbacks.OnModelCount != nil {
					callbacks.OnModelCount(store.Count())
				}
				log.Info("removed compiled model", "configmap", cm.Name)
			}
		},
	})
	if err != nil {
		return err
	}
	factory.Start(ctx.Done())
	if !cache.WaitForCacheSync(ctx.Done(), inf.HasSynced) {
		return fmt.Errorf("configmap informer failed to sync")
	}
	store.MarkSynced()
	if callbacks != nil && callbacks.OnSync != nil {
		callbacks.OnSync(true)
	}
	<-ctx.Done()
	return nil
}
