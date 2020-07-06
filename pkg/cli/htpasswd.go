package cli

import (
	"net"

	"github.com/projectcontour/contour-authserver/pkg/auth"
	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
)

// NewHtpasswdCommand ...
func NewHtpasswdCommand() *cobra.Command {
	cmd := cobra.Command{
		Use:   "htpasswd [OPTIONS]",
		Short: "Run a htpasswd basic authentication server",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			log := ctrl.Log.WithName("auth.htpasswd")
			s := runtime.NewScheme()

			scheme.AddToScheme(s) //nolint(errcheck)

			var cacheFunc cache.NewCacheFunc

			if ns, err := cmd.Flags().GetStringSlice("watch-namespaces"); err == nil && len(ns) > 0 {
				cacheFunc = cache.MultiNamespacedCacheBuilder(ns)
			}

			mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
				Scheme:             s,
				NewCache:           cacheFunc,
				MetricsBindAddress: mustString(cmd.Flags().GetString("metrics-address")),
			})
			if err != nil {
				return ExitErrorf(EX_CONFIG, "failed to create controller manager: %s", err)
			}

			htpasswd := &auth.Htpasswd{
				Log:    log,
				Client: mgr.GetClient(),
				Realm:  mustString(cmd.Flags().GetString("auth-realm")),
			}

			if err := htpasswd.RegisterWithManager(mgr); err != nil {
				return ExitErrorf(EX_FAIL, "htpasswd controller registration failed: %w", err)
			}

			listener, err := net.Listen("tcp", mustString(cmd.Flags().GetString("address")))
			if err != nil {
				return ExitError{EX_CONFIG, err}
			}

			opts := []grpc.ServerOption{
				grpc.MaxConcurrentStreams(1 << 20),
			}

			if anyString(
				mustString(cmd.Flags().GetString("tls-cert-path")),
				mustString(cmd.Flags().GetString("tls-key-path")),
				mustString(cmd.Flags().GetString("tls-ca-path")),
			) {
				creds, err := auth.NewServerCredentials(
					mustString(cmd.Flags().GetString("tls-cert-path")),
					mustString(cmd.Flags().GetString("tls-key-path")),
					mustString(cmd.Flags().GetString("tls-ca-path")),
				)
				if err != nil {
					return ExitErrorf(EX_CONFIG, "invalid TLS configuration: %s", err)
				}

				opts = append(opts, grpc.Creds(creds))
			}

			errChan := make(chan error)
			stopChan := ctrl.SetupSignalHandler()

			go func() {
				srv := grpc.NewServer(opts...)
				auth.RegisterServer(srv, htpasswd)

				log.Info("started authorization server",
					"address", mustString(cmd.Flags().GetString("address")),
					"realm", htpasswd.Realm)

				if err := auth.RunServer(listener, srv, stopChan); err != nil {
					errChan <- ExitErrorf(EX_FAIL, "authorization server failed: %w", err)
				}

				errChan <- nil
			}()

			go func() {
				log.Info("started controller")

				if err := mgr.Start(stopChan); err != nil {
					errChan <- ExitErrorf(EX_FAIL, "controller manager failed: %w", err)
				}

				errChan <- nil
			}()

			select {
			case err := <-errChan:
				return err
			case <-stopChan:
				return nil
			}
		},
	}

	// Controller flags.
	cmd.Flags().String("metrics-address", ":8080", "The address the metrics endpoint binds to.")
	cmd.Flags().StringSlice("watch-namespaces", []string{}, "The list of namespaces to watch for Secrets.")

	// GRPC flags.
	cmd.Flags().String("address", ":9090", "The address the authentication endpoint binds to.")
	cmd.Flags().String("tls-cert-path", "", "Path to the TLS server certificate.")
	cmd.Flags().String("tls-ca-path", "", "Path to the TLS CA certificate bundle.")
	cmd.Flags().String("tls-key-path", "", "Path to the TLS server key.")

	// Authorization flags.
	cmd.Flags().String("auth-realm", "default", "Basic authentication realm.")

	return &cmd
}