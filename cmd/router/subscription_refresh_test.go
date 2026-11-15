package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSubscriptionRefreshPolicyRequiresExplicitOptIn(t *testing.T) {
	for _, scenario := range []struct {
		name, ids, age string
		valid          bool
	}{
		{name: "off by default", valid: true},
		{name: "one seat", ids: "11111111-1111-1111-1111-111111111111", age: "144h", valid: true},
		{name: "age without seats", age: "144h"},
		{name: "seats without age", ids: "11111111-1111-1111-1111-111111111111"},
		{name: "invalid seat", ids: "not-a-uuid", age: "144h"},
		{name: "refresh storm", ids: "11111111-1111-1111-1111-111111111111", age: "1m"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			_, err := subscriptionRefreshPolicy(scenario.ids, scenario.age)
			if scenario.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}
