package e2e

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/client-go/transport/spdy"
	"k8s.io/streaming/pkg/httpstream"
	"k8s.io/utils/ptr"

	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/ready"
)

// KubectlWorks waits until logs and port-forward to CoreDNS, and exec in etcd, work the way kubectl does them.
// Every cluster here runs both in kube-system.
func KubectlWorks(ctx context.Context, kubeconfig string) error {
	return ready.Wait(ctx, ready.Gate{
		Name: "kubectl logs, exec and port-forward work through " + kubeconfig, Timeout: 3 * time.Minute, Interval: 3 * time.Second, Attempt: 30 * time.Second,
		Check: func(ctx context.Context) error { return kubectlWorks(ctx, kubeconfig) },
	})
}

func kubectlWorks(ctx context.Context, kubeconfig string) error {
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return err
	}
	api, err := url.Parse(cfg.Host)
	if err != nil {
		return err
	}
	proxy, stop, err := closingProxy(api.Host)
	if err != nil {
		return err
	}
	defer stop()
	// Some steps ignore their context, so the proxy closes when the context ends, not when they return.
	context.AfterFunc(ctx, stop)
	if cfg.TLSClientConfig.ServerName == "" {
		cfg.TLSClientConfig.ServerName = api.Hostname()
	}
	cfg.Host = api.Scheme + "://" + proxy
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return err
	}
	coredns, err := firstPod(ctx, cs, "k8s-app=kube-dns")
	if err != nil {
		return fmt.Errorf("list CoreDNS pods: %w", err)
	}
	etcd, err := firstPod(ctx, cs, "component=etcd")
	if err != nil {
		return fmt.Errorf("list etcd pods: %w", err)
	}
	if _, err := cs.CoreV1().Pods("kube-system").GetLogs(coredns, &corev1.PodLogOptions{TailLines: ptr.To[int64](1)}).DoRaw(ctx); err != nil {
		return fmt.Errorf("logs %s: %w", coredns, err)
	}
	if err := execEtcdVersion(ctx, cfg, cs, etcd); err != nil {
		return fmt.Errorf("exec %s: %w", etcd, err)
	}
	if err := portForwardHealth(ctx, cfg, cs, coredns); err != nil {
		return fmt.Errorf("port-forward %s: %w", coredns, err)
	}
	return nil
}

func firstPod(ctx context.Context, cs kubernetes.Interface, selector string) (string, error) {
	pods, err := cs.CoreV1().Pods("kube-system").List(ctx, metav1.ListOptions{LabelSelector: selector, FieldSelector: "status.phase=Running"})
	if err != nil {
		return "", err
	}
	if len(pods.Items) == 0 {
		return "", fmt.Errorf("no running pod in kube-system matches %s", selector)
	}
	return pods.Items[0].Name, nil
}

func execEtcdVersion(ctx context.Context, cfg *rest.Config, cs *kubernetes.Clientset, pod string) error {
	url := cs.CoreV1().RESTClient().Post().Resource("pods").Namespace("kube-system").Name(pod).SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{Container: "etcd", Command: []string{"etcd", "--version"}, Stdout: true, Stderr: true}, scheme.ParameterCodec).
		URL()
	websocket, err := remotecommand.NewWebSocketExecutor(cfg, "GET", url.String())
	if err != nil {
		return err
	}
	spdyExec, err := remotecommand.NewSPDYExecutor(cfg, "POST", url)
	if err != nil {
		return err
	}
	exec, err := remotecommand.NewFallbackExecutor(websocket, spdyExec, upgradeFailed)
	if err != nil {
		return err
	}
	var stdout, stderr bytes.Buffer
	if err := exec.StreamWithContext(ctx, remotecommand.StreamOptions{Stdout: &stdout, Stderr: &stderr}); err != nil {
		if stderr.Len() > 0 {
			err = fmt.Errorf("%w: %s", err, stderr.String())
		}
		return err
	}
	if !strings.Contains(stdout.String(), "etcd Version") {
		return fmt.Errorf("etcd --version printed %q", stdout.String())
	}
	return nil
}

func portForwardHealth(ctx context.Context, cfg *rest.Config, cs *kubernetes.Clientset, pod string) error {
	url := cs.CoreV1().RESTClient().Post().Resource("pods").Namespace("kube-system").Name(pod).SubResource("portforward").URL()
	websocket, err := portforward.NewSPDYOverWebsocketDialer(url, cfg)
	if err != nil {
		return err
	}
	transport, upgrader, err := spdy.RoundTripperFor(cfg)
	if err != nil {
		return err
	}
	dialer := portforward.NewFallbackDialer(websocket, spdy.NewDialer(upgrader, &http.Client{Transport: transport}, "POST", url), upgradeFailed)
	stop, forwarding := make(chan struct{}), make(chan struct{})
	defer close(stop)
	pf, err := portforward.NewOnAddresses(dialer, []string{"127.0.0.1"}, []string{"0:8080"}, stop, forwarding, io.Discard, io.Discard)
	if err != nil {
		return err
	}
	forwarded := make(chan error, 1)
	go func() { forwarded <- pf.ForwardPorts() }()
	select {
	case <-forwarding:
	case err := <-forwarded:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
	ports, err := pf.GetPorts()
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("http://127.0.0.1:%d/health", ports[0].Local), nil)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{Transport: &http.Transport{DisableKeepAlives: true}}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(body)) != "OK" {
		return fmt.Errorf("CoreDNS /health returned %d %q", resp.StatusCode, body)
	}
	return nil
}

// upgradeFailed tells the fallbacks, as in kubectl, when to retry over SPDY.
func upgradeFailed(err error) bool {
	return httpstream.IsUpgradeFailure(err) || httpstream.IsHTTPSProxyError(err)
}

// closingProxy forwards connections to target until stop, which closes all of them.
// A Dagger host tunnel stalls while any of its connections leaves data unread, and some
// client-go streaming paths ignore their context, so a hung attempt must not leave connections open.
func closingProxy(target string) (addr string, stop func(), err error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	var mu sync.Mutex
	var conns []net.Conn
	stopped := false
	keep := func(c net.Conn) bool {
		mu.Lock()
		defer mu.Unlock()
		if stopped {
			c.Close()
			return false
		}
		conns = append(conns, c)
		return true
	}
	go func() {
		for {
			client, err := l.Accept()
			if err != nil {
				return
			}
			upstream, err := net.Dial("tcp", target)
			if err != nil {
				client.Close()
				continue
			}
			if !keep(client) || !keep(upstream) {
				upstream.Close()
				return
			}
			// Either side closing closes both, so no connection outlives its peer with data unread.
			pipe := func(dst, src net.Conn) {
				defer dst.Close()
				defer src.Close()
				_, _ = io.Copy(dst, src)
			}
			go pipe(upstream, client)
			go pipe(client, upstream)
		}
	}()
	return l.Addr().String(), func() {
		l.Close()
		mu.Lock()
		defer mu.Unlock()
		stopped = true
		for _, c := range conns {
			c.Close()
		}
	}, nil
}
