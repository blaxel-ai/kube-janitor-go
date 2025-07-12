package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/blaxel-ai/kube-janitor-go/internal/janitor"
	"github.com/blaxel-ai/kube-janitor-go/internal/metrics"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

var rootCmd = &cobra.Command{
	Use:   "kube-janitor-go",
	Short: "Clean up Kubernetes resources based on TTL annotations and rules",
	Long: `kube-janitor-go is a Kubernetes controller that automatically cleans up 
resources based on TTL (time to live) annotations or custom rules.`,
	RunE: run,
}

func init() {
	cobra.OnInitialize(initConfig)

	rootCmd.PersistentFlags().Bool("dry-run", false, "Dry run mode: print what would be deleted without actually deleting")
	rootCmd.PersistentFlags().Duration("interval", 30*time.Second, "Interval between cleanup runs")
	rootCmd.PersistentFlags().Bool("once", false, "Run once and exit")
	rootCmd.PersistentFlags().StringSlice("include-resources", []string{}, "Resource types to include (default: all)")
	rootCmd.PersistentFlags().StringSlice("exclude-resources", []string{"events", "controllerrevisions"}, "Resource types to exclude")
	rootCmd.PersistentFlags().StringSlice("include-namespaces", []string{}, "Namespaces to include (default: all)")
	rootCmd.PersistentFlags().StringSlice("exclude-namespaces", []string{"kube-system", "kube-public", "kube-node-lease"}, "Namespaces to exclude")
	rootCmd.PersistentFlags().String("rules-file", "", "Path to YAML file containing cleanup rules")
	rootCmd.PersistentFlags().Int("metrics-port", 8080, "Port for Prometheus metrics")
	rootCmd.PersistentFlags().String("log-level", "info", "Log level: debug, info, warn, error")
	rootCmd.PersistentFlags().Int("max-workers", 10, "Maximum number of concurrent workers")
	rootCmd.PersistentFlags().String("kubeconfig", "", "Path to kubeconfig file (optional)")

	// Bind flags to viper
	if err := viper.BindPFlags(rootCmd.PersistentFlags()); err != nil {
		logrus.WithError(err).Fatal("Failed to bind flags")
	}
}

func initConfig() {
	// Set environment variable prefix
	viper.SetEnvPrefix("KUBE_JANITOR")
	viper.AutomaticEnv()

	// Set log level
	level, err := logrus.ParseLevel(viper.GetString("log-level"))
	if err != nil {
		logrus.Warnf("Invalid log level %s, using info", viper.GetString("log-level"))
		level = logrus.InfoLevel
	}
	logrus.SetLevel(level)

	// Set log format
	logrus.SetFormatter(&logrus.JSONFormatter{
		TimestampFormat: time.RFC3339,
	})
}

func run(_ *cobra.Command, _ []string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Setup signal handling
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		logrus.Info("Received shutdown signal")
		cancel()
	}()

	// Log version info
	logrus.WithFields(logrus.Fields{
		"version": version,
		"commit":  commit,
		"date":    date,
	}).Info("Starting kube-janitor-go")

	// Create Kubernetes client
	config, err := getKubeConfig()
	if err != nil {
		return fmt.Errorf("failed to get kubernetes config: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("failed to create kubernetes client: %w", err)
	}

	// Start metrics server
	metricsServer := metrics.NewServer(viper.GetInt("metrics-port"))
	go func() {
		if err := metricsServer.Start(); err != nil {
			logrus.WithError(err).Error("Failed to start metrics server")
		}
	}()

	// Create and run janitor
	janitorConfig := janitor.Config{
		DryRun:            viper.GetBool("dry-run"),
		Interval:          viper.GetDuration("interval"),
		Once:              viper.GetBool("once"),
		IncludeResources:  viper.GetStringSlice("include-resources"),
		ExcludeResources:  viper.GetStringSlice("exclude-resources"),
		IncludeNamespaces: viper.GetStringSlice("include-namespaces"),
		ExcludeNamespaces: viper.GetStringSlice("exclude-namespaces"),
		RulesFile:         viper.GetString("rules-file"),
		MaxWorkers:        viper.GetInt("max-workers"),
	}

	j, err := janitor.New(clientset, config, janitorConfig)
	if err != nil {
		return fmt.Errorf("failed to create janitor: %w", err)
	}

	return j.Run(ctx)
}

func getKubeConfig() (*rest.Config, error) {
	kubeconfig := viper.GetString("kubeconfig")

	// Try in-cluster config first
	config, err := rest.InClusterConfig()
	if err == nil {
		return config, nil
	}

	// Fall back to kubeconfig
	if kubeconfig == "" {
		kubeconfig = clientcmd.RecommendedHomeFile
	}

	return clientcmd.BuildConfigFromFlags("", kubeconfig)
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
