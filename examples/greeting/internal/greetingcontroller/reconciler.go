// Package greetingcontroller deploys, for each Greeting, the first-party hello
// app behind a third-party nginx proxy.
package greetingcontroller

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	demov1 "github.com/rosenhouse/capi-dagger-dev-env/examples/greeting/api/v1alpha1"
)

//go:generate go tool controller-gen rbac:roleName=greeting-controller paths=./... output:rbac:dir=../../config/greeting-controller

// +kubebuilder:rbac:groups=demo.example.com,resources=greetings,verbs=get;list;watch
// +kubebuilder:rbac:groups=demo.example.com,resources=greetings/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update
// +kubebuilder:rbac:groups="",resources=services;configmaps,verbs=get;list;watch;create;update

type Reconciler struct {
	client.Client
	HelloImage string
	ProxyImage string
}

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&demov1.Greeting{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.ConfigMap{}).
		Complete(r)
}

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	g := &demov1.Greeting{}
	if err := r.Get(ctx, req.NamespacedName, g); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	hello := g.Name + "-hello"
	proxy := g.Name + "-proxy"
	proxyConf := fmt.Sprintf("server {\n  listen 8080;\n  location / {\n    proxy_pass http://%s:8080;\n  }\n}\n", hello)

	cm := &corev1.ConfigMap{}
	cm.Name, cm.Namespace = proxy, g.Namespace
	if err := r.apply(ctx, g, cm, func() { cm.Data = map[string]string{"default.conf": proxyConf} }); err != nil {
		return ctrl.Result{}, err
	}

	helloContainer := corev1.Container{
		Name:  "hello",
		Image: r.HelloImage,
		Env:   []corev1.EnvVar{{Name: "GREETING", Value: g.Spec.Message}},
		Ports: []corev1.ContainerPort{{ContainerPort: 8080}},
	}
	if err := r.deployment(ctx, g, hello, helloContainer, nil); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.service(ctx, g, hello, 8080); err != nil {
		return ctrl.Result{}, err
	}

	proxyContainer := corev1.Container{
		Name:         "nginx",
		Image:        r.ProxyImage,
		Ports:        []corev1.ContainerPort{{ContainerPort: 8080}},
		VolumeMounts: []corev1.VolumeMount{{Name: "conf", MountPath: "/etc/nginx/conf.d"}},
	}
	conf := corev1.Volume{Name: "conf", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
		LocalObjectReference: corev1.LocalObjectReference{Name: proxy},
	}}}
	if err := r.deployment(ctx, g, proxy, proxyContainer, []corev1.Volume{conf}); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, r.service(ctx, g, proxy, 80)
}

func (r *Reconciler) deployment(ctx context.Context, g *demov1.Greeting, name string, c corev1.Container, volumes []corev1.Volume) error {
	d := &appsv1.Deployment{}
	d.Name, d.Namespace = name, g.Namespace
	return r.apply(ctx, g, d, func() {
		d.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels(name)}
		d.Spec.Template.Labels = labels(name)
		d.Spec.Template.Spec.Containers = []corev1.Container{c}
		d.Spec.Template.Spec.Volumes = volumes
	})
}

func (r *Reconciler) service(ctx context.Context, g *demov1.Greeting, name string, port int32) error {
	s := &corev1.Service{}
	s.Name, s.Namespace = name, g.Namespace
	return r.apply(ctx, g, s, func() {
		s.Spec.Selector = labels(name)
		s.Spec.Ports = []corev1.ServicePort{{Port: port, TargetPort: intstr.FromInt32(8080)}}
	})
}

func (r *Reconciler) apply(ctx context.Context, g *demov1.Greeting, obj client.Object, mutate func()) error {
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, obj, func() error {
		mutate()
		return controllerutil.SetControllerReference(g, obj, r.Scheme())
	})
	return err
}

func labels(name string) map[string]string {
	return map[string]string{"app.kubernetes.io/name": name}
}
