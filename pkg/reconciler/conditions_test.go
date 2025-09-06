package reconciler

import (
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	vaultv1alpha1 "github.com/fredericrous/homelab/vault-operator/api/v1alpha1"
)

func TestNewConditionManager(t *testing.T) {
	vtu := &vaultv1alpha1.VaultTransitUnseal{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: "default",
		},
	}

	cm := NewConditionManager(vtu)
	assert.NotNil(t, cm)
	assert.Equal(t, vtu, cm.vtu)
}

func TestSetCondition(t *testing.T) {
	tests := []struct {
		name            string
		existingConds   []vaultv1alpha1.Condition
		condType        string
		status          metav1.ConditionStatus
		reason          string
		message         string
		expectCount     int
		expectTransTime bool
	}{
		{
			name:          "add new condition",
			existingConds: []vaultv1alpha1.Condition{},
			condType:      "Ready",
			status:        metav1.ConditionTrue,
			reason:        "VaultReady",
			message:       "Vault is ready",
			expectCount:   1,
		},
		{
			name: "update existing condition with changed status",
			existingConds: []vaultv1alpha1.Condition{
				{
					Type:               "Ready",
					Status:             string(metav1.ConditionFalse),
					Reason:             "NotReady",
					Message:            "Vault is not ready",
					LastTransitionTime: "2020-01-01T00:00:00Z",
				},
			},
			condType:        "Ready",
			status:          metav1.ConditionTrue,
			reason:          "VaultReady",
			message:         "Vault is ready",
			expectCount:     1,
			expectTransTime: true,
		},
		{
			name: "no update when condition unchanged",
			existingConds: []vaultv1alpha1.Condition{
				{
					Type:               "Ready",
					Status:             string(metav1.ConditionTrue),
					Reason:             "VaultReady",
					Message:            "Vault is ready",
					LastTransitionTime: "2023-01-01T00:00:00Z",
				},
			},
			condType:    "Ready",
			status:      metav1.ConditionTrue,
			reason:      "VaultReady",
			message:     "Vault is ready",
			expectCount: 1,
		},
		{
			name: "add condition to existing list",
			existingConds: []vaultv1alpha1.Condition{
				{
					Type:    "Initialized",
					Status:  string(metav1.ConditionTrue),
					Reason:  "VaultInitialized",
					Message: "Vault is initialized",
				},
			},
			condType:    "Ready",
			status:      metav1.ConditionTrue,
			reason:      "VaultReady",
			message:     "Vault is ready",
			expectCount: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vtu := &vaultv1alpha1.VaultTransitUnseal{
				Status: vaultv1alpha1.VaultTransitUnsealStatus{
					Conditions: tt.existingConds,
				},
			}

			cm := NewConditionManager(vtu)
			oldTransTime := ""
			if len(tt.existingConds) > 0 && tt.existingConds[0].Type == tt.condType {
				oldTransTime = tt.existingConds[0].LastTransitionTime
			}

			cm.SetCondition(tt.condType, tt.status, tt.reason, tt.message)

			assert.Len(t, vtu.Status.Conditions, tt.expectCount)

			// Find the condition
			var found *vaultv1alpha1.Condition
			for i := range vtu.Status.Conditions {
				if vtu.Status.Conditions[i].Type == tt.condType {
					found = &vtu.Status.Conditions[i]
					break
				}
			}

			assert.NotNil(t, found)
			assert.Equal(t, tt.condType, found.Type)
			assert.Equal(t, string(tt.status), found.Status)
			assert.Equal(t, tt.reason, found.Reason)
			assert.Equal(t, tt.message, found.Message)

			if tt.expectTransTime && oldTransTime != "" {
				assert.NotEqual(t, oldTransTime, found.LastTransitionTime)
			} else if !tt.expectTransTime && oldTransTime != "" {
				assert.Equal(t, oldTransTime, found.LastTransitionTime)
			}
		})
	}
}

func TestGetCondition(t *testing.T) {
	tests := []struct {
		name      string
		conds     []vaultv1alpha1.Condition
		condType  string
		expectNil bool
	}{
		{
			name:      "condition not found",
			conds:     []vaultv1alpha1.Condition{},
			condType:  "Ready",
			expectNil: true,
		},
		{
			name: "condition found",
			conds: []vaultv1alpha1.Condition{
				{
					Type:    "Ready",
					Status:  string(metav1.ConditionTrue),
					Reason:  "VaultReady",
					Message: "Vault is ready",
				},
			},
			condType:  "Ready",
			expectNil: false,
		},
		{
			name: "find correct condition among many",
			conds: []vaultv1alpha1.Condition{
				{
					Type:   "Initialized",
					Status: string(metav1.ConditionTrue),
				},
				{
					Type:   "Ready",
					Status: string(metav1.ConditionFalse),
				},
				{
					Type:   "Synced",
					Status: string(metav1.ConditionTrue),
				},
			},
			condType:  "Ready",
			expectNil: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vtu := &vaultv1alpha1.VaultTransitUnseal{
				Status: vaultv1alpha1.VaultTransitUnsealStatus{
					Conditions: tt.conds,
				},
			}

			cm := NewConditionManager(vtu)
			cond := cm.GetCondition(tt.condType)

			if tt.expectNil {
				assert.Nil(t, cond)
			} else {
				assert.NotNil(t, cond)
				assert.Equal(t, tt.condType, cond.Type)
			}
		})
	}
}

func TestIsConditionTrue(t *testing.T) {
	tests := []struct {
		name     string
		conds    []vaultv1alpha1.Condition
		condType string
		want     bool
	}{
		{
			name:     "condition not found",
			conds:    []vaultv1alpha1.Condition{},
			condType: "Ready",
			want:     false,
		},
		{
			name: "condition is true",
			conds: []vaultv1alpha1.Condition{
				{
					Type:   "Ready",
					Status: string(metav1.ConditionTrue),
				},
			},
			condType: "Ready",
			want:     true,
		},
		{
			name: "condition is false",
			conds: []vaultv1alpha1.Condition{
				{
					Type:   "Ready",
					Status: string(metav1.ConditionFalse),
				},
			},
			condType: "Ready",
			want:     false,
		},
		{
			name: "condition is unknown",
			conds: []vaultv1alpha1.Condition{
				{
					Type:   "Ready",
					Status: string(metav1.ConditionUnknown),
				},
			},
			condType: "Ready",
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vtu := &vaultv1alpha1.VaultTransitUnseal{
				Status: vaultv1alpha1.VaultTransitUnsealStatus{
					Conditions: tt.conds,
				},
			}

			cm := NewConditionManager(vtu)
			got := cm.IsConditionTrue(tt.condType)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestRemoveCondition(t *testing.T) {
	tests := []struct {
		name          string
		initialConds  []vaultv1alpha1.Condition
		removeType    string
		expectedConds []string
	}{
		{
			name:          "remove from empty list",
			initialConds:  []vaultv1alpha1.Condition{},
			removeType:    "Ready",
			expectedConds: []string{},
		},
		{
			name: "remove existing condition",
			initialConds: []vaultv1alpha1.Condition{
				{Type: "Ready"},
				{Type: "Initialized"},
			},
			removeType:    "Ready",
			expectedConds: []string{"Initialized"},
		},
		{
			name: "remove non-existing condition",
			initialConds: []vaultv1alpha1.Condition{
				{Type: "Ready"},
				{Type: "Initialized"},
			},
			removeType:    "Synced",
			expectedConds: []string{"Ready", "Initialized"},
		},
		{
			name: "remove from single condition list",
			initialConds: []vaultv1alpha1.Condition{
				{Type: "Ready"},
			},
			removeType:    "Ready",
			expectedConds: []string{},
		},
		{
			name: "remove middle condition",
			initialConds: []vaultv1alpha1.Condition{
				{Type: "Ready"},
				{Type: "Initialized"},
				{Type: "Synced"},
			},
			removeType:    "Initialized",
			expectedConds: []string{"Ready", "Synced"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vtu := &vaultv1alpha1.VaultTransitUnseal{
				Status: vaultv1alpha1.VaultTransitUnsealStatus{
					Conditions: tt.initialConds,
				},
			}

			cm := NewConditionManager(vtu)
			cm.RemoveCondition(tt.removeType)

			// Check resulting conditions
			assert.Len(t, vtu.Status.Conditions, len(tt.expectedConds))

			actualTypes := make([]string, len(vtu.Status.Conditions))
			for i, c := range vtu.Status.Conditions {
				actualTypes[i] = c.Type
			}
			assert.Equal(t, tt.expectedConds, actualTypes)
		})
	}
}
