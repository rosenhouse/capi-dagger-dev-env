package greetingcontroller_test

import (
	"context"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	demov1 "github.com/rosenhouse/capi-dagger-dev-env/examples/greeting/api/v1alpha1"
	"github.com/rosenhouse/capi-dagger-dev-env/examples/greeting/internal/greetingcontroller"
)

func TestDeploysHelloBehindProxy(t *testing.T) {
	c, r := setup(greeting("hi"))
	reconcile(t, r)

	hello := get(t, c, &appsv1.Deployment{}, "g1-hello")
	helloContainer := hello.Spec.Template.Spec.Containers[0]
	if helloContainer.Image != "registry.local/hello@sha256:abc" {
		t.Errorf("hello image = %q", helloContainer.Image)
	}
	if env := helloContainer.Env; len(env) != 1 || env[0].Name != "GREETING" || env[0].Value != "hi" {
		t.Errorf("hello env = %v", env)
	}
	assertOwnedByGreeting(t, hello)
	assertServes(t, get(t, c, &corev1.Service{}, "g1-hello"), 8080, hello)

	cm := get(t, c, &corev1.ConfigMap{}, "g1-proxy")
	conf := cm.Data["default.conf"]
	for _, directive := range []string{"listen 8080;", "proxy_pass http://g1-hello:8080;"} {
		if !strings.Contains(conf, directive) {
			t.Errorf("proxy config lacks %q:\n%s", directive, conf)
		}
	}
	assertOwnedByGreeting(t, cm)

	proxy := get(t, c, &appsv1.Deployment{}, "g1-proxy")
	if img := proxy.Spec.Template.Spec.Containers[0].Image; img != "docker.io/library/nginx:1.29-alpine" {
		t.Errorf("proxy image = %q", img)
	}
	if vol := proxy.Spec.Template.Spec.Volumes; len(vol) != 1 || vol[0].ConfigMap == nil || vol[0].ConfigMap.Name != "g1-proxy" {
		t.Errorf("proxy volumes = %v", vol)
	}
	if mounts := proxy.Spec.Template.Spec.Containers[0].VolumeMounts; len(mounts) != 1 || mounts[0].MountPath != "/etc/nginx/conf.d" {
		t.Errorf("proxy mounts = %v, want the config at /etc/nginx/conf.d", mounts)
	}
	assertOwnedByGreeting(t, proxy)
	assertServes(t, get(t, c, &corev1.Service{}, "g1-proxy"), 80, proxy)
}

func TestUpdatesHelloWhenMessageChanges(t *testing.T) {
	c, r := setup(greeting("hi"))
	reconcile(t, r)

	g := get(t, c, &demov1.Greeting{}, "g1")
	g.Spec.Message = "bye"
	if err := c.Update(context.Background(), g); err != nil {
		t.Fatal(err)
	}
	reconcile(t, r)

	env := get(t, c, &appsv1.Deployment{}, "g1-hello").Spec.Template.Spec.Containers[0].Env
	if env[0].Value != "bye" {
		t.Errorf("GREETING = %q, want bye", env[0].Value)
	}
}

func TestIgnoresMissingGreeting(t *testing.T) {
	_, r := setup()
	reconcile(t, r)
}

func greeting(message string) *demov1.Greeting {
	return &demov1.Greeting{
		ObjectMeta: metav1.ObjectMeta{Name: "g1", Namespace: "default", UID: "greeting-uid"},
		Spec:       demov1.GreetingSpec{Message: message},
	}
}

func setup(objs ...client.Object) (client.Client, *greetingcontroller.Reconciler) {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = demov1.AddToScheme(scheme)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	return c, &greetingcontroller.Reconciler{
		Client:     c,
		HelloImage: "registry.local/hello@sha256:abc",
		ProxyImage: "docker.io/library/nginx:1.29-alpine",
	}
}

func reconcile(t *testing.T, r *greetingcontroller.Reconciler) {
	t.Helper()
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "g1"}}); err != nil {
		t.Fatal(err)
	}
}

func get[T client.Object](t *testing.T, c client.Client, obj T, name string) T {
	t.Helper()
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: name}, obj); err != nil {
		t.Fatal(err)
	}
	return obj
}

func assertOwnedByGreeting(t *testing.T, obj client.Object) {
	t.Helper()
	refs := obj.GetOwnerReferences()
	if len(refs) != 1 || refs[0].UID != "greeting-uid" || refs[0].Controller == nil || !*refs[0].Controller {
		t.Errorf("%s owner references = %v", obj.GetName(), refs)
	}
}

func assertServes(t *testing.T, svc *corev1.Service, port int32, d *appsv1.Deployment) {
	t.Helper()
	podLabels := d.Spec.Template.Labels
	containerPort := d.Spec.Template.Spec.Containers[0].Ports[0].ContainerPort
	if len(svc.Spec.Ports) != 1 || svc.Spec.Ports[0].Port != port || svc.Spec.Ports[0].TargetPort.IntVal != containerPort {
		t.Errorf("%s ports = %v, want %d -> %d", svc.Name, svc.Spec.Ports, port, containerPort)
	}
	for k, v := range svc.Spec.Selector {
		if podLabels[k] != v {
			t.Errorf("%s selector %v does not match pod labels %v", svc.Name, svc.Spec.Selector, podLabels)
		}
	}
	if len(svc.Spec.Selector) == 0 {
		t.Errorf("%s has no selector", svc.Name)
	}
	assertOwnedByGreeting(t, svc)
}
