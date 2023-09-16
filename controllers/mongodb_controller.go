/*
Copyright 2022.

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

package controllers

import (
	"context"
	"fmt"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	"github.com/go-logr/logr"
	"github.com/nunnatsa/mongodb-operator/pkg/mongohelper"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/pointer"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	api "github.com/nunnatsa/mongodb-operator/api/v1alpha1"
)

const (
	defaultServicePort = int32(mongohelper.MongoDbPort)

	dbNameLabel      = api.Group + "/name"
	mongoDBFinalizer = api.Group + "/mongoDBFinalizer"
)

// MongoDBReconciler reconciles a MongoDB object
type MongoDBReconciler struct {
	client.Client
	Scheme        *runtime.Scheme
	MDBImage      string
	EventRecorder record.EventRecorder
}

//+kubebuilder:rbac:groups=mongodb.example.com,resources=mongodbs,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=mongodb.example.com,resources=mongodbs/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=mongodb.example.com,resources=mongodbs/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=services;persistentvolumeclaims;,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events;,verbs=create
// +kubebuilder:rbac:groups="apps",resources=statefulsets;,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *MongoDBReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	mdb := &api.MongoDB{}

	err := r.Get(ctx, client.ObjectKey{Namespace: req.Namespace, Name: req.Name}, mdb)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// already deleted; ignore
			return ctrl.Result{}, nil
		}

		// unknown error - retry
		return ctrl.Result{}, err
	}

	if !controllerutil.ContainsFinalizer(mdb, mongoDBFinalizer) {
		if mdb.DeletionTimestamp == nil {
			logger.Info("adding finalizer")
			controllerutil.AddFinalizer(mdb, mongoDBFinalizer)
		}
	} else if mdb.DeletionTimestamp != nil {
		err = r.ensureDeletion(ctx, mdb)
		if err != nil {
			logger.Error(err, "failed to ensure deletion")
			return ctrl.Result{}, fmt.Errorf("failed to delete resources; %w", err)
		}

		logger.Info("removing the finalizer")
		controllerutil.RemoveFinalizer(mdb, mongoDBFinalizer)

		return ctrl.Result{}, r.Update(ctx, mdb)
	}

	var lastError error
	err = r.ensureStatefulSet(ctx, mdb, logger)
	if err != nil {
		lastError = err
		logger.Error(err, "failed to ensure StatefulSet", "mongodb index", mdb.Name)
	}

	err = r.ensureService(ctx, mdb)
	if err != nil {
		lastError = err
		logger.Error(err, "failed to ensure Service")
	}

	if lastError != nil {
		return ctrl.Result{}, lastError
	}

	replicas := getReplicas(mdb)
	status, err := mongohelper.EnsureMongoDBReplicaSet(ctx, mdb, replicas, logger)
	if err != nil {
		logger.Error(err, "failed to ensure the replicaSet")
		return ctrl.Result{}, err
	}
	logger.Info(fmt.Sprintf("RS Status: %v", status))

	meta.SetStatusCondition(&mdb.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		Reason:             "creatingStatefulSet",
		Message:            fmt.Sprintf("the %s replica set is ready", mdb.Name),
		ObservedGeneration: mdb.Generation,
	})

	url := fmt.Sprintf("mongodb://%s:%d?replicaSet=%s", mdb.Name, getPort(mdb), mdb.Name)
	mdb.Status.ConnectionURL = pointer.String(url)
	mdb.Status.Replicas = pointer.Int32(replicas)

	_ = r.Status().Update(ctx, mdb)

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *MongoDBReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&api.MongoDB{}).
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

func (r *MongoDBReconciler) getStatefulSet(ctx context.Context, mdb *api.MongoDB) (*appsv1.StatefulSet, error) {
	deploy := &appsv1.StatefulSet{}
	err := r.Get(ctx, client.ObjectKey{Namespace: mdb.Namespace, Name: mdb.Name}, deploy)
	return deploy, err
}

func (r *MongoDBReconciler) ensureStatefulSet(ctx context.Context, mdb *api.MongoDB, logger logr.Logger) error {
	logger.Info("ensuring StatefulSet", "StatefulSet name", mdb.Name)

	set, err := r.getStatefulSet(ctx, mdb)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("creating new StatefulSet", "name", mdb.Name)
			err = r.createNewStatefulSet(ctx, mdb)
			if err != nil {
				return fmt.Errorf("failed to create StatefulSet; %w", err)
			}

			logger.Info("StatefulSet created", "name", mdb.Name)
			r.EventRecorder.Event(mdb, corev1.EventTypeNormal, "Created", "Created StatefulSet "+mdb.Name)
			return nil
		} else {
			return fmt.Errorf("failed to get StatefulSet; %w", err)
		}
	}
	updated, err := r.updateStatefulSet(ctx, set, mdb)
	if err != nil {
		return fmt.Errorf("failed to update StatefulSet; %w", err)
	}

	if updated {
		logger.Info("StatefulSet updated", "name", mdb.Name)
		r.EventRecorder.Event(mdb, corev1.EventTypeNormal, "Updated", "Updated StatefulSet "+mdb.Name)
	}

	return nil
}

func (r *MongoDBReconciler) getService(ctx context.Context, mdb *api.MongoDB) (*corev1.Service, error) {
	svc := &corev1.Service{}
	err := r.Get(ctx, client.ObjectKey{Namespace: mdb.Namespace, Name: mdb.Name}, svc)
	return svc, err
}

func (r *MongoDBReconciler) ensureService(ctx context.Context, mdb *api.MongoDB) error {
	svc, err := r.getService(ctx, mdb)
	if err != nil {
		if apierrors.IsNotFound(err) {
			err = r.createNewService(ctx, mdb)
			if err != nil {
				return fmt.Errorf("failed to create service; %w", err)
			}

			r.EventRecorder.Event(mdb, corev1.EventTypeNormal, "Created", "Created Service "+mdb.Name)

			return nil
		}
	}

	updated, err := r.updateService(ctx, svc, mdb)
	if err != nil {
		return fmt.Errorf("failed to update Service; %w", err)
	}

	if updated {
		r.EventRecorder.Event(mdb, corev1.EventTypeNormal, "Updated", "Updated Service "+mdb.Name)
	}

	return nil
}

func (r *MongoDBReconciler) createNewService(ctx context.Context, mdb *api.MongoDB) error {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: mdb.Namespace,
			Name:      mdb.Name,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "v1alpha1",
					Kind:       "MongoDB",
					Name:       mdb.Name,
					UID:        mdb.UID,
				},
			},
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{
				dbNameLabel: nameLabelValue(mdb),
			},
			ClusterIP: corev1.ClusterIPNone,
			Ports: []corev1.ServicePort{
				{
					Port:       getPort(mdb),
					TargetPort: intstr.FromInt(mongohelper.MongoDbPort),
				},
			},
		},
	}

	return r.Create(ctx, svc, &client.CreateOptions{})
}

func (r *MongoDBReconciler) updateService(_ context.Context, _ *corev1.Service, _ *api.MongoDB) (bool, error) {
	// todo: implement
	return false, nil
}

func (r *MongoDBReconciler) createNewStatefulSet(ctx context.Context, mdb *api.MongoDB) error {
	label := nameLabelValue(mdb)

	replicas := getReplicas(mdb)

	vmode := corev1.PersistentVolumeFilesystem

	set := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: mdb.Namespace,
			Name:      mdb.Name,
			Labels: map[string]string{
				dbNameLabel: label,
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "v1alpha1",
					Kind:       "MongoDB",
					Name:       mdb.Name,
					UID:        mdb.UID,
				},
			},
		},
		Spec: appsv1.StatefulSetSpec{
			ServiceName: mdb.Name,
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
					TerminationGracePeriodSeconds: pointer.Int64(30),
					Containers: []corev1.Container{
						{
							Name:    mdb.Name,
							Image:   r.MDBImage,
							Command: []string{"mongod"},
							Args: []string{
								fmt.Sprintf("--replSet=%s", mdb.Name),
								"--bind_ip_all",
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      mdb.Name + "-storage",
									MountPath: "/data/db",
								},
							},
							Ports: []corev1.ContainerPort{
								{
									ContainerPort: defaultServicePort,
								},
							},
						},
					},
				},
			},
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: mdb.Name + "-storage",
					},
					Spec: corev1.PersistentVolumeClaimSpec{
						AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								"storage": resource.MustParse("2Gi"),
							},
						},
						StorageClassName: mdb.Spec.StorageClassName,
						VolumeMode:       &vmode,
					},
				},
			},
		},
	}

	meta.SetStatusCondition(&mdb.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		Reason:             "creatingStatefulSet",
		Message:            "creating the mongoDB StatefulSet",
		ObservedGeneration: mdb.Generation,
	})

	_ = r.Status().Update(ctx, mdb)

	return r.Create(ctx, set, &client.CreateOptions{})
}

func (r *MongoDBReconciler) updateStatefulSet(ctx context.Context, set *appsv1.StatefulSet, mdb *api.MongoDB) (bool, error) {
	changed := false

	replicas := getReplicas(mdb)
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
		meta.SetStatusCondition(&mdb.Status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			Reason:             "updatingReplicaSet",
			Message:            "updating the mongoDB replica set",
			ObservedGeneration: mdb.Generation,
		})

		_ = r.Status().Update(ctx, mdb)

		err := r.Update(ctx, set, &client.UpdateOptions{})
		if err != nil {
			return false, err
		}

		if pvcsToRemove > 0 {
			for node := int32(0); node < pvcsToRemove; node++ {
				err = r.deletePVC(ctx, mdb, node+replicas)
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
	meta.SetStatusCondition(&mdb.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: mdb.Generation,
	})

	_ = r.Update(ctx, mdb, &client.UpdateOptions{})

	return changed, nil
}

func (r *MongoDBReconciler) ensureDeletion(ctx context.Context, mdb *api.MongoDB) error {
	err := r.deleteService(ctx, mdb)
	if err != nil {
		return err
	}
	r.EventRecorder.Event(mdb, corev1.EventTypeNormal, "Deleted", "Deleted Service "+mdb.Name)

	err = r.deleteStatefulSet(ctx, mdb)
	if err != nil {
		return err
	}
	r.EventRecorder.Event(mdb, corev1.EventTypeNormal, "Deleted", "Deleted StatefulSet "+mdb.Name)

	replicas := getReplicas(mdb)

	for i := int32(0); i < replicas; i++ {
		err = r.deletePVC(ctx, mdb, i)
		if err != nil {
			return err
		}
	}

	return nil
}

func (r *MongoDBReconciler) deleteStatefulSet(ctx context.Context, mdb *api.MongoDB) error {
	set, err := r.getStatefulSet(ctx, mdb)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}

	return r.Delete(ctx, set, &client.DeleteOptions{})
}

func (r *MongoDBReconciler) deleteService(ctx context.Context, mdb *api.MongoDB) error {
	svc, err := r.getService(ctx, mdb)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}

	return r.Delete(ctx, svc, &client.DeleteOptions{})
}

func (r *MongoDBReconciler) deletePVC(ctx context.Context, mdb *api.MongoDB, node int32) error {
	name := fmt.Sprintf("%s-storage-%s-%d", mdb.Name, mdb.Name, node)
	pvc, err := r.getPVC(ctx, mdb, name)
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

	r.EventRecorder.Event(mdb, corev1.EventTypeNormal, "Deleted", "Deleted PersistentVolumeClaim "+pvc.Name)

	return nil
}

func (r *MongoDBReconciler) getPVC(ctx context.Context, mdb *api.MongoDB, name string) (*corev1.PersistentVolumeClaim, error) {
	pvc := &corev1.PersistentVolumeClaim{}
	err := r.Get(ctx, client.ObjectKey{Namespace: mdb.Namespace, Name: name}, pvc)
	return pvc, err
}

func nameLabelValue(mdb *api.MongoDB) string {
	return fmt.Sprintf("%s_%s", mdb.Namespace, mdb.Name)
}

func getReplicas(mdb *api.MongoDB) int32 {
	if mdb.Spec.Replicas != nil {
		return *mdb.Spec.Replicas
	}
	return int32(3)
}

func getPort(mdb *api.MongoDB) int32 {
	if mdb.Spec.Port == 0 {
		return defaultServicePort
	}
	return mdb.Spec.Port
}
