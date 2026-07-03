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

	"github.com/vara-bonthu/osi-semantic-operator/api/v1alpha1"
	"github.com/vara-bonthu/osi-semantic-operator/internal/planner"
)

// Store holds the compiled models currently published by the operator,
// keyed by OSI model name. It is updated by a ConfigMap informer and read
// by every request, so reads take an RLock only.
type Store struct {
	mu     sync.RWMutex
	models map[string]*planner.CompiledModel
	byCM   map[string]string // configmap name -> model name (for deletes)
}

// NewStore returns an empty store.
func NewStore() *Store {
	return &Store{models: map[string]*planner.CompiledModel{}, byCM: map[string]string{}}
}

// Get returns the compiled model by name.
func (s *Store) Get(name string) (*planner.CompiledModel, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m, ok := s.models[name]
	return m, ok
}

// Names returns model names sorted for stable listings.
func (s *Store) Names() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.models))
	for n := range s.models {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Single returns the only model when exactly one is published; adapters use
// it so single-model deployments do not need to name the model per call.
func (s *Store) Single() (*planner.CompiledModel, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.models) != 1 {
		return nil, false
	}
	for _, m := range s.models {
		return m, true
	}
	return nil, false
}

// Put ingests a published ConfigMap (also used directly by tests).
func (s *Store) Put(cmName string, blob []byte) error {
	var m planner.CompiledModel
	if err := json.Unmarshal(blob, &m); err != nil {
		return fmt.Errorf("configmap %s: bad compiled model: %w", cmName, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.models[m.Name] = &m
	s.byCM[cmName] = m.Name
	return nil
}

// Delete removes the model published by a deleted ConfigMap.
func (s *Store) Delete(cmName string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if model, ok := s.byCM[cmName]; ok {
		delete(s.models, model)
		delete(s.byCM, cmName)
	}
}

// WatchConfigMaps runs an informer over operator-published ConfigMaps in the
// namespace and keeps the store current. Blocks until ctx is done.
func WatchConfigMaps(ctx context.Context, namespace string, store *Store, log *slog.Logger) error {
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
		log.Info("loaded compiled model", "configmap", cm.Name,
			"model", cm.Labels[v1alpha1.LabelModel], "version", cm.Labels[v1alpha1.LabelVersion])
	}
	_, err = inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ingest,
		UpdateFunc: func(_, newObj any) { ingest(newObj) },
		DeleteFunc: func(obj any) {
			if cm, ok := obj.(*corev1.ConfigMap); ok {
				store.Delete(cm.Name)
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
	<-ctx.Done()
	return nil
}
