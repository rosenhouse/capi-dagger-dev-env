// Package kube holds Kubernetes checks run from the host through published ports.
package kube

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// requestTimeout bounds each request, because smolvm accepts a connection on a published port before the guest answers it.
var requestTimeout = 30 * time.Second

// Config returns a client config, whose requests time out, for the kubeconfig at path.
func Config(path string) (*rest.Config, error) {
	cfg, err := clientcmd.BuildConfigFromFlags("", path)
	if err != nil {
		return nil, err
	}
	cfg.Timeout = requestTimeout
	return cfg, nil
}

// Client returns a clientset for the kubeconfig at path.
func Client(path string) (kubernetes.Interface, error) {
	cfg, err := Config(path)
	if err != nil {
		return nil, err
	}
	return kubernetes.NewForConfig(cfg)
}

// NodesReady returns nil when the cluster has nodes and all of them are Ready.
func NodesReady(ctx context.Context, cs kubernetes.Interface) error {
	nodes, err := cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	if len(nodes.Items) == 0 {
		return errors.New("no nodes")
	}
	var notReady []string
	for _, n := range nodes.Items {
		if !isReady(n) {
			notReady = append(notReady, n.Name)
		}
	}
	if len(notReady) > 0 {
		return fmt.Errorf("nodes not ready: %s", strings.Join(notReady, ", "))
	}
	return nil
}

func isReady(n corev1.Node) bool {
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// Dynamic returns a dynamic client for the kubeconfig at path.
func Dynamic(path string) (dynamic.Interface, error) {
	cfg, err := Config(path)
	if err != nil {
		return nil, err
	}
	return dynamic.NewForConfig(cfg)
}
