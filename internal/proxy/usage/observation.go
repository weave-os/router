package usage

import "context"

type observationContextKey struct{}

func WithObservation(ctx context.Context, observation *Observation) context.Context {
	return context.WithValue(ctx, observationContextKey{}, observation)
}

func ObservationFromContext(ctx context.Context) *Observation {
	observation, _ := ctx.Value(observationContextKey{}).(*Observation)
	return observation
}

type observationEpoch struct {
	active     int
	generation int64
}

type Observation struct {
	observer *Observer
	key      CredentialKey
	aliases  []CredentialKey
	epoch    *observationEpoch
	closed   bool
}

func (o *Observer) BeginObservation(key CredentialKey, generation *int64, aliases ...CredentialKey) *Observation {
	o.mu.Lock()
	defer o.mu.Unlock()
	epoch := o.observationEpochLocked(key)
	if generation != nil && *generation > epoch.generation {
		epoch = &observationEpoch{generation: *generation}
		o.observations[key] = epoch
		delete(o.data, key)
	} else if generation != nil && *generation < epoch.generation {
		epoch = &observationEpoch{generation: *generation}
	}
	epoch.active++
	return &Observation{observer: o, key: key, aliases: append([]CredentialKey(nil), aliases...), epoch: epoch}
}

func (observation *Observation) Record(snapshot Snapshot) {
	if !snapshot.hasData() {
		return
	}
	o := observation.observer
	o.mu.Lock()
	defer o.mu.Unlock()
	if observation.closed || o.observations[observation.key] != observation.epoch {
		return
	}
	o.recordLocked(observation.key, snapshot)
	for _, alias := range observation.aliases {
		o.recordLocked(alias, snapshot)
	}
}

func (observation *Observation) Close() {
	o := observation.observer
	o.mu.Lock()
	defer o.mu.Unlock()
	if observation.closed {
		return
	}
	observation.closed = true
	observation.epoch.active--
	if _, present := o.data[observation.key]; !present && observation.epoch.active == 0 && o.observations[observation.key] == observation.epoch {
		delete(o.observations, observation.key)
	}
}

func (o *Observer) Forget(key CredentialKey) {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.data, key)
	delete(o.observations, key)
}

func (o *Observer) Recover(key CredentialKey, generation int64) {
	if generation <= 0 {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	epoch := o.observationEpochLocked(key)
	if generation > epoch.generation {
		o.observations[key] = &observationEpoch{generation: generation}
		delete(o.data, key)
	}
}

func (o *Observer) observationEpochLocked(key CredentialKey) *observationEpoch {
	if o.observations == nil {
		o.observations = make(map[CredentialKey]*observationEpoch)
	}
	if epoch := o.observations[key]; epoch != nil {
		return epoch
	}
	epoch := &observationEpoch{}
	o.observations[key] = epoch
	return epoch
}

func (o *Observer) sweepObservationsLocked() {
	for key, epoch := range o.observations {
		if _, present := o.data[key]; !present && epoch.active == 0 {
			delete(o.observations, key)
		}
	}
}

func (o *Observer) Record(key CredentialKey, snapshot Snapshot) {
	if !snapshot.hasData() {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.observationEpochLocked(key)
	o.recordLocked(key, snapshot)
}
func (o *Observer) recordLocked(key CredentialKey, snap Snapshot) {
	now := o.now()
	if snap.Primary.present() {
		snap.Primary.ObservedAt = now
	}
	if snap.Secondary.present() {
		snap.Secondary.ObservedAt = now
	}
	if prev, ok := o.data[key]; ok && o.now().Sub(prev.ObservedAt) <= o.freshFor(prev) {
		if !snap.Primary.present() {
			snap.Primary = prev.Primary
		} else if snap.Primary.ResetAt.IsZero() && prev.Primary.ResetAt.After(now) {
			snap.Primary.ResetAt = prev.Primary.ResetAt
		}
		if !snap.Secondary.present() {
			snap.Secondary = prev.Secondary
		} else if snap.Secondary.ResetAt.IsZero() && prev.Secondary.ResetAt.After(now) {
			snap.Secondary.ResetAt = prev.Secondary.ResetAt
		}
	}
	snap.ObservedAt = now
	o.data[key] = snap
}
