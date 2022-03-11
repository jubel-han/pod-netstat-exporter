package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/jessevdk/go-flags"
	"github.com/sirupsen/logrus"
	"golang.org/x/time/rate"
	corev1 "k8s.io/api/core/v1"

	"github.com/wish/pod-netstat-exporter/pkg/cri"
	"github.com/wish/pod-netstat-exporter/pkg/kubelet"
	"github.com/wish/pod-netstat-exporter/pkg/metrics"
	"github.com/wish/pod-netstat-exporter/pkg/netstat"
)

type Options struct {
	kubelet.ClientConfig
	LogLevel      string  `long:"log-level" env:"LOG_LEVEL" description:"Log level" default:"info"`
	RateLimit     float64 `long:"rate-limit" env:"RATE_LIMIT" description:"The number of /metrics requests served per second" default:"3"`
	BindAddr      string  `long:"bind-address" short:"p" env:"BIND_ADDRESS" default:":8080" description:"address for binding metrics listener"`
	HostMountPath string  `long:"host-mount-path" env:"HOST_MOUNT_PATH" default:"/host" description:"The path where the host filesystem is mounted"`
}

func setupLogging(logLevel string) {
	// Use log level
	level, err := logrus.ParseLevel(logLevel)
	if err != nil {
		logrus.Fatalf("Unknown log level %s: %v", logLevel, err)
	}
	logrus.SetLevel(level)

	// Set the log format to have a reasonable timestamp
	formatter := &logrus.TextFormatter{
		FullTimestamp: true,
	}
	logrus.SetFormatter(formatter)
}

func getNodeMeta(opts *Options, k *kubelet.Client) (*metrics.NodeMeta, error) {
	node, err := k.GetNode()
	if err != nil {
		logrus.Error("get node %v failed", opts.NodeName)
		return nil, err
	}

	logrus.Tracef("getting node region and zone from node labels: %v", node.Labels)
	region, ok := node.Labels[kubelet.NodeLabelRegionKey]
	if !ok {
		logrus.Warnf("couldn't found the node region with label key %v", kubelet.NodeLabelRegionKey)
	}

	zone, ok := node.Labels[kubelet.NodeLabelZoneKey]
	if !ok {
		logrus.Warnf("couldn't found the node zone with label key %v", kubelet.NodeLabelZoneKey)
	}

	meta := &metrics.NodeMeta{Name: node.Name, Region: region, Zone: zone}
	logrus.Infof("get node metadata %v", meta)
	return meta, nil
}

// All containers in a pod share the same netns, so get the PID and then the statistics
// from the first pod
func getPodNetstats(opts *Options, pod *corev1.Pod) (*netstat.NetStats, error) {
	logrus.Tracef("Getting stats for pod %v", pod.Name)
	if len(pod.Status.ContainerStatuses) == 0 {
		return nil, fmt.Errorf("no containers in pod")
	}

	container := pod.Status.ContainerStatuses[0].ContainerID
	pid, err := cri.ContainerToPID(opts.HostMountPath, container)
	if err != nil {
		return nil, fmt.Errorf("error getting pid for container %v: %v", container, err)
	}

	logrus.Tracef("Container %v of pod %v has PID %v", container, pod.Name, pid)
	stats, err := netstat.GetStats(opts.HostMountPath, pid)
	return &stats, err
}

func collectAllPodStats(opts *Options, client *kubelet.Client) ([]*metrics.PodStats, error) {
	var podStats []*metrics.PodStats

	pods, err := client.GetPodList()
	if err != nil {
		return podStats, fmt.Errorf("error getting pod list: %v", err)
	}

	// Actually fetch the per-pod statistics
	for _, pod := range pods.Items {
		if pod.Spec.HostNetwork {
			logrus.Tracef("Pod %v has hostNetwork: true, cannot fetch per-pod network metrics", pod.Name)
			continue
		}

		stats, err := getPodNetstats(opts, &pod)
		if err != nil {
			logrus.Warnf("Could not get stats for pod %v: %v", pod.Name, err)
			continue
		}
		podStats = append(podStats, &metrics.PodStats{
			NetStats:  *stats,
			Name:      pod.Name,
			Namespace: pod.Namespace,
		})
	}

	return podStats, nil
}

func main() {
	opts := &Options{}
	parser := flags.NewParser(opts, flags.Default)
	if _, err := parser.Parse(); err != nil {
		// If the error was from the parser, then we can simply return
		// as Parse() prints the error already
		if _, ok := err.(*flags.Error); ok {
			os.Exit(1)
		}
		logrus.Fatalf("Error parsing flags: %v", err)
	}
	setupLogging(opts.LogLevel)

	client, err := kubelet.NewClient(opts.ClientConfig)
	if err != nil {
		logrus.Fatal("initializing the kubelet/k8s client failed: %v", err)
	}

	nodeMeta, err := getNodeMeta(opts, client)
	if err != nil {
		logrus.Fatalf("getting the node metadata failed: %v", err)
	}

	srv := &http.Server{
		Addr: opts.BindAddr,
	}
	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, "OK\n")
	})

	metricsLimiter := rate.NewLimiter(rate.Limit(opts.RateLimit), 5)
	http.HandleFunc("/metrics", func(rsp http.ResponseWriter, req *http.Request) {
		if metricsLimiter.Allow() == false {
			http.Error(rsp, http.StatusText(429), http.StatusTooManyRequests)
			return
		}

		stats, err := collectAllPodStats(opts, client)
		if err != nil {
			logrus.Error(err)
			metrics.HTTPError(rsp, err)
			return
		}

		metrics.Handler(rsp, req, stats, nodeMeta)
	})
	go func() {
		logrus.Infof("Serving HTTP at %v", opts.BindAddr)

		if err := srv.ListenAndServe(); err != nil {
			logrus.Errorf("Error serving HTTP at %v: %v", opts.BindAddr, err)
		}
	}()

	stopCh := make(chan struct{})
	defer close(stopCh)

	sigterm := make(chan os.Signal, 1)
	signal.Notify(sigterm, syscall.SIGTERM)
	signal.Notify(sigterm, syscall.SIGINT)
	<-sigterm

	logrus.Info("Received SIGTERM or SIGINT. Shutting down.")
	_ = srv.Shutdown(context.Background())
}
