package controllers

import (
	"context"

	api "github.com/nunnatsa/mongodb-operator/api/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type eventRecorderMock struct{}

func (*eventRecorderMock) Event(object runtime.Object, eventtype, reason, message string) {}
func (*eventRecorderMock) Eventf(object runtime.Object, eventtype, reason, messageFmt string, args ...interface{}) {
}
func (*eventRecorderMock) AnnotatedEventf(object runtime.Object, annotations map[string]string, eventtype, reason, messageFmt string, args ...interface{}) {
}

var _ = Describe("test the controller", func() {
	It("should create a deployment and a service", func() {
		mdb := &api.MongoDB{
			TypeMeta: metav1.TypeMeta{APIVersion: "mongodb.example.com/v1alpha1", Kind: "MongoDB"},
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "default",
				Name:      "testdb",
			},
		}
		client := fake.NewClientBuilder().WithObjects(mdb).WithScheme(setupScheme()).Build()

		r := MongoDBReconciler{
			Client:        client,
			Scheme:        setupScheme(),
			MDBImage:      "image-name",
			EventRecorder: &eventRecorderMock{},
		}

		res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "testdb"}})
		Expect(err).To(HaveOccurred())
		Expect(res.IsZero()).To(BeTrue())
	})
})

func setupScheme() *runtime.Scheme {
	s := runtime.NewScheme()

	if err := api.AddToScheme(s); err != nil {
		panic(err)
	}
	if err := appsv1.AddToScheme(s); err != nil {
		panic(err)
	}
	if err := corev1.AddToScheme(s); err != nil {
		panic(err)
	}

	return s
}
