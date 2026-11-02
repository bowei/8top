// Command 8top is a k9s-style terminal UI for an Agent Substrate cluster.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/pflag"
	"k8s.io/klog/v2"

	"go.ate.dev/8top/internal/client"
	"go.ate.dev/8top/internal/model"
	"go.ate.dev/8top/internal/ui"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "8top:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		opts     client.Options
		atespace string
		refresh  time.Duration
		readOnly bool
		command  string
		showVer  bool
	)
	pflag.StringVar(&opts.Kubeconfig, "kubeconfig", "", "path to the kubeconfig file (default ~/.kube/config)")
	pflag.StringVar(&opts.Context, "context", "", "kubeconfig context to use (default current context)")
	pflag.StringVar(&opts.Endpoint, "endpoint", "", "gRPC endpoint for the API server; skips the port-forward")
	pflag.StringVar(&opts.TokenFile, "token-file", "", "bearer token for ate-api auth, or - for stdin (default Kubernetes ServiceAccount token)")
	pflag.StringVarP(&atespace, "atespace", "a", "", "atespace to open in (default all atespaces)")
	pflag.DurationVar(&refresh, "refresh", 2*time.Second, "polling interval")
	pflag.BoolVar(&readOnly, "readonly", false, "disable every mutating action and the editor")
	pflag.StringVar(&command, "command", "actors", "view to open at startup (e.g. workers)")
	pflag.BoolVar(&showVer, "version", false, "print the version and exit")
	pflag.Parse()

	if showVer {
		fmt.Println(version)
		return nil
	}
	if refresh <= 0 {
		return fmt.Errorf("--refresh must be positive, got %v", refresh)
	}

	silenceKlog()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c, err := client.New(ctx, opts)
	if err != nil {
		return err
	}
	defer c.Close()

	app := ui.New(ui.Config{
		API:      model.NewStore(c.Ate, c.Kube, c.Pools),
		Context:  c.Context,
		Endpoint: c.Endpoint,
		Version:  version,
		Atespace: atespace,
		Command:  command,
		Refresh:  refresh,
		ReadOnly: readOnly,
	})
	_, err = tea.NewProgram(app, tea.WithContext(ctx)).Run()
	return err
}

// silenceKlog stops client-go (e.g. port-forward stream errors) from writing
// to stderr, which would corrupt the TUI.
func silenceKlog() {
	fs := flag.NewFlagSet("klog", flag.ContinueOnError)
	klog.InitFlags(fs)
	_ = fs.Set("logtostderr", "false")
	_ = fs.Set("alsologtostderr", "false")
	_ = fs.Set("stderrthreshold", "FATAL")
	klog.SetOutput(io.Discard)
}
