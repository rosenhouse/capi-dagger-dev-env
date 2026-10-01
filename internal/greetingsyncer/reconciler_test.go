package greetingsyncer_test

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/cluster-api/controllers/clustercache"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	demov1 "github.com/rosenhouse/capi-dagger-dev-env/api/v1alpha1"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/greetingsyncer"
)

func TestCopiesGreetingToItsCluster(t *testing.T) {
	remote := &fakeRemote{client: newClient()}
	r := reconciler(remote, greeting("work", "hi"))

	res := reconcile(t, r)

	if remote.requested != (client.ObjectKey{Namespace: "team-a", Name: "work"}) {
		t.Errorf("requested cluster %v", remote.requested)
	}
	copied := &demov1.Greeting{}
	if err := remote.client.Get(context.Background(), types.NamespacedName{Namespace: "greetings", Name: "g1"}, copied); err != nil {
		t.Fatal(err)
	}
	if copied.Spec != (demov1.GreetingSpec{Message: "hi"}) {
		t.Errorf("copied spec = %+v", copied.Spec)
	}
	if res.RequeueAfter != 0 {
		t.Errorf("requeued after %v", res.RequeueAfter)
	}
}

func TestUpdatesExistingCopy(t *testing.T) {
	old := &demov1.Greeting{ObjectMeta: metav1.ObjectMeta{Namespace: "greetings", Name: "g1"}, Spec: demov1.GreetingSpec{Message: "old"}}
	remote := &fakeRemote{client: newClient(old)}

	reconcile(t, reconciler(remote, greeting("work", "new")))

	copied := &demov1.Greeting{}
	_ = remote.client.Get(context.Background(), client.ObjectKeyFromObject(old), copied)
	if copied.Spec.Message != "new" {
		t.Errorf("message = %q, want new", copied.Spec.Message)
	}
}

func TestRequeuesUntilClusterIsConnected(t *testing.T) {
	remote := &fakeRemote{err: clustercache.ErrClusterNotConnected}

	if res := reconcile(t, reconciler(remote, greeting("work", "hi"))); res.RequeueAfter == 0 {
		t.Error("did not requeue")
	}
}

func TestRequeuesUntilGreetingCRDIsInstalled(t *testing.T) {
	noCRD := fake.NewClientBuilder().WithScheme(scheme()).WithInterceptorFuncs(interceptor.Funcs{
		Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
			return &meta.NoKindMatchError{GroupKind: demov1.GroupVersion.WithKind("Greeting").GroupKind()}
		},
	}).Build()

	if res := reconcile(t, reconciler(&fakeRemote{client: noCRD}, greeting("work", "hi"))); res.RequeueAfter == 0 {
		t.Error("did not requeue")
	}
}

func TestIgnoresGreetingWithoutCluster(t *testing.T) {
	remote := &fakeRemote{client: newClient()}

	reconcile(t, reconciler(remote, greeting("", "hi")))

	if remote.requested != (client.ObjectKey{}) {
		t.Errorf("requested cluster %v", remote.requested)
	}
}

type fakeRemote struct {
	client    client.Client
	err       error
	requested client.ObjectKey
}

func (f *fakeRemote) GetUncachedClient(_ context.Context, cluster client.ObjectKey) (client.Client, error) {
	f.requested = cluster
	return f.client, f.err
}

func greeting(cluster, message string) *demov1.Greeting {
	return &demov1.Greeting{
		ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "g1"},
		Spec:       demov1.GreetingSpec{ClusterName: cluster, Message: message},
	}
}

func reconciler(remote *fakeRemote, g *demov1.Greeting) *greetingsyncer.Reconciler {
	return &greetingsyncer.Reconciler{Client: newClient(g), Remote: remote, TargetNamespace: "greetings"}
}

func reconcile(t *testing.T, r *greetingsyncer.Reconciler) ctrl.Result {
	t.Helper()
	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "team-a", Name: "g1"}})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func scheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = demov1.AddToScheme(s)
	return s
}

func newClient(objs ...client.Object) client.Client {
	return fake.NewClientBuilder().WithScheme(scheme()).WithObjects(objs...).Build()
}
