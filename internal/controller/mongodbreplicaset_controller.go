/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	api "github.com/nunnatsa/mongodb-operator/api/v1alpha1"
	"github.com/nunnatsa/mongodb-operator/internal/mongohelper"
)

var (
	dbNameLabel      = api.GroupVersion.Group + "/name"
	mongoDBFinalizer = api.GroupVersion.Group + "/mongoDBFinalizer"
)

// MongoDBReplicaSetReconciler reconciles a MongoDBReplicaSet object
type MongoDBReplicaSetReconciler struct {
	client.Client
	Scheme        *runtime.Scheme
	MDBImage      string
	EventRecorder record.EventRecorder
}

//+kubebuilder:rbac:groups=mongodb.example.com,resources=mongodbreplicasets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=mongodb.example.com,resources=mongodbreplicasets/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=mongodb.example.com,resources=mongodbreplicasets/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=services;persistentvolumeclaims;,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events;,verbs=create
// +kubebuilder:rbac:groups="apps",resources=statefulsets;,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.15.0/pkg/reconcile
func (r *MongoDBReplicaSetReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	mrs := &api.MongoDBReplicaSet{}

	err := r.Get(ctx, client.ObjectKey{Namespace: req.Namespace, Name: req.Name}, mrs)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// already deleted; ignore
			return ctrl.Result{}, nil
		}

		// unknown error - retry
		return ctrl.Result{}, err
	}

	if !controllerutil.ContainsFinalizer(mrs, mongoDBFinalizer) {
		if mrs.DeletionTimestamp == nil {
			logger.Info("adding finalizer")
			controllerutil.AddFinalizer(mrs, mongoDBFinalizer)
		}
	} else if mrs.DeletionTimestamp != nil {
		err = r.ensureDeletion(ctx, mrs)
		if err != nil {
			logger.Error(err, "failed to ensure deletion")
			return ctrl.Result{}, fmt.Errorf("failed to delete resources; %w", err)
		}

		logger.Info("removing the finalizer")
		controllerutil.RemoveFinalizer(mrs, mongoDBFinalizer)

		return ctrl.Result{}, r.Update(ctx, mrs)
	}

	var errList []error
	err = r.ensureStatefulSet(ctx, mrs, logger)
	if err != nil {
		errList = append(errList, err)
		logger.Error(err, "failed to ensure StatefulSet", "mongodb index", mrs.Name)
	}

	err = r.ensureService(ctx, mrs)
	if err != nil {
		errList = append(errList, err)
		logger.Error(err, "failed to ensure Service")
	}

	if len(errList) > 0 {
		return ctrl.Result{}, errors.NewAggregate(errList)
	}

	replicas := getReplicas(mrs)
	status, err := mongohelper.EnsureMongoDBReplicaSet(ctx, mrs, replicas, logger)
	if err != nil {
		logger.Error(err, "failed to ensure the replicaSet")
		return ctrl.Result{}, err
	}
	logger.Info(fmt.Sprintf("RS Status: %v", status))

	meta.SetStatusCondition(&mrs.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		Reason:             "creatingStatefulSet",
		Message:            fmt.Sprintf("the %s replica set is ready", mrs.Name),
		ObservedGeneration: mrs.Generation,
	})

	url := fmt.Sprintf("mongodb://%s:%d?replicaSet=%s", mrs.Name, mrs.Spec.Port, mrs.Name)
	mrs.Status.ConnectionURL = ptr.To(url)
	mrs.Status.Replicas = ptr.To(replicas)

	_ = r.Status().Update(ctx, mrs)

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *MongoDBReplicaSetReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&api.MongoDBReplicaSet{}).
		Watches(
			&appsv1.StatefulSet{},
			handler.EnqueueRequestsFromMapFunc(objectToRequest),
			builder.WithPredicates(getLabelPredicate()),
		).
		Watches(
			&corev1.Service{},
			handler.EnqueueRequestsFromMapFunc(objectToRequest),
			builder.WithPredicates(getLabelPredicate()),
		).Complete(r)
}

func objectToRequest(_ context.Context, o client.Object) []reconcile.Request {
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: o.GetNamespace(), Name: o.GetName()}}}
}

func getLabelPredicate() predicate.Predicate {
	p, _ := predicate.LabelSelectorPredicate(
		metav1.LabelSelector{
			MatchExpressions: []metav1.LabelSelectorRequirement{{
				Key:      dbNameLabel,
				Operator: metav1.LabelSelectorOpExists,
				Values:   nil,
			}},
		})
	return p
}

func (r *MongoDBReplicaSetReconciler) getStatefulSet(ctx context.Context, mrs *api.MongoDBReplicaSet) (*appsv1.StatefulSet, error) {
	deploy := &appsv1.StatefulSet{}
	err := r.Get(ctx, client.ObjectKey{Namespace: mrs.Namespace, Name: mrs.Name}, deploy)
	return deploy, err
}

func (r *MongoDBReplicaSetReconciler) ensureStatefulSet(ctx context.Context, mrs *api.MongoDBReplicaSet, logger logr.Logger) error {
	logger.Info("ensuring StatefulSet", "StatefulSet name", mrs.Name)

	set, err := r.getStatefulSet(ctx, mrs)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("creating new StatefulSet", "name", mrs.Name)
			err = r.createNewStatefulSet(ctx, mrs)
			if err != nil {
				return fmt.Errorf("failed to create StatefulSet; %w", err)
			}

			logger.Info("StatefulSet created", "name", mrs.Name)
			r.EventRecorder.Event(mrs, corev1.EventTypeNormal, "Created", "Created StatefulSet "+mrs.Name)
			return nil
		} else {
			return fmt.Errorf("failed to get StatefulSet; %w", err)
		}
	}
	updated, err := r.updateStatefulSet(ctx, set, mrs)
	if err != nil {
		return fmt.Errorf("failed to update StatefulSet; %w", err)
	}

	if updated {
		logger.Info("StatefulSet updated", "name", mrs.Name)
		r.EventRecorder.Event(mrs, corev1.EventTypeNormal, "Updated", "Updated StatefulSet "+mrs.Name)
	}

	return nil
}

func (r *MongoDBReplicaSetReconciler) getService(ctx context.Context, mrs *api.MongoDBReplicaSet) (*corev1.Service, error) {
	svc := &corev1.Service{}
	err := r.Get(ctx, client.ObjectKey{Namespace: mrs.Namespace, Name: mrs.Name}, svc)
	return svc, err
}

func (r *MongoDBReplicaSetReconciler) ensureService(ctx context.Context, mrs *api.MongoDBReplicaSet) error {
	svc, err := r.getService(ctx, mrs)
	if err != nil {
		if apierrors.IsNotFound(err) {
			err = r.createNewService(ctx, mrs)
			if err != nil {
				return fmt.Errorf("failed to create service; %w", err)
			}

			r.EventRecorder.Event(mrs, corev1.EventTypeNormal, "Created", "Created Service "+mrs.Name)

			return nil
		}
	}

	updated, err := r.updateService(ctx, svc, mrs)
	if err != nil {
		return fmt.Errorf("failed to update Service; %w", err)
	}

	if updated {
		r.EventRecorder.Event(mrs, corev1.EventTypeNormal, "Updated", "Updated Service "+mrs.Name)
	}

	return nil
}

func (r *MongoDBReplicaSetReconciler) createNewService(ctx context.Context, mrs *api.MongoDBReplicaSet) error {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: mrs.Namespace,
			Name:      mrs.Name,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "v1alpha1",
					Kind:       "MongoDB",
					Name:       mrs.Name,
					UID:        mrs.UID,
				},
			},
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{
				dbNameLabel: nameLabelValue(mrs),
			},
			ClusterIP: corev1.ClusterIPNone,
			Ports: []corev1.ServicePort{
				{
					Port:       mrs.Spec.Port,
					TargetPort: intstr.FromInt32(mongohelper.MongoDbPort),
				},
			},
		},
	}

	return r.Create(ctx, svc, &client.CreateOptions{})
}

func (r *MongoDBReplicaSetReconciler) updateService(_ context.Context, _ *corev1.Service, _ *api.MongoDBReplicaSet) (bool, error) {
	// todo: implement
	return false, nil
}

func (r *MongoDBReplicaSetReconciler) createNewStatefulSet(ctx context.Context, mrs *api.MongoDBReplicaSet) error {
	label := nameLabelValue(mrs)

	replicas := getReplicas(mrs)

	set := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: mrs.Namespace,
			Name:      mrs.Name,
			Labels: map[string]string{
				dbNameLabel: label,
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "v1alpha1",
					Kind:       "MongoDB",
					Name:       mrs.Name,
					UID:        mrs.UID,
				},
			},
		},
		Spec: appsv1.StatefulSetSpec{
			ServiceName: mrs.Name,
			Replicas:    &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					dbNameLabel: label,
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						dbNameLabel: label,
					},
				},
				Spec: corev1.PodSpec{
					TerminationGracePeriodSeconds: ptr.To[int64](30),
					Containers: []corev1.Container{
						{
							Name:    mrs.Name,
							Image:   r.MDBImage,
							Command: []string{"mongod"},
							Args: []string{
								fmt.Sprintf("--replSet=%s", mrs.Name),
								"--bind_ip_all",
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      mrs.Name + "-storage",
									MountPath: "/data/db",
								},
							},
							Ports: []corev1.ContainerPort{
								{
									ContainerPort: mongohelper.MongoDbPort,
								},
							},
						},
					},
				},
			},
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: mrs.Name + "-storage",
					},
					Spec: corev1.PersistentVolumeClaimSpec{
						AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{
								"storage": resource.MustParse("2Gi"),
							},
						},
						StorageClassName: mrs.Spec.StorageClassName,
						VolumeMode:       ptr.To(corev1.PersistentVolumeFilesystem),
					},
				},
			},
		},
	}

	meta.SetStatusCondition(&mrs.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		Reason:             "creatingStatefulSet",
		Message:            "creating the mongoDB StatefulSet",
		ObservedGeneration: mrs.Generation,
	})

	_ = r.Status().Update(ctx, mrs)

	return r.Create(ctx, set, &client.CreateOptions{})
}

func (r *MongoDBReplicaSetReconciler) updateStatefulSet(ctx context.Context, set *appsv1.StatefulSet, mrs *api.MongoDBReplicaSet) (bool, error) {
	changed := false

	replicas := getReplicas(mrs)
	pvcsToRemove := int32(0)

	if *set.Spec.Replicas != replicas {
		changed = true
		if *set.Spec.Replicas > replicas {
			pvcsToRemove = *set.Spec.Replicas - replicas
		}

		set.Spec.Replicas = &replicas
	}

	if len(set.Spec.Template.Spec.Containers) != 1 {
		changed = true
		set.Spec.Template.Spec.Containers = []corev1.Container{
			{
				Image: r.MDBImage,
			},
		}
	} else {
		if set.Spec.Template.Spec.Containers[0].Image != r.MDBImage {
			changed = true
			set.Spec.Template.Spec.Containers[0].Image = r.MDBImage
		}
	}

	if changed {
		meta.SetStatusCondition(&mrs.Status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			Reason:             "updatingReplicaSet",
			Message:            "updating the mongoDB replica set",
			ObservedGeneration: mrs.Generation,
		})

		_ = r.Status().Update(ctx, mrs)

		err := r.Update(ctx, set, &client.UpdateOptions{})
		if err != nil {
			return false, err
		}

		if pvcsToRemove > 0 {
			for node := int32(0); node < pvcsToRemove; node++ {
				err = r.deletePVC(ctx, mrs, node+replicas)
				if err != nil {
					return false, err
				}
			}
		}
	}

	status := metav1.ConditionFalse
	reason := "notReady"
	message := "not enough available pods"
	if set.Status.AvailableReplicas == replicas {
		status = metav1.ConditionTrue
		reason = "Ready"
		message = "mongoDB is available"
	}
	meta.SetStatusCondition(&mrs.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: mrs.Generation,
	})

	_ = r.Update(ctx, mrs, &client.UpdateOptions{})

	return changed, nil
}

func (r *MongoDBReplicaSetReconciler) deleteStatefulSet(ctx context.Context, mrs *api.MongoDBReplicaSet) error {
	set, err := r.getStatefulSet(ctx, mrs)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}

	return r.Delete(ctx, set, &client.DeleteOptions{})
}

func (r *MongoDBReplicaSetReconciler) deleteService(ctx context.Context, mrs *api.MongoDBReplicaSet) error {
	svc, err := r.getService(ctx, mrs)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}

	return r.Delete(ctx, svc, &client.DeleteOptions{})
}

func (r *MongoDBReplicaSetReconciler) deletePVC(ctx context.Context, mrs *api.MongoDBReplicaSet, node int32) error {
	name := fmt.Sprintf("%s-storage-%s-%d", mrs.Name, mrs.Name, node)
	pvc, err := r.getPVC(ctx, mrs, name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}

	err = r.Delete(ctx, pvc, &client.DeleteOptions{})
	if err != nil {
		return err
	}

	r.EventRecorder.Event(mrs, corev1.EventTypeNormal, "Deleted", "Deleted PersistentVolumeClaim "+pvc.Name)

	return nil
}

func (r *MongoDBReplicaSetReconciler) getPVC(ctx context.Context, mrs *api.MongoDBReplicaSet, name string) (*corev1.PersistentVolumeClaim, error) {
	pvc := &corev1.PersistentVolumeClaim{}
	err := r.Get(ctx, client.ObjectKey{Namespace: mrs.Namespace, Name: name}, pvc)
	return pvc, err
}

func (r *MongoDBReplicaSetReconciler) ensureDeletion(ctx context.Context, mrs *api.MongoDBReplicaSet) error {
	err := r.deleteService(ctx, mrs)
	if err != nil {
		return err
	}
	r.EventRecorder.Event(mrs, corev1.EventTypeNormal, "Deleted", "Deleted Service "+mrs.Name)

	err = r.deleteStatefulSet(ctx, mrs)
	if err != nil {
		return err
	}
	r.EventRecorder.Event(mrs, corev1.EventTypeNormal, "Deleted", "Deleted StatefulSet "+mrs.Name)

	replicas := getReplicas(mrs)

	for i := int32(0); i < replicas; i++ {
		err = r.deletePVC(ctx, mrs, i)
		if err != nil {
			return err
		}
	}

	return nil
}

func nameLabelValue(mrs *api.MongoDBReplicaSet) string {
	return fmt.Sprintf("%s_%s", mrs.Namespace, mrs.Name)
}

func getReplicas(mrs *api.MongoDBReplicaSet) int32 {
	if mrs.Spec.Replicas != nil {
		return *mrs.Spec.Replicas
	}
	return int32(3)
}
