package subscriptions

import "weave-os/router/internal/auth"

func accountFromSubscription(seat *auth.SubscriptionAccount) Account {
	account := Account{ID: seat.ID, OwnerID: "account:" + seat.ID, Provider: Provider(seat.Provider),
		Enabled: seat.Enabled, State: seat.State, TokenRefreshVersion: seat.TokenRefreshVersion}
	if account.Provider == ProviderCodex {
		account.AccountID = seat.ExternalAccountID
	}
	if seat.CooldownUntil != nil {
		account.CooldownTil = *seat.CooldownUntil
	}
	return account
}
