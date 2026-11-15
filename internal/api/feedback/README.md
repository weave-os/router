# internal/api/feedback

No-login feedback-link surface. The signed token from `internal/feedback` is
the sole credential, so these routes deliberately carry no auth middleware.

The one-click `GET /v1/feedback/rate?t=<token>&r=up|down` footer link records
the rating straight from the GET. Link prefetchers can trigger it; that is
accepted because the token is unguessable and per-request, so the blast radius
is that request's own rating.
