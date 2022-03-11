package kubelet

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"

	"github.com/sirupsen/logrus"
	k8sApi "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const (
	NodeLabelRegionKey = "failure-domain.beta.kubernetes.io/region"
	NodeLabelZoneKey   = "failure-domain.beta.kubernetes.io/zone"
	PodListAPIEndpoint = "/pods"
)

// ClientConfig holds the config options for connecting to the kubelet API
type ClientConfig struct {
	KubeletAPIPort     int    `long:"kubelet-api-port" env:"KUBELET_API_PORT" description:"kubelet API listening port" default:"10250"`
	KubeletAPIHost     string `long:"kubelet-api" env:"KUBELET_API_HOST" description:"kubelet API hostname" default:"localhost"`
	InsecureSkipVerify bool   `long:"kubelet-api-insecure-skip-verify" env:"KUBELET_API_INSECURE_SKIP_VERIFY" description:"skip verification of TLS certificate from kubelet API"`
	NodeName           string `long:"node-name" env:"NODE_NAME" description:"node name of the exporter pod self running on."`
}

// NewClient returns a new Client based on the given config
func NewClient(c ClientConfig) (*Client, error) {
	// creates the in-cluster config
	config, err := rest.InClusterConfig()
	if err != nil {
		if err == rest.ErrNotInCluster {
			if c.InsecureSkipVerify {
				tr := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
				return &Client{config: c, http: &http.Client{Transport: tr}}, nil
			}

			return &Client{config: c, http: http.DefaultClient}, nil
		}
		return nil, err
	}

	clientSet, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	if c.InsecureSkipVerify {
		config.TLSClientConfig.Insecure = true
		config.TLSClientConfig.CAData = nil
		config.TLSClientConfig.CAFile = ""
	}

	transport, err := rest.TransportFor(config)
	if err != nil {
		return nil, err
	}

	return &Client{config: c, http: &http.Client{Transport: transport}, k8s: clientSet}, nil
}

// Client is an HTTP & K8S client for interacting with kubelet and k8s APIs.
type Client struct {
	config ClientConfig
	http   *http.Client
	k8s    *kubernetes.Clientset
}

// GetPodList returns the list of pods the kubelet is managing
func (k *Client) GetPodList() (*k8sApi.PodList, error) {
	ep, err := k.getPodListAPIEndpoint()
	logrus.Tracef("get the kubelet pod list API endpoint: %v", ep)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest("GET", ep, nil)
	logrus.Tracef("http request %v, error: %v", req, err)
	if err != nil {
		return nil, err
	}

	resp, err := k.http.Do(req)
	logrus.Tracef("http response %v, error: %v", resp, err)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		logrus.Errorf("http response failed %v %v", resp.Header, resp.Body)
		return nil, errors.New("get pod list failed")
	}

	var podList k8sApi.PodList
	b, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if err := json.Unmarshal(b, &podList); err != nil {
		logrus.Tracef("pod list json object error: %v", err)
		return nil, err
	}
	return &podList, nil
}

func (k *Client) GetNode() (*k8sApi.Node, error) {
	return k.k8s.CoreV1().Nodes().Get(context.TODO(), k.config.NodeName, metav1.GetOptions{})
}

func (k *Client) getPodListAPIEndpoint() (string, error) {
	var host string
	if k.config.NodeName != "" {
		host = k.config.NodeName
	} else {
		host = k.config.KubeletAPIHost
	}
	host = fmt.Sprintf("%s:%d", host, k.config.KubeletAPIPort)
	u := &url.URL{Scheme: "https", Host: host, Path: PodListAPIEndpoint}

	ep := u.String()
	_, err := url.Parse(ep)
	if err != nil {
		logrus.Errorf("kubelet API endpoint is not a valid URL, %s", ep)
		return "", err
	}
	return ep, nil
}
