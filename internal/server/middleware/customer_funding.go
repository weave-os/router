package middleware

import "weave-os/router/internal/credentials"

func requireCustomerFunding(funding credentials.CustomerFunding) {
	if funding == nil {
		panic("middleware: a billing middleware was built without the credentials.CustomerFunding port; wire it in cmd/router")
	}
}
