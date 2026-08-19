// The semantic-operator controller manager. Configuration comes from built-in
// defaults, an optional YAML file (--config or SEMANTIC_CONFIG), SEMANTIC__
// environment overrides, and the controller-runtime flags (highest
// precedence). See config.go and the confload package.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"go.uber.org/zap/zapcore"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	semanticv1alpha1 "github.com/KubedAI/semantic-operator/api/v1alpha1"
	"github.com/KubedAI/semantic-operator/controllers"
	"github.com/KubedAI/semantic-operator/internal/dbclient"
	"github.com/KubedAI/semantic-operator/internal/emitter"
	_ "github.com/KubedAI/semantic-operator/internal/emitter/starrocks"
	_ "github.com/KubedAI/semantic-operator/internal/emitter/trino"
	_ "github.com/KubedAI/semantic-operator/internal/starrocks"
	_ "github.com/KubedAI/semantic-operator/internal/trino"
)

// configEnv names the config file path when --config is not given.
const configEnv = "SEMANTIC_CONFIG"

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(semanticv1alpha1.AddToScheme(scheme))
}

func main() {
	if err := run(); err != nil {
		os.Exit(1)
	}
}

func run() error {
	var configPath, metricsAddr, probeAddr string
	var leaderElect bool
	flag.StringVar(&configPath, "config", os.Getenv(configEnv), "path to the YAML config file (also "+configEnv+")")
	flag.StringVar(&metricsAddr, "metrics-bind-address", "", "Prometheus metrics address (overrides config)")
	flag.StringVar(&probeAddr, "health-probe-bind-address", "", "health probe address (overrides config)")
	flag.BoolVar(&leaderElect, "leader-elect", false, "enable leader election (overrides config)")
	flag.Parse()

	cfg, err := Load(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "loading configuration: %v\n", err)
		return err
	}
	// Explicit flags are the highest-precedence layer, above file and env.
	flag.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "metrics-bind-address":
			cfg.Metrics.BindAddress = metricsAddr
		case "health-probe-bind-address":
			cfg.Health.HealthProbeBindAddress = probeAddr
		case "leader-elect":
			cfg.LeaderElection.LeaderElect = leaderElect
		}
	})

	ctrl.SetLogger(zap.New(zap.UseDevMode(cfg.Logging.Development), zap.Level(zapLevel(cfg.Logging.Level))))
	log := ctrl.Log.WithName("setup")

	// The dialect selects both halves of the engine boundary: the SQL dialect
	// (emitter) and the connection client (dbclient).
	dialect, err := emitter.Get(cfg.Engine.Dialect)
	if err != nil {
		log.Error(err, "resolving dialect")
		return err
	}
	engineCfg := toEngineConfig(cfg.Engine)
	if engineCfg.Host == "" {
		err := fmt.Errorf("engine.connection.host is required")
		log.Error(err, "reading engine connection config")
		return err
	}
	db, err := dbclient.Open(cfg.Engine.Dialect, engineCfg)
	if err != nil {
		log.Error(err, "opening query engine client", "engine", cfg.Engine.Dialect)
		return err
	}

	options := toManagerOptions(cfg, scheme)
	if len(cfg.Cache.WatchNamespaces) > 0 {
		log.Info("watching a fixed namespace list", "namespaces", cfg.Cache.WatchNamespaces)
	} else {
		log.Info("watching all namespaces; the manager holds a ClusterRole")
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), options)
	if err != nil {
		log.Error(err, "starting manager")
		return err
	}

	if err = (&controllers.SemanticModelReconciler{
		Client:       mgr.GetClient(),
		DB:           db,
		Dialect:      dialect,
		ViewDatabase: cfg.Controller.ViewDatabase,
		ResyncPeriod: cfg.Controller.ResyncPeriod,
	}).SetupWithManager(mgr); err != nil {
		log.Error(err, "creating controller", "controller", "SemanticModel")
		return err
	}

	utilruntime.Must(mgr.AddHealthzCheck("healthz", healthz.Ping))
	utilruntime.Must(mgr.AddReadyzCheck("readyz", healthz.Ping))

	log.Info("starting semantic-operator")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		log.Error(err, "manager exited")
		return err
	}
	return nil
}

// zapLevel maps a config level string to a zap level, defaulting to info.
func zapLevel(s string) zapcore.Level {
	switch strings.ToLower(s) {
	case "debug":
		return zapcore.DebugLevel
	case "warn":
		return zapcore.WarnLevel
	case "error":
		return zapcore.ErrorLevel
	default:
		return zapcore.InfoLevel
	}
}
