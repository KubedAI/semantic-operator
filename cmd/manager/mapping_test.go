package main

import (
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
)

func TestToManagerOptionsLeavesZeroDurationsNil(t *testing.T) {
	cfg := Config{
		Metrics:        MetricsConfig{BindAddress: ":8080"},
		Health:         HealthConfig{HealthProbeBindAddress: ":8081"},
		LeaderElection: LeaderElectionConfig{LeaderElect: true, ResourceName: "id"},
	}
	opts := toManagerOptions(cfg, runtime.NewScheme())
	if opts.Metrics.BindAddress != ":8080" || opts.HealthProbeBindAddress != ":8081" {
		t.Errorf("addresses = %q / %q", opts.Metrics.BindAddress, opts.HealthProbeBindAddress)
	}
	if !opts.LeaderElection || opts.LeaderElectionID != "id" {
		t.Errorf("leader election = %v / %q", opts.LeaderElection, opts.LeaderElectionID)
	}
	// Zero durations must stay nil so controller-runtime keeps its defaults.
	if opts.LeaseDuration != nil || opts.RenewDeadline != nil || opts.RetryPeriod != nil {
		t.Errorf("zero durations must map to nil, got %v/%v/%v", opts.LeaseDuration, opts.RenewDeadline, opts.RetryPeriod)
	}
}

func TestToManagerOptionsSetsConfiguredDurationsAndNamespaces(t *testing.T) {
	cfg := Config{
		LeaderElection: LeaderElectionConfig{
			LeaseDuration: 20 * time.Second,
			RenewDeadline: 15 * time.Second,
			RetryPeriod:   3 * time.Second,
		},
		Cache: CacheConfig{WatchNamespaces: []string{"team-a", "team-b"}},
	}
	opts := toManagerOptions(cfg, runtime.NewScheme())
	if opts.LeaseDuration == nil || *opts.LeaseDuration != 20*time.Second {
		t.Errorf("leaseDuration = %v, want 20s", opts.LeaseDuration)
	}
	if opts.RenewDeadline == nil || *opts.RenewDeadline != 15*time.Second {
		t.Errorf("renewDeadline = %v, want 15s", opts.RenewDeadline)
	}
	if opts.RetryPeriod == nil || *opts.RetryPeriod != 3*time.Second {
		t.Errorf("retryPeriod = %v, want 3s", opts.RetryPeriod)
	}
	if len(opts.Cache.DefaultNamespaces) != 2 {
		t.Errorf("cache namespaces = %v, want team-a and team-b", opts.Cache.DefaultNamespaces)
	}
	if _, ok := opts.Cache.DefaultNamespaces["team-a"]; !ok {
		t.Error("cache must include team-a")
	}
}
