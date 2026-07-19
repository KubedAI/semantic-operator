package ossie

import (
	"testing"

	"github.com/KubedAI/semantic-operator/api/v1alpha1"
)

func TestValidateJoinsOK(t *testing.T) {
	spec := validSpec()
	spec.Joins = []v1alpha1.RelationshipJoin{{Relationship: "sales_to_customer", Type: "LEFT"}}
	if err := ValidateSpec(spec); err != nil {
		t.Fatalf("valid joins override rejected: %v", err)
	}
}

func TestValidateJoinsUnknownRelationship(t *testing.T) {
	spec := validSpec()
	spec.Joins = []v1alpha1.RelationshipJoin{{Relationship: "nope", Type: "LEFT"}}
	wantErr(t, spec, `relationship "nope" does not exist`)
}

func TestValidateJoinsDuplicateRejected(t *testing.T) {
	// Conflicting duplicates must be rejected, not resolved last-write-wins.
	spec := validSpec()
	spec.Joins = []v1alpha1.RelationshipJoin{
		{Relationship: "sales_to_customer", Type: "LEFT"},
		{Relationship: "sales_to_customer", Type: "INNER"},
	}
	wantErr(t, spec, `duplicate override for relationship "sales_to_customer"`)
}

func TestValidateJoinsBadType(t *testing.T) {
	spec := validSpec()
	spec.Joins = []v1alpha1.RelationshipJoin{{Relationship: "sales_to_date", Type: "FULL"}}
	wantErr(t, spec, `must be INNER or LEFT`)
}
