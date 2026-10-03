package e2e

import (
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func clientcmdClient(cfg *rest.Config) (kubernetes.Interface, error) { return kubernetes.NewForConfig(cfg) }
