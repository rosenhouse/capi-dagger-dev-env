package e2e

import (
	"context"
	"os"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// The suite needs MGMT_KUBECONFIG and WORKLOAD_KUBECONFIG.
var mgmt, workload client.Client

func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "fleet-addons e2e")
}

var _ = BeforeSuite(func() {
	mgmt = clientFor(os.Getenv("MGMT_KUBECONFIG"))
	workload = clientFor(os.Getenv("WORKLOAD_KUBECONFIG"))
})

func clientFor(path string) client.Client {
	Expect(path).NotTo(BeEmpty())
	cfg, err := clientcmd.BuildConfigFromFlags("", path)
	Expect(err).NotTo(HaveOccurred())
	c, err := client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
	return c
}

var _ = Describe("fleet-addons", func() {
	It("writes fleet-info into the workload cluster", func(ctx context.Context) {
		cm := &corev1.ConfigMap{}
		Eventually(func() error {
			return workload.Get(ctx, client.ObjectKey{Namespace: "fleet-system", Name: "fleet-info"}, cm)
		}).WithTimeout(3 * time.Minute).Should(Succeed())
		GinkgoWriter.Printf("fleet-info version=%q\n", cm.Data["version"])
		if want := os.Getenv("WANT_VERSION"); want != "" {
			Expect(cm.Data["version"]).To(Equal(want))
		}
	})

	It("rejects a FleetPolicy without a message", func(ctx context.Context) {
		p := &unstructured.Unstructured{}
		p.SetGroupVersionKind(schema.GroupVersionKind{Group: "fleet.acme.io", Version: "v1alpha1", Kind: "FleetPolicy"})
		p.SetNamespace("default")
		p.SetName("bad")
		p.Object["spec"] = map[string]any{"message": ""}
		err := mgmt.Create(ctx, p)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("spec.message must not be empty"))
	})
})
