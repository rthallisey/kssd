/*
Copyright 2026 The Kubernetes Authors.

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

package plugin

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"

	lifecycleapi "k8s.io/api/lifecycle/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	cliflag "k8s.io/component-base/cli/flag"
	"k8s.io/component-base/featuregate"
	"k8s.io/component-base/logs"
	logsapi "k8s.io/component-base/logs/api/v1"
	"k8s.io/component-base/term"
	"k8s.io/klog/v2"
	registerapi "k8s.io/kubelet/pkg/apis/pluginregistration/v1"
	slmpbv1alpha1 "k8s.io/kubelet/pkg/apis/slm/v1alpha1"

	"k8s.io/kubectl-server-side-drain/pkg/driver"
)

const (
	// DriverName is the unique identifier for this SLM driver.
	DriverName = "drain.slm.k8s.io"

	// DefaultKubeletPluginsDir is the base directory for per-driver sockets.
	DefaultKubeletPluginsDir = "/var/lib/kubelet/plugins"
	// DefaultKubeletRegistryDir is where the kubelet plugin watcher discovers
	// registration sockets.
	DefaultKubeletRegistryDir = "/var/lib/kubelet/plugins_registry"
)

// NewCommand creates the cobra command tree for the drain driver.
func NewCommand() *cobra.Command {
	o := logsapi.NewLoggingConfiguration()
	var clientset kubernetes.Interface
	logger := klog.Background()

	cmd := &cobra.Command{
		Use:  "drain-driver",
		Long: "SLM driver that implements server-side node drain via the Specialized Lifecycle Management API.",
	}

	sharedFlagSets := cliflag.NamedFlagSets{}
	fs := sharedFlagSets.FlagSet("logging")
	logsapi.AddFlags(o, fs)
	logs.AddFlags(fs, logs.SkipLoggingConfigurationFlags())

	fs = sharedFlagSets.FlagSet("Kubernetes client")
	kubeconfig := fs.String("kubeconfig", "", "Path to kubeconfig. Uses in-cluster config if empty.")
	kubeAPIQPS := fs.Float32("kube-api-qps", 50, "QPS for the Kubernetes API client.")
	kubeAPIBurst := fs.Int("kube-api-burst", 100, "Burst for the Kubernetes API client.")

	fs = sharedFlagSets.FlagSet("SLM")
	driverName := fs.String("driver-name", DriverName, "SLM driver name.")
	evictionTimeout := fs.Duration("eviction-timeout", 30*time.Second, "Timeout for individual pod evictions.")
	gracePeriod := fs.Int64("grace-period", -1, "Override for pod termination grace period (-1 = use pod's own).")

	fs = sharedFlagSets.FlagSet("other")
	featureGate := featuregate.NewFeatureGate()
	utilruntime.Must(logsapi.AddFeatureGates(featureGate))
	featureGate.AddFlag(fs)

	fs = cmd.PersistentFlags()
	for _, f := range sharedFlagSets.FlagSets {
		fs.AddFlagSet(f)
	}

	cmd.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		if err := logsapi.ValidateAndApply(o, featureGate); err != nil {
			return err
		}

		if env := os.Getenv("KUBECONFIG"); env != "" && *kubeconfig == "" {
			*kubeconfig = env
		}

		var config *rest.Config
		var err error
		if *kubeconfig == "" {
			config, err = rest.InClusterConfig()
			if err != nil {
				return fmt.Errorf("create in-cluster config: %w", err)
			}
		} else {
			config, err = clientcmd.BuildConfigFromFlags("", *kubeconfig)
			if err != nil {
				return fmt.Errorf("create out-of-cluster config: %w", err)
			}
		}
		config.QPS = *kubeAPIQPS
		config.Burst = int(*kubeAPIBurst)

		clientset, err = kubernetes.NewForConfig(config)
		if err != nil {
			return fmt.Errorf("create clientset: %w", err)
		}
		return nil
	}

	// kubelet-plugin subcommand
	kubeletPlugin := &cobra.Command{
		Use:   "kubelet-plugin",
		Short: "Run as a kubelet SLM plugin for node drain",
		Args:  cobra.ExactArgs(0),
	}

	pluginFlagSets := cliflag.NamedFlagSets{}
	fs = pluginFlagSets.FlagSet("kubelet")
	kubeletRegistryDir := fs.String("plugin-registration-path", DefaultKubeletRegistryDir, "kubelet plugin registration directory")
	kubeletPluginsDir := fs.String("datadir", DefaultKubeletPluginsDir, "kubelet plugins base directory")
	fs = pluginFlagSets.FlagSet("SLM")
	nodeName := fs.String("node-name", "", "Name of this node (required).")
	sla := fs.Duration("sla", 5*time.Minute, "SLA duration for completing the drain.")
	fs = kubeletPlugin.Flags()
	for _, f := range pluginFlagSets.FlagSets {
		fs.AddFlagSet(f)
	}

	kubeletPlugin.RunE = func(cmd *cobra.Command, args []string) error {
		if *nodeName == "" {
			return errors.New("--node-name is required")
		}

		datadir := path.Join(*kubeletPluginsDir, *driverName)
		if err := os.MkdirAll(filepath.Dir(datadir), 0750); err != nil {
			return fmt.Errorf("create socket directory: %w", err)
		}

		ctx := cmd.Context()

		// Create LifecycleTransitions
		//
		// The drain driver publishes two cluster-wide transitions,
		// usable on all nodes, as defined by the KEP:
		//   1. drain-started → drain-complete    (cordon + evict)
		//   2. uncordoning   → maintenance-complete (uncordon)
		allNodes := true
		slaDuration := metav1.Duration{Duration: *sla}

		drainTransition := &lifecycleapi.LifecycleTransition{
			ObjectMeta: metav1.ObjectMeta{Name: *driverName + "-drain"},
			Spec: lifecycleapi.LifecycleTransitionSpec{
				Start:    driver.DrainStarted,
				End:      driver.DrainComplete,
				AllNodes: &allNodes,
				Driver:   *driverName,
				Sla:      &slaDuration,
			},
		}
		if err := createOrUpdateTransition(ctx, clientset, drainTransition); err != nil {
			return fmt.Errorf("create drain LifecycleTransition: %w", err)
		}
		logger.Info("Published LifecycleTransition", "name", drainTransition.Name)

		uncordonTransition := &lifecycleapi.LifecycleTransition{
			ObjectMeta: metav1.ObjectMeta{Name: *driverName + "-maintenance-complete"},
			Spec: lifecycleapi.LifecycleTransitionSpec{
				Start:    driver.Uncordoning,
				End:      driver.MaintenanceComplete,
				AllNodes: &allNodes,
				Driver:   *driverName,
				Sla:      &slaDuration,
			},
		}
		if err := createOrUpdateTransition(ctx, clientset, uncordonTransition); err != nil {
			return fmt.Errorf("create uncordon LifecycleTransition: %w", err)
		}
		logger.Info("Published LifecycleTransition", "name", uncordonTransition.Name)

		// Start gRPC server
		slmEndpoint := path.Join(datadir, "slm.sock")
		slmListener, err := listen(slmEndpoint)
		if err != nil {
			return fmt.Errorf("listen SLM socket: %w", err)
		}
		slmServer := grpc.NewServer()
		slmpbv1alpha1.RegisterSLMPluginServer(slmServer, driver.NewDrainService(clientset, *nodeName, *evictionTimeout, *gracePeriod))
		go func() {
			logger.Info("SLM gRPC server started", "endpoint", slmEndpoint)
			if err := slmServer.Serve(slmListener); err != nil {
				logger.Error(err, "SLM gRPC server failed")
			}
		}()

		// Start registration server
		regSocket := filepath.Join(*kubeletRegistryDir, *driverName+"-reg.sock")
		regListener, err := listen(regSocket)
		if err != nil {
			slmServer.Stop()
			return fmt.Errorf("listen registration socket: %w", err)
		}
		regServer := grpc.NewServer()
		registerapi.RegisterRegistrationServer(regServer, &registrationService{
			driverName:        *driverName,
			endpoint:          slmEndpoint,
			supportedVersions: []string{slmpbv1alpha1.SLMPluginService},
		})
		go func() {
			logger.Info("Registration server started", "socket", regSocket)
			if err := regServer.Serve(regListener); err != nil {
				logger.Error(err, "Registration gRPC server failed")
			}
		}()

		logger.Info("Drain driver started",
			"driverName", *driverName,
			"nodeName", *nodeName,
			"slmEndpoint", slmEndpoint,
			"registrationSocket", regSocket,
		)

		// Wait for shutdown
		sigc := make(chan os.Signal, 1)
		signal.Notify(sigc, os.Interrupt, syscall.SIGTERM)
		sig := <-sigc
		logger.Info("Received signal, shutting down", "signal", sig)

		regServer.GracefulStop()
		slmServer.GracefulStop()

		// The kubelet's SLM plugin manager handles cleanup of
		// node-scoped transitions on driver deregistration, but
		// AllNodes transitions are left for an administrator or
		// controller to manage.

		return nil
	}
	cmd.AddCommand(kubeletPlugin)

	cols, _, _ := term.TerminalSize(cmd.OutOrStdout())
	cliflag.SetUsageAndHelpFunc(cmd, sharedFlagSets, cols)
	cliflag.SetUsageAndHelpFunc(kubeletPlugin, pluginFlagSets, cols)

	return cmd
}

// createOrUpdateTransition creates the LifecycleTransition or updates it if
// it already exists.
func createOrUpdateTransition(ctx context.Context, cs kubernetes.Interface, lt *lifecycleapi.LifecycleTransition) error {
	_, err := cs.LifecycleV1alpha1().LifecycleTransitions().Create(ctx, lt, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		existing, getErr := cs.LifecycleV1alpha1().LifecycleTransitions().Get(ctx, lt.Name, metav1.GetOptions{})
		if getErr != nil {
			return getErr
		}
		existing.Spec = lt.Spec
		_, err = cs.LifecycleV1alpha1().LifecycleTransitions().Update(ctx, existing, metav1.UpdateOptions{})
	}
	return err
}

// listen creates a Unix domain socket, removing any stale socket first.
func listen(socketPath string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(socketPath), 0750); err != nil {
		return nil, fmt.Errorf("create directory for %s: %w", socketPath, err)
	}
	if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("remove stale socket %s: %w", socketPath, err)
	}
	return net.Listen("unix", socketPath)
}

// Kubelet plugin registration

type registrationService struct {
	registerapi.UnimplementedRegistrationServer
	driverName        string
	endpoint          string
	supportedVersions []string
}

func (r *registrationService) GetInfo(ctx context.Context, req *registerapi.InfoRequest) (*registerapi.PluginInfo, error) {
	klog.FromContext(ctx).Info("GetInfo called", "driver", r.driverName)
	return &registerapi.PluginInfo{
		Type:              registerapi.SLMPlugin,
		Name:              r.driverName,
		Endpoint:          r.endpoint,
		SupportedVersions: r.supportedVersions,
	}, nil
}

func (r *registrationService) NotifyRegistrationStatus(ctx context.Context, status *registerapi.RegistrationStatus) (*registerapi.RegistrationStatusResponse, error) {
	if !status.PluginRegistered {
		klog.FromContext(ctx).Error(nil, "Registration failed", "error", status.Error)
		return nil, fmt.Errorf("registration failed: %s", status.Error)
	}
	klog.FromContext(ctx).Info("Successfully registered with kubelet")
	return &registerapi.RegistrationStatusResponse{}, nil
}
