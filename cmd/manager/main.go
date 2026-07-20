// The semantic-operator controller manager. Configuration comes from env
// (set by the Helm chart); no endpoint or credential is compiled in.
package main

import (
	"flag"
	"os"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	semanticv1alpha1 "github.com/KubedAI/semantic-operator/api/v1alpha1"
	"github.com/KubedAI/semantic-operator/controllers"
	"github.com/KubedAI/semantic-operator/internal/dbclient"
	"github.com/KubedAI/semantic-operator/internal/emitter"
	_ "github.com/KubedAI/semantic-operator/internal/emitter/starrocks"
	_ "github.com/KubedAI/semantic-operator/internal/emitter/trino"
	_ "github.com/KubedAI/semantic-operator/internal/starrocks"
	_ "github.com/KubedAI/semantic-operator/internal/trino"
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

	// SQL_DIALECT selects both halves of the engine boundary: the SQL
	// dialect (emitter) and the connection client (dbclient). The ENGINE_*
	// env vars configure the connection.
	engine := envOr("SQL_DIALECT", "starrocks")
	dialect, err := emitter.Get(engine)
	if err != nil {
		log.Error(err, "resolving dialect")
		os.Exit(1)
	}
	cfg, err := dbclient.EnvConfig()
	if err != nil {
		log.Error(err, "reading engine connection config")
		os.Exit(1)
	}
	db, err := dbclient.Open(engine, cfg)
	if err != nil {
		log.Error(err, "opening query engine client", "engine", engine)
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
		DB:           db,
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

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
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
