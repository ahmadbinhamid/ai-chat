package builderplan

import "ai-chat/internal/buildercontract"

// ContractVersion mirrors buildercontract.ContractVersion.
// BuilderPlan executions and future training examples should record this value.
const ContractVersion = buildercontract.ContractVersion

// Ensure BuilderPlan OperationKind strings stay aligned with buildercontract.
// The machine-readable package is authoritative; this file holds the executable
// subset (DefaultAllowedOpKinds). CONTRACT_DEFINED_NOT_IMPLEMENTED ops exist
// only in buildercontract until a safe executor is added — do not emit them.

// ToContractOp maps a plan OperationKind to the domain contract operation.
func ToContractOp(kind OperationKind) (buildercontract.PageOperation, bool) {
	return buildercontract.ParsePageOperation(string(kind))
}

// ContractRule returns the lifecycle rule for a plan operation kind.
func ContractRule(kind OperationKind) (buildercontract.LifecycleRule, bool) {
	op, ok := ToContractOp(kind)
	if !ok {
		return buildercontract.LifecycleRule{}, false
	}
	return buildercontract.RuleFor(op)
}

// WithContractVersion stamps ContractVersion onto a plan's ClassifierSource-adjacent
// metadata via Constraints when missing. Does not invent operations.
func WithContractVersion(plan BuilderPlan) BuilderPlan {
	const prefix = "contract_version="
	for _, c := range plan.Constraints {
		if len(c) >= len(prefix) && c[:len(prefix)] == prefix {
			return plan
		}
	}
	plan.Constraints = append(append([]string{}, plan.Constraints...), prefix+ContractVersion)
	return plan
}
