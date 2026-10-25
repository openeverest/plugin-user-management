package main

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/openeverest/plugin-user-management/backend/api/v1alpha1"
	"github.com/openeverest/plugin-user-management/backend/internal/controller"
	"github.com/openeverest/plugin-user-management/backend/internal/engine"
	"github.com/openeverest/plugin-user-management/backend/internal/engine/cnpg"
	"github.com/openeverest/plugin-user-management/backend/internal/engine/kserve"
	"github.com/openeverest/plugin-user-management/backend/internal/engine/mongodb"
)

// dist/main.js is copied from the frontend build during the Docker build.
//
//go:embed dist/main.js
var distFS embed.FS

var (
	bundleData    []byte
	bundleDataErr error
	bundleETag    string
	bundleOnce    sync.Once
)

func loadBundle() {
	bundleData, bundleDataErr = distFS.ReadFile("dist/main.js")
	if bundleDataErr == nil {
		sum := sha256.Sum256(bundleData)
		bundleETag = `"` + hex.EncodeToString(sum[:]) + `"`
	}
}

// GET /main.js — stable URL, so revalidate via a content ETag instead of a hashed filename.
func handleBundle(w http.ResponseWriter, r *http.Request) {
	bundleOnce.Do(loadBundle)
	if bundleDataErr != nil {
		http.Error(w, "bundle not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/javascript")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("ETag", bundleETag)
	if r.Header.Get("If-None-Match") == bundleETag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	_, _ = w.Write(bundleData)
}

func handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func kubeConfig() (*rest.Config, error) {
	if cfg, err := rest.InClusterConfig(); err == nil {
		return cfg, nil
	}
	// Local development outside the cluster.
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{}).ClientConfig()
}

func newMux(s *server) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /main.js", handleBundle)
	mux.HandleFunc("GET /healthz", handleHealthz)
	mux.HandleFunc("GET /api/users", s.handleListUsers)
	mux.HandleFunc("POST /api/users", s.handleCreateUser)
	mux.HandleFunc("PUT /api/users/{id}/roles", s.handleUpdateRoles)
	mux.HandleFunc("GET /api/users/{id}/credentials", s.handleGetCredentials)
	mux.HandleFunc("POST /api/users/{id}/password", s.handleResetPassword)
	mux.HandleFunc("DELETE /api/users/{id}", s.handleDeleteUser)
	return mux
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func newScheme() (*runtime.Scheme, error) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return nil, err
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		return nil, err
	}
	return scheme, nil
}

func newManager(cfg *rest.Config) (ctrl.Manager, error) {
	scheme, err := newScheme()
	if err != nil {
		return nil, err
	}
	instance := &unstructured.Unstructured{}
	instance.SetGroupVersionKind(controller.InstanceGVK)
	// Only the plugin's credentials Secrets are cached and watched.
	pluginSecrets, err := labels.NewRequirement(controller.InstanceLabel, selection.Exists, nil)
	if err != nil {
		return nil, err
	}
	return ctrl.NewManager(cfg, ctrl.Options{
		Scheme: scheme,
		// The HTTP server below serves health checks; no metrics endpoint yet.
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
		LeaderElection:         true,
		LeaderElectionID:       "plugin-user-management.plugins.openeverest.io",
		// Set from the pod's namespace by the chart; needed when running out of cluster.
		LeaderElectionNamespace: os.Getenv("POD_NAMESPACE"),
		Cache: cache.Options{ByObject: map[client.Object]cache.ByObject{
			&corev1.Secret{}: {Label: labels.NewSelector().Add(*pluginSecrets)},
		}},
		Client: client.Options{Cache: &client.CacheOptions{
			// Read directly: caching would hold every Instance in the cluster.
			DisableFor: []client.Object{instance},
		}},
	})
}

func main() {
	ctrl.SetLogger(zap.New())
	cfg, err := kubeConfig()
	if err != nil {
		log.Fatalf("kubernetes config: %v", err)
	}
	kube, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		log.Fatalf("kubernetes client: %v", err)
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		log.Fatalf("kubernetes dynamic client: %v", err)
	}
	engines := map[string]engine.Engine{
		mongodb.ProviderName: mongodb.New(kube, dyn),
		cnpg.ProviderName:    cnpg.New(kube, dyn),
		kserve.ProviderName:  kserve.New(kube, dyn),
	}

	mgr, err := newManager(cfg)
	if err != nil {
		log.Fatalf("controller manager: %v", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := controller.IndexInstance(ctx, mgr); err != nil {
		log.Fatalf("index InstanceUsers: %v", err)
	}
	if err := (&controller.Reconciler{Client: mgr.GetClient(), Engines: engines}).SetupWithManager(mgr); err != nil {
		log.Fatalf("set up controller: %v", err)
	}

	s := &server{
		everest: newEverestClient(everestAPIURL()),
		kube:    mgr.GetClient(),
		engines: engines,
	}
	srv := &http.Server{
		Addr:              ":" + envOrDefault("PORT", "8080"),
		Handler:           newMux(s),
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	go func() {
		// Every replica serves the API from the shared cache; only the leader reconciles.
		if err := mgr.Start(ctx); err != nil {
			log.Fatalf("controller manager: %v", err)
		}
	}()
	if !mgr.GetCache().WaitForCacheSync(ctx) {
		log.Fatal("controller cache did not sync")
	}

	log.Printf("plugin-user-management backend listening on %s (everest API: %s)", srv.Addr, s.everest.baseURL)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(fmt.Errorf("server error: %w", err))
	}
}
