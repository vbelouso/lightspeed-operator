package controller

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"

	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"

	olsv1alpha1 "github.com/openshift/lightspeed-operator/api/v1alpha1"
)

func (r *OLSConfigReconciler) reconcileRedisServer(ctx context.Context, olsconfig *olsv1alpha1.OLSConfig) error {
	r.logger.Info("reconcileRedisServer starts")
	tasks := []ReconcileTask{
		{
			Name: "reconcile Redis Secret",
			Task: r.reconcileRedisSecret,
		},
		{
			Name: "reconcile Redis Service",
			Task: r.reconcileRedisService,
		},
		{
			Name: "reconcile Redis Deployment",
			Task: r.reconcileRedisDeployment,
		},
	}

	for _, task := range tasks {
		err := task.Task(ctx, olsconfig)
		if err != nil {
			r.logger.Error(err, "reconcileRedisServer error", "task", task.Name)
			return fmt.Errorf("failed to %s: %w", task.Name, err)
		}
	}

	r.logger.Info("reconcileRedisServer completed")

	return nil
}

func (r *OLSConfigReconciler) reconcileRedisDeployment(ctx context.Context, cr *olsv1alpha1.OLSConfig) error {
	desiredDeployment, err := r.generateRedisDeployment(cr)
	if err != nil {
		return fmt.Errorf("failed to generate Redis deployment: %w", err)
	}

	existingDeployment := &appsv1.Deployment{}
	err = r.Client.Get(ctx, client.ObjectKey{Name: RedisDeploymentName, Namespace: r.Options.Namespace}, existingDeployment)
	if err != nil && errors.IsNotFound(err) {
		updateDeploymentAnnotations(desiredDeployment, map[string]string{
			RedisConfigHashKey: r.stateCache[RedisConfigHashStateCacheKey],
		})
		updateDeploymentTemplateAnnotations(desiredDeployment, map[string]string{
			RedisConfigHashKey: r.stateCache[RedisConfigHashStateCacheKey],
		})
		r.logger.Info("creating a new Redis deployment", "deployment", desiredDeployment.Name)
		err = r.Create(ctx, desiredDeployment)
		if err != nil {
			return fmt.Errorf("failed to create Redis deployment: %w", err)
		}
		return nil
	} else if err != nil {
		return fmt.Errorf("failed to get Redis deployment: %w", err)
	}

	err = r.updateRedisDeployment(ctx, existingDeployment, desiredDeployment)
	if err != nil {
		return fmt.Errorf("failed to update Redis deployment: %w", err)
	}

	r.logger.Info("Redis deployment reconciled", "deployment", desiredDeployment.Name)
	return nil
}

func (r *OLSConfigReconciler) reconcileRedisService(ctx context.Context, cr *olsv1alpha1.OLSConfig) error {
	service, err := r.generateRedisService(cr)
	if err != nil {
		return fmt.Errorf("failed to generate Redis service: %w", err)
	}

	foundService := &corev1.Service{}
	err = r.Client.Get(ctx, client.ObjectKey{Name: RedisServiceName, Namespace: r.Options.Namespace}, foundService)
	if err != nil && errors.IsNotFound(err) {
		err = r.Create(ctx, service)
		if err != nil {
			return fmt.Errorf("failed to create Redis service: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("failed to get Redis service: %w", err)
	}
	r.logger.Info("Redis service reconciled", "service", service.Name)
	return nil
}

func (r *OLSConfigReconciler) reconcileRedisSecret(ctx context.Context, cr *olsv1alpha1.OLSConfig) error {
	secret, err := r.generateRedisSecret(cr)
	if err != nil {
		return fmt.Errorf("failed to generate Redis secret: %w", err)
	}
	foundSecret := &corev1.Secret{}
	err = r.Client.Get(ctx, client.ObjectKey{Name: cr.Spec.OLSConfig.ConversationCache.Redis.CredentialsSecret, Namespace: r.Options.Namespace}, foundSecret)
	if err != nil && errors.IsNotFound(err) {
		err = r.deleteOldRedisSecrets(ctx)
		if err != nil {
			return err
		}
		r.logger.Info("creating a new Redis secret", "secret", secret.Name)
		err = r.Create(ctx, secret)
		if err != nil {
			return fmt.Errorf("failed to create Redis secret: %w", err)
		}
		r.stateCache[RedisSecretHashStateCacheKey] = secret.Annotations[RedisSecretHashKey]
		return nil
	} else if err != nil {
		return fmt.Errorf("failed to get Redis secret: %w", err)
	}
	foundSecretHash, err := hashBytes(foundSecret.Data[RedisSecretKeyName])
	if err != nil {
		return fmt.Errorf("failed to generate hash for the existing Redis secret: %w", err)
	}
	if foundSecretHash == r.stateCache[RedisSecretHashStateCacheKey] {
		r.logger.Info("Redis secret reconciliation skipped", "secret", foundSecret.Name, "hash", foundSecret.Annotations[RedisSecretHashKey])
		return nil
	}
	r.stateCache[RedisSecretHashStateCacheKey] = foundSecretHash
	secret.Annotations[RedisSecretHashKey] = foundSecretHash
	secret.Data[RedisSecretKeyName] = foundSecret.Data[RedisSecretKeyName]
	err = r.Update(ctx, secret)
	if err != nil {
		return fmt.Errorf("failed to update Redis secret: %w", err)
	}
	r.logger.Info("Redis secret reconciled", "secret", secret.Name, "hash", secret.Annotations[RedisSecretHashKey])
	return nil
}

func (r *OLSConfigReconciler) deleteOldRedisSecrets(ctx context.Context) error {
	labelSelector := labels.Set{"app.kubernetes.io/name": "lightspeed-service-redis"}.AsSelector()
	matchingLabels := client.MatchingLabelsSelector{Selector: labelSelector}
	oldSecrets := &corev1.SecretList{}
	err := r.Client.List(ctx, oldSecrets, &client.ListOptions{Namespace: r.Options.Namespace, LabelSelector: labelSelector})
	if err != nil {
		return fmt.Errorf("failed to list old Redis secrets: %w", err)
	}
	r.logger.Info("deleting old Redis secrets", "count", len(oldSecrets.Items))

	deleteOptions := &client.DeleteAllOfOptions{
		ListOptions: client.ListOptions{
			Namespace:     r.Options.Namespace,
			LabelSelector: matchingLabels,
		},
	}
	if err := r.Client.DeleteAllOf(ctx, &corev1.Secret{}, deleteOptions); err != nil {
		return fmt.Errorf("failed to delete old Redis secrets: %w", err)
	}
	return nil
}
