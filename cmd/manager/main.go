// The semantic-operator controller manager. Configuration comes from env
// (set by the Helm chart); no endpoint or credential is compiled in.
package main

import (
	"flag"
	"os"
	"strconv"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	semanticv1alpha1 "github.com/KubedAI/ossie-semantic-operator/api/v1alpha1"
	"github.com/KubedAI/ossie-semantic-operator/controllers"
	"github.com/KubedAI/ossie-semantic-operator/internal/emitter"
	_ "github.com/KubedAI/ossie-semantic-operator/internal/emitter/starrocks"
	"github.com/KubedAI/ossie-semantic-operator/internal/starrocks"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(semanticv1alpha1.AddToScheme(scheme))
}

func main() {
	var metricsAddr, probeAddr string
	var enableLeaderElection bool
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "Prometheus metrics address")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "health probe address")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false, "enable leader election")
	opts := zap.Options{Development: false}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	log := ctrl.Log.WithName("setup")

	srClient, err := starrocks.Open(starrocks.Config{
		Host:     mustEnv(log, "STARROCKS_HOST"),
		Port:     envInt("STARROCKS_PORT", 9030),
		User:     envOr("STARROCKS_USER", "root"),
		Password: os.Getenv("STARROCKS_PASSWORD"),
	})
	if err != nil {
		log.Error(err, "opening StarRocks client")
		os.Exit(1)
	}

	dialect, err := emitter.Get(envOr("SQL_DIALECT", "starrocks"))
	if err != nil {
		log.Error(err, "resolving dialect")
		os.Exit(1)
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "semantic-operator.semantic.ossie.io",
	})
	if err != nil {
		log.Error(err, "starting manager")
		os.Exit(1)
	}

	if err = (&controllers.SemanticModelReconciler{
		Client:       mgr.GetClient(),
		StarRocks:    srClient,
		Dialect:      dialect,
		ViewDatabase: envOr("VIEW_DATABASE", "semantic_views"),
		ResyncPeriod: envDuration("RESYNC_PERIOD", 5*time.Minute),
	}).SetupWithManager(mgr); err != nil {
		log.Error(err, "creating controller", "controller", "SemanticModel")
		os.Exit(1)
	}

	utilruntime.Must(mgr.AddHealthzCheck("healthz", healthz.Ping))
	utilruntime.Must(mgr.AddReadyzCheck("readyz", healthz.Ping))

	log.Info("starting semantic-operator")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		log.Error(err, "manager exited")
		os.Exit(1)
	}
}

func mustEnv(log logrLike, key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Error(nil, "required environment variable missing", "key", key)
		os.Exit(1)
	}
	return v
}

type logrLike interface {
	Error(err error, msg string, kv ...any)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
