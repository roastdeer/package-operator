package objectsets

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	corev1alpha1 "package-operator.run/apis/core/v1alpha1"

	"package-operator.run/package-operator/internal/controllers"
)

// Reconciles ObjectSetPhase objects for the parent ObjectSet.
type objectSetRemotePhaseReconciler struct {
	client            client.Client
	scheme            *runtime.Scheme
	newObjectSetPhase genericObjectSetPhaseFactory
}

func newObjectSetRemotePhaseReconciler(
	client client.Client,
	scheme *runtime.Scheme,
	newObjectSetPhase genericObjectSetPhaseFactory,
) *objectSetRemotePhaseReconciler {
	return &objectSetRemotePhaseReconciler{
		client:            client,
		scheme:            scheme,
		newObjectSetPhase: newObjectSetPhase,
	}
}

const noStatusProbeFailure = "no status reported"

// Teardown just ensures the remote ObjectSetPhase object
// has been deleted from the cluster.
func (r *objectSetRemotePhaseReconciler) Teardown(
	ctx context.Context, objectSet genericObjectSet,
	phase corev1alpha1.ObjectSetTemplatePhase,
) (cleanupDone bool, err error) {
	log := logr.FromContextOrDiscard(ctx)

	defer log.Info("teardown of remote phase", "phase", phase.Name, "cleanupDone", cleanupDone)
	objectSetPhase := r.newObjectSetPhase(r.scheme)
	err = r.client.Get(ctx, client.ObjectKey{
		Name:      objectSetPhaseName(objectSet, phase),
		Namespace: objectSet.ClientObject().GetNamespace(),
	}, objectSetPhase.ClientObject())
	if err != nil && errors.IsNotFound(err) {
		// object is already gone -> nothing to cleanup
		return true, nil
	}

	// If object has a namespace check if it is already in the process of being deleted.
	// If so, remove finalizer from object to let it go.
	if len(objectSet.ClientObject().GetNamespace()) != 0 {
		ns := corev1.Namespace{}

		if err := r.client.Get(ctx, client.ObjectKey{Name: objectSet.ClientObject().GetNamespace()}, &ns); err != nil {
			return false, err
		}

		if !ns.DeletionTimestamp.IsZero() {
			log.Info("removing finalizer from object since containing namespace in deletion")

			objectSetPhase.ClientObject().SetFinalizers(nil)
			if err := r.client.Update(ctx, objectSetPhase.ClientObject()); err != nil {
				return false, err
			}
		}
	}

	err = r.client.Delete(ctx, objectSetPhase.ClientObject())
	if err != nil && errors.IsNotFound(err) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("deleting ObjectSetPhase: %w", err)
	}

	// Wait until we retry and really get a 404.
	return false, nil
}

func (r *objectSetRemotePhaseReconciler) Reconcile(
	ctx context.Context, objectSet genericObjectSet,
	phase corev1alpha1.ObjectSetTemplatePhase,
) ([]corev1alpha1.ControlledObjectReference, controllers.ProbingResult, error) {
	if len(phase.Class) == 0 {
		return nil, controllers.ProbingResult{}, nil
	}

	desiredObjectSetPhase, err := r.desiredObjectSetPhase(objectSet, phase)
	if err != nil {
		return nil, controllers.ProbingResult{}, err
	}

	currentObjectSetPhase := r.newObjectSetPhase(r.scheme)
	err = r.client.Get(
		ctx, client.ObjectKeyFromObject(desiredObjectSetPhase.ClientObject()),
		currentObjectSetPhase.ClientObject(),
	)
	if errors.IsNotFound(err) {
		if err := r.client.Create(
			ctx, desiredObjectSetPhase.ClientObject()); err != nil {
			return nil, controllers.ProbingResult{}, fmt.Errorf("creating new ObjectSetPhase: %w", err)
		}
		currentObjectSetPhase = desiredObjectSetPhase
	}
	if err != nil {
		return nil, controllers.ProbingResult{}, fmt.Errorf("getting existing ObjectSetPhase: %w", err)
	}

	// Report ObjectSetPhase as part of this ObjectSet
	ref := corev1alpha1.RemotePhaseReference{
		Name: currentObjectSetPhase.ClientObject().GetName(),
		UID:  currentObjectSetPhase.ClientObject().GetUID(),
	}
	remotes := objectSet.GetRemotePhases()
	objectSet.SetRemotePhases(addRemoteObjectSetPhase(remotes, ref))

	// Pause/Unpause
	if currentObjectSetPhase.IsPaused() != desiredObjectSetPhase.IsPaused() {
		current := currentObjectSetPhase.ClientObject()
		patch := map[string]interface{}{
			"metadata": map[string]interface{}{
				"resourceVersion": current.GetResourceVersion(),
			},
			"spec": map[string]interface{}{
				"paused": desiredObjectSetPhase.IsPaused(),
			},
		}
		patchJSON, err := json.Marshal(patch)
		if err != nil {
			panic(err)
		}
		if err := r.client.Patch(
			ctx, current, client.RawPatch(types.MergePatchType, patchJSON)); err != nil {
			return nil, controllers.ProbingResult{}, fmt.Errorf("patching ObjectSetPhase: %w", err)
		}
	}

	// ObjectSetPhase already exists
	// -> copy mapped status conditions
	controllers.MapConditions(
		ctx,
		currentObjectSetPhase.ClientObject().GetGeneration(), currentObjectSetPhase.GetConditions(),
		objectSet.ClientObject().GetGeneration(), objectSet.GetConditions(),
	)

	// -> check status
	availableCond := meta.FindStatusCondition(
		currentObjectSetPhase.GetConditions(),
		corev1alpha1.ObjectSetAvailable,
	)
	activeObjects := currentObjectSetPhase.GetStatusControllerOf()
	if availableCond == nil ||
		availableCond.ObservedGeneration !=
			currentObjectSetPhase.ClientObject().GetGeneration() {
		// no status reported, wait longer
		return activeObjects, controllers.ProbingResult{
			PhaseName: phase.Name,
			FailedProbes: []string{
				noStatusProbeFailure,
			},
		}, nil
	}
	if availableCond.Status == metav1.ConditionTrue {
		// Remote Phase is Available!
		return activeObjects, controllers.ProbingResult{}, nil
	}

	// Remote Phase is not Available!
	// Reports its message as failed probe output.
	return activeObjects, controllers.ProbingResult{
		PhaseName: phase.Name,
		FailedProbes: []string{
			availableCond.Message,
		},
	}, nil
}

func (r *objectSetRemotePhaseReconciler) desiredObjectSetPhase(
	objectSet genericObjectSet,
	phase corev1alpha1.ObjectSetTemplatePhase,
) (genericObjectSetPhase, error) {
	objectSetObj := objectSet.ClientObject()

	desiredObjectSetPhase := r.newObjectSetPhase(r.scheme)
	desired := desiredObjectSetPhase.ClientObject()
	desired.SetName(objectSetPhaseName(objectSet, phase))
	desired.SetNamespace(objectSetObj.GetNamespace())
	desired.SetAnnotations(objectSetObj.GetAnnotations())
	desired.SetLabels(objectSetObj.GetLabels())

	desiredObjectSetPhase.SetPhase(phase)
	desiredObjectSetPhase.SetAvailabilityProbes(objectSet.GetAvailabilityProbes())
	desiredObjectSetPhase.SetRevision(objectSet.GetRevision())
	desiredObjectSetPhase.SetPrevious(objectSet.GetPrevious())
	if objectSet.IsPaused() {
		// ObjectSetPhases don't have to support archival.
		desiredObjectSetPhase.SetPaused(true)
	}

	if err := controllerutil.SetControllerReference(
		objectSetObj, desired, r.scheme); err != nil {
		return nil, err
	}
	return desiredObjectSetPhase, nil
}

func objectSetPhaseName(
	objectSet genericObjectSet,
	phase corev1alpha1.ObjectSetTemplatePhase,
) string {
	return objectSet.ClientObject().GetName() + "-" + phase.Name
}

// Adds a RemotePhaseReference if it's not already part of the slice.
func addRemoteObjectSetPhase(
	refs []corev1alpha1.RemotePhaseReference,
	ref corev1alpha1.RemotePhaseReference,
) []corev1alpha1.RemotePhaseReference {
	for i := range refs {
		if refs[i].Name == ref.Name {
			refs[i] = ref
			return refs
		}
	}
	refs = append(refs, ref)
	return refs
}
