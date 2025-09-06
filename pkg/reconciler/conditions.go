package reconciler

import (
	"time"

	vaultv1alpha1 "github.com/fredericrous/homelab/vault-operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ConditionManager helps manage status conditions
type ConditionManager struct {
	vtu *vaultv1alpha1.VaultTransitUnseal
}

// NewConditionManager creates a new condition manager
func NewConditionManager(vtu *vaultv1alpha1.VaultTransitUnseal) *ConditionManager {
	return &ConditionManager{vtu: vtu}
}

// SetCondition updates or adds a condition
func (cm *ConditionManager) SetCondition(condType string, status metav1.ConditionStatus, reason, message string) {
	now := metav1.Now()

	// Find existing condition
	for i := range cm.vtu.Status.Conditions {
		if cm.vtu.Status.Conditions[i].Type == condType {
			// Only update if changed
			if cm.vtu.Status.Conditions[i].Status != string(status) ||
				cm.vtu.Status.Conditions[i].Reason != reason ||
				cm.vtu.Status.Conditions[i].Message != message {

				cm.vtu.Status.Conditions[i].Status = string(status)
				cm.vtu.Status.Conditions[i].Reason = reason
				cm.vtu.Status.Conditions[i].Message = message
				cm.vtu.Status.Conditions[i].LastTransitionTime = now.Format(time.RFC3339)
			}
			return
		}
	}

	// Add new condition
	cm.vtu.Status.Conditions = append(cm.vtu.Status.Conditions, vaultv1alpha1.Condition{
		Type:               condType,
		Status:             string(status),
		Reason:             reason,
		Message:            message,
		LastTransitionTime: now.Format(time.RFC3339),
	})
}

// GetCondition returns a condition by type
func (cm *ConditionManager) GetCondition(condType string) *vaultv1alpha1.Condition {
	for i := range cm.vtu.Status.Conditions {
		if cm.vtu.Status.Conditions[i].Type == condType {
			return &cm.vtu.Status.Conditions[i]
		}
	}
	return nil
}

// IsConditionTrue returns true if a condition is set to True
func (cm *ConditionManager) IsConditionTrue(condType string) bool {
	cond := cm.GetCondition(condType)
	return cond != nil && cond.Status == string(metav1.ConditionTrue)
}

// RemoveCondition removes a condition by type
func (cm *ConditionManager) RemoveCondition(condType string) {
	conditions := make([]vaultv1alpha1.Condition, 0, len(cm.vtu.Status.Conditions))
	for _, c := range cm.vtu.Status.Conditions {
		if c.Type != condType {
			conditions = append(conditions, c)
		}
	}
	cm.vtu.Status.Conditions = conditions
}
