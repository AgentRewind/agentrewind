/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"context"
	"flag"
	"log/slog"
	"os"

	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	agentrewindv1alpha1 "github.com/AgentRewind/agentrewind/api/v1alpha1"
	"github.com/AgentRewind/agentrewind/internal/controller"
	"github.com/AgentRewind/agentrewind/internal/store"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(agentrewindv1alpha1.AddToScheme(scheme))
}

func main() {
	var metricsAddr string
	var probeAddr string
	var enableLeaderElection bool

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metrics endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager.")
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)

	// Connect to Postgres.
	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		log.Error("POSTGRES_DSN environment variable is required")
		os.Exit(1)
	}

	ctx := context.Background()
	pool, err := store.Connect(ctx, dsn)
	if err != nil {
		log.Error("connecting to postgres", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	pgStore := store.NewPostgresStore(pool)
	if err := pgStore.EnsureSchema(ctx); err != nil {
		log.Error("ensuring database schema", "error", err)
		os.Exit(1)
	}
	log.Info("database schema ready")

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress: metricsAddr,
		},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "agentrewind.io",
	})
	if err != nil {
		log.Error("creating manager", "error", err)
		os.Exit(1)
	}

	reconciler := &controller.PodReconciler{
		Client:   mgr.GetClient(),
		Store:    pgStore,
		Recorder: mgr.GetEventRecorderFor("agentrewind-controller"),
		Log:      log.With("controller", "pod"),
	}
	if err := reconciler.SetupWithManager(mgr); err != nil {
		log.Error("setting up pod controller", "error", err)
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		log.Error("setting up health check", "error", err)
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		log.Error("setting up ready check", "error", err)
		os.Exit(1)
	}

	log.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		log.Error("running manager", "error", err)
		os.Exit(1)
	}
}
