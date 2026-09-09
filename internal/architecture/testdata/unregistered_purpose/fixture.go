package unregisteredpurpose

import "weave-os/router/internal/router/policy"

var purpose = policy.Purpose("fixture_unregistered_purpose")

var parenthesized = (policy.Purpose)("fixture_unregistered_purpose_paren")

var typed policy.Purpose = "fixture_unregistered_purpose_typed"

const declared policy.Purpose = "fixture_unregistered_purpose_const"

func accept(policy.Purpose) {}

func use() {
	accept("fixture_unregistered_purpose_arg")
	accept(purpose)
	accept(parenthesized)
	accept(typed)
	accept(declared)
}
