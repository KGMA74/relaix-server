// Package storetest provides an in-memory store.Store for tests.
//
// It exists so that hub, scheduler, gRPC handlers and the HTTP API can be
// exercised without a database: those components are where the interesting
// decisions live, and making each of their tests wait on Postgres would make
// the suite slow enough that people stop running it.
//
// It is a test double, not a second implementation. It is deliberately not
// wired into any binary: correctness against real SQL is the Postgres store's
// own problem, verified against a real server.
package storetest

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/KGMA74/relaix-server/policy"
	"github.com/KGMA74/relaix-server/store"
)

// Store is an in-memory store.Store.
//
// One mutex guards everything, and WithTx simply holds it for the duration of
// the callback. That is a cruder guarantee than the real store's row locks, but
// it is a stricter one — anything that passes here would also pass under
// serializable isolation — so a test that goes green on this fake is not being
// told a comfortable lie about atomicity, and a failed transaction really does
// undo its writes.
//
// The four sub-stores are separate types sharing this state, because the
// interfaces each declare a Create with a different signature and one type
// cannot carry all three.
type Store struct {
	mu sync.Mutex

	accounts    map[uuid.UUID]*store.Account
	devices     map[uuid.UUID]*store.Device
	tokenToID   map[string]uuid.UUID // device token hash -> device id
	jobs        map[uuid.UUID]*store.Job
	enrollments map[string]*store.EnrollmentToken // token hash -> token
	events      []*store.JobEvent
	nextEventID int64
	inbound     map[uuid.UUID]*store.InboundMessage
	runs        map[uuid.UUID]*store.AutomationRun

	// lastJobAt keeps job creation times strictly increasing. Postgres gives
	// each transaction its own now(), but the wall clock here can return the
	// same instant twice in a row on Windows, and job history is ordered by
	// creation time. Like a sequence, it is not rolled back with a transaction.
	lastJobAt time.Time

	// inTx marks a handle scoped to a WithTx callback, so the lock is taken
	// once rather than recursively.
	inTx bool

	// failNext, when non-nil, is returned by the next mutating call and then
	// cleared. Lets a test drive error paths that are otherwise unreachable
	// without a broken database.
	failNext error
}

// New returns an empty Store.
func New() *Store {
	// Seeded like migration 00009 seeds Postgres.
	return &Store{
		accounts: map[uuid.UUID]*store.Account{
			store.DefaultAccountID: {ID: store.DefaultAccountID, Name: "default", CreatedAt: time.Unix(0, 0).UTC()},
		},
		devices:     make(map[uuid.UUID]*store.Device),
		tokenToID:   make(map[string]uuid.UUID),
		jobs:        make(map[uuid.UUID]*store.Job),
		enrollments: make(map[string]*store.EnrollmentToken),
		inbound:     make(map[uuid.UUID]*store.InboundMessage),
		runs:        make(map[uuid.UUID]*store.AutomationRun),
	}
}

// FailNext makes the next mutating call return err.
func (s *Store) FailNext(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failNext = err
}

// lock takes the mutex unless we are already inside WithTx, and returns the
// matching unlock.
func (s *Store) lock() func() {
	if s.inTx {
		return func() {}
	}
	s.mu.Lock()
	return s.mu.Unlock
}

func (s *Store) takeFailure() error {
	err := s.failNext
	s.failNext = nil
	return err
}

// Accounts implements store.Store.
func (s *Store) Accounts() store.AccountStore { return accountStore{s} }

// Devices implements store.Store.
func (s *Store) Devices() store.DeviceStore { return deviceStore{s} }

// Jobs implements store.Store.
func (s *Store) Jobs() store.JobStore { return jobStore{s} }

// Enrollments implements store.Store.
func (s *Store) Enrollments() store.EnrollmentStore { return enrollmentStore{s} }

// Events implements store.Store.
func (s *Store) Events() store.EventStore { return eventStore{s} }

// WithTx implements store.Store. Returning an error from fn rolls every write
// back.
//
// Rollback is implemented by snapshotting the whole state on entry and
// restoring it on failure — crude, but the data here is a handful of rows and
// the alternative is a fake that silently keeps partial writes. That would be
// worse than useless for the operations this exists to test: enrollment is only
// safe because consuming a token and creating a device succeed or fail
// together, and a fake that could not fail them together would go green on code
// that leaks a device every time a token loses a race.
// Nesting is rejected by txStore.WithTx rather than by an inTx check here: the
// check would have to read shared state before taking the lock, which both
// races and misfires — a caller merely waiting its turn would be told it was
// nesting.
func (s *Store) WithTx(ctx context.Context, fn func(store.Store) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	undo := s.snapshot()

	s.inTx = true
	defer func() { s.inTx = false }()

	if err := fn(txStore{s}); err != nil {
		s.restore(undo)
		return err
	}
	return nil
}

// state is a deep copy of everything the store holds.
type state struct {
	accounts    map[uuid.UUID]*store.Account
	devices     map[uuid.UUID]*store.Device
	tokenToID   map[string]uuid.UUID
	jobs        map[uuid.UUID]*store.Job
	enrollments map[string]*store.EnrollmentToken
	events      []*store.JobEvent
	nextEventID int64
	inbound     map[uuid.UUID]*store.InboundMessage
	runs        map[uuid.UUID]*store.AutomationRun
}

// snapshot deep-copies the state. The copies must be deep: the store mutates
// the structs its maps point at, so sharing them would let a rolled-back
// transaction's edits survive.
func (s *Store) snapshot() state {
	cp := state{
		accounts:    make(map[uuid.UUID]*store.Account, len(s.accounts)),
		devices:     make(map[uuid.UUID]*store.Device, len(s.devices)),
		tokenToID:   make(map[string]uuid.UUID, len(s.tokenToID)),
		jobs:        make(map[uuid.UUID]*store.Job, len(s.jobs)),
		enrollments: make(map[string]*store.EnrollmentToken, len(s.enrollments)),
		events:      make([]*store.JobEvent, 0, len(s.events)),
		nextEventID: s.nextEventID,
		inbound:     make(map[uuid.UUID]*store.InboundMessage, len(s.inbound)),
	}
	for k, v := range s.inbound {
		cp.inbound[k] = copyInbound(v)
	}
	cp.runs = make(map[uuid.UUID]*store.AutomationRun, len(s.runs))
	for k, v := range s.runs {
		cp.runs[k] = copyRun(v)
	}
	for k, v := range s.accounts {
		c := *v
		cp.accounts[k] = &c
	}
	for k, v := range s.devices {
		cp.devices[k] = copyDevice(v)
	}
	maps.Copy(cp.tokenToID, s.tokenToID)
	for k, v := range s.jobs {
		cp.jobs[k] = copyJob(v)
	}
	for k, v := range s.enrollments {
		cp.enrollments[k] = copyToken(v)
	}
	for _, e := range s.events {
		c := *e
		cp.events = append(cp.events, &c)
	}
	return cp
}

func (s *Store) restore(st state) {
	s.accounts = st.accounts
	s.devices = st.devices
	s.tokenToID = st.tokenToID
	s.jobs = st.jobs
	s.enrollments = st.enrollments
	s.events = st.events
	s.nextEventID = st.nextEventID
	s.inbound = st.inbound
	s.runs = st.runs
}

// txStore is the handle handed to a WithTx callback. It shares all state with
// its parent and only exists to reject nesting.
type txStore struct{ *Store }

func (t txStore) WithTx(context.Context, func(store.Store) error) error {
	return fmt.Errorf("storetest: nested WithTx")
}

// Compile-time proof that the fake satisfies the boundary it stands in for.
var (
	_ store.Store           = (*Store)(nil)
	_ store.Store           = txStore{}
	_ store.AccountStore    = accountStore{}
	_ store.DeviceStore     = deviceStore{}
	_ store.JobStore        = jobStore{}
	_ store.EnrollmentStore = enrollmentStore{}
	_ store.EventStore      = eventStore{}
)

// ---------------------------------------------------------------------------
// DeviceStore
// ---------------------------------------------------------------------------

type deviceStore struct{ s *Store }

func (d deviceStore) Create(ctx context.Context, in *store.Device, tokenHash string) (*store.Device, error) {
	defer d.s.lock()()
	if err := d.s.takeFailure(); err != nil {
		return nil, err
	}
	if _, taken := d.s.tokenToID[tokenHash]; taken {
		return nil, store.ErrConflict
	}
	// Mirrors the partial unique index on hardware_hash.
	if in.HardwareHash != "" {
		for _, other := range d.s.devices {
			if other.HardwareHash == in.HardwareHash {
				return nil, store.ErrConflict
			}
		}
	}

	cp := *in
	if cp.ID == uuid.Nil {
		cp.ID = uuid.New()
	}
	if cp.CreatedAt.IsZero() {
		cp.CreatedAt = time.Now()
	}
	cp.UpdatedAt = cp.CreatedAt
	cp.Sims = append([]store.SimInfo(nil), in.Sims...)
	cp.Capabilities = slices.Clone(in.Capabilities)
	cp.Health = copyHealth(in.Health)
	cp.AccountID = store.AccountOrDefault(in.AccountID)
	// A newly enrolled device is enabled, whatever the caller passed. The
	// column is NOT NULL DEFAULT true and the insert sets it explicitly, so the
	// fake must not let a zero-valued struct create a disabled device.
	cp.Enabled = true

	d.s.devices[cp.ID] = &cp
	d.s.tokenToID[tokenHash] = cp.ID
	return copyDevice(&cp), nil
}

func (d deviceStore) Get(ctx context.Context, id uuid.UUID) (*store.Device, error) {
	defer d.s.lock()()
	dev, ok := d.s.devices[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return copyDevice(dev), nil
}

func (d deviceStore) GetByTokenHash(ctx context.Context, tokenHash string) (*store.Device, error) {
	defer d.s.lock()()
	id, ok := d.s.tokenToID[tokenHash]
	if !ok {
		return nil, store.ErrNotFound
	}
	return copyDevice(d.s.devices[id]), nil
}

func (d deviceStore) List(ctx context.Context, scope store.Scope) ([]*store.Device, error) {
	defer d.s.lock()()
	out := make([]*store.Device, 0, len(d.s.devices))
	for _, dev := range d.s.devices {
		if scope.Includes(dev.AccountID) {
			out = append(out, copyDevice(dev))
		}
	}
	slices.SortFunc(out, func(a, b *store.Device) int {
		return b.CreatedAt.Compare(a.CreatedAt) // newest first
	})
	return out, nil
}

func (d deviceStore) GetByHardwareHash(ctx context.Context, hardwareHash string) (*store.Device, error) {
	defer d.s.lock()()
	if hardwareHash == "" {
		return nil, store.ErrNotFound
	}
	for _, dev := range d.s.devices {
		if dev.HardwareHash == hardwareHash {
			return copyDevice(dev), nil
		}
	}
	return nil, store.ErrNotFound
}

func (d deviceStore) Rekey(ctx context.Context, id uuid.UUID, in *store.Device, tokenHash string) (*store.Device, error) {
	defer d.s.lock()()
	if err := d.s.takeFailure(); err != nil {
		return nil, err
	}
	dev, ok := d.s.devices[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	if owner, taken := d.s.tokenToID[tokenHash]; taken && owner != id {
		return nil, store.ErrConflict
	}
	// The old token must stop authenticating: in Postgres it is overwritten
	// on the row, here it lives in a separate index.
	for hash, owner := range d.s.tokenToID {
		if owner == id {
			delete(d.s.tokenToID, hash)
		}
	}
	d.s.tokenToID[tokenHash] = id

	dev.AccountID = store.AccountOrDefault(in.AccountID)
	dev.Enabled = true
	dev.Label = in.Label
	dev.PhoneNumber = in.PhoneNumber
	dev.Manufacturer = in.Manufacturer
	dev.Model = in.Model
	dev.OSVersion = in.OSVersion
	dev.AgentVersion = in.AgentVersion
	dev.Carrier = in.Carrier
	dev.TimeZone = in.TimeZone
	dev.Capabilities = slices.Clone(in.Capabilities)
	dev.Sims = append([]store.SimInfo(nil), in.Sims...)
	dev.UpdatedAt = time.Now()
	return copyDevice(dev), nil
}

func (d deviceStore) SetPolicy(ctx context.Context, id uuid.UUID, p policy.Policy) (policy.Policy, error) {
	defer d.s.lock()()
	if err := d.s.takeFailure(); err != nil {
		return policy.Policy{}, err
	}
	dev, ok := d.s.devices[id]
	if !ok {
		return policy.Policy{}, store.ErrNotFound
	}
	now := time.Now().UTC()
	p.Version = dev.Policy.Version + 1
	p.UpdatedAt = &now
	dev.Policy = p
	dev.UpdatedAt = now
	return p, nil
}

func (d deviceStore) UpdateInfo(ctx context.Context, id uuid.UUID, in *store.Device) error {
	defer d.s.lock()()
	if err := d.s.takeFailure(); err != nil {
		return err
	}
	dev, ok := d.s.devices[id]
	if !ok {
		return store.ErrNotFound
	}
	dev.Label = in.Label
	dev.PhoneNumber = in.PhoneNumber
	dev.Manufacturer = in.Manufacturer
	dev.Model = in.Model
	dev.OSVersion = in.OSVersion
	dev.AgentVersion = in.AgentVersion
	dev.Carrier = in.Carrier
	dev.TimeZone = in.TimeZone
	dev.Capabilities = slices.Clone(in.Capabilities)
	dev.Sims = append([]store.SimInfo(nil), in.Sims...)
	dev.UpdatedAt = time.Now()
	return nil
}

func (d deviceStore) Touch(ctx context.Context, id uuid.UUID, h *store.DeviceHealth, seenAt time.Time) error {
	defer d.s.lock()()
	if err := d.s.takeFailure(); err != nil {
		return err
	}
	dev, ok := d.s.devices[id]
	if !ok {
		return store.ErrNotFound
	}
	// A nil health leaves the stored snapshot alone rather than blanking it, to
	// match store/postgres: a heartbeat that carried no health is not evidence
	// the phone has none.
	if h != nil {
		dev.Health = copyHealth(h)
	}
	t := seenAt
	dev.LastSeenAt = &t
	dev.UpdatedAt = seenAt
	return nil
}

func (d deviceStore) SetEnabled(ctx context.Context, id uuid.UUID, enabled bool) error {
	defer d.s.lock()()
	if err := d.s.takeFailure(); err != nil {
		return err
	}
	dev, ok := d.s.devices[id]
	if !ok {
		return store.ErrNotFound
	}
	dev.Enabled = enabled
	dev.UpdatedAt = time.Now()
	return nil
}

func (d deviceStore) Delete(ctx context.Context, id uuid.UUID) error {
	defer d.s.lock()()
	if err := d.s.takeFailure(); err != nil {
		return err
	}
	if _, ok := d.s.devices[id]; !ok {
		return store.ErrNotFound
	}
	delete(d.s.devices, id)

	// The token index is separate here, unlike Postgres where the hash lives
	// on the row and disappears with it. Forgetting this left a deleted device
	// still authenticating — which the conformance suite caught.
	for hash, owner := range d.s.tokenToID {
		if owner == id {
			delete(d.s.tokenToID, hash)
		}
	}

	// Mirrors ON DELETE SET NULL in the schema: jobs keep their history but
	// stop pointing at a device that no longer exists. Without this the fake
	// would let a conformance test pass while Postgres behaved differently.
	for _, j := range d.s.jobs {
		if j.RequestedDeviceID != nil && *j.RequestedDeviceID == id {
			j.RequestedDeviceID = nil
		}
		if j.AssignedDeviceID != nil && *j.AssignedDeviceID == id {
			j.AssignedDeviceID = nil
		}
	}
	for _, m := range d.s.inbound {
		if m.DeviceID != nil && *m.DeviceID == id {
			m.DeviceID = nil
		}
	}
	for _, r := range d.s.runs {
		if r.DeviceID != nil && *r.DeviceID == id {
			r.DeviceID = nil
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// JobStore
// ---------------------------------------------------------------------------

type jobStore struct{ s *Store }

func (j jobStore) Create(ctx context.Context, in *store.Job) (*store.Job, error) {
	defer j.s.lock()()
	if err := j.s.takeFailure(); err != nil {
		return nil, err
	}
	cp := *in
	if cp.ID == uuid.Nil {
		cp.ID = uuid.New()
	}
	if cp.Status == "" {
		cp.Status = store.JobPending
	}
	if cp.Kind == "" {
		cp.Kind = store.KindSMS
	}
	cp.Replies = slices.Clone(in.Replies)
	cp.Responses = nil
	cp.AccountID = store.AccountOrDefault(cp.AccountID)
	if cp.CreatedAt.IsZero() {
		cp.CreatedAt = time.Now()
		if !cp.CreatedAt.After(j.s.lastJobAt) {
			cp.CreatedAt = j.s.lastJobAt.Add(time.Microsecond)
		}
		j.s.lastJobAt = cp.CreatedAt
	}
	cp.UpdatedAt = cp.CreatedAt
	j.s.jobs[cp.ID] = &cp
	return copyJob(&cp), nil
}

func (j jobStore) Get(ctx context.Context, id uuid.UUID) (*store.Job, error) {
	defer j.s.lock()()
	job, ok := j.s.jobs[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return copyJob(job), nil
}

// ClaimSchedulable mirrors the ordering of the partial index in migration 0001:
// priority descending, then jobs with no scheduled_at before those with one,
// then oldest first.
func (j jobStore) ClaimSchedulable(ctx context.Context, now time.Time, limit int) ([]*store.Job, error) {
	defer j.s.lock()()
	if err := j.s.takeFailure(); err != nil {
		return nil, err
	}

	var out []*store.Job
	for _, job := range j.s.jobs {
		if job.Status != store.JobPending {
			continue
		}
		if job.ScheduledAt != nil && now.Before(*job.ScheduledAt) {
			continue
		}
		out = append(out, copyJob(job))
	}

	slices.SortFunc(out, func(a, b *store.Job) int {
		if c := cmp.Compare(b.Priority, a.Priority); c != 0 {
			return c
		}
		switch {
		case a.ScheduledAt == nil && b.ScheduledAt != nil:
			return -1
		case a.ScheduledAt != nil && b.ScheduledAt == nil:
			return 1
		case a.ScheduledAt != nil && b.ScheduledAt != nil:
			if c := a.ScheduledAt.Compare(*b.ScheduledAt); c != 0 {
				return c
			}
		}
		return a.CreatedAt.Compare(b.CreatedAt)
	})

	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (j jobStore) MarkAssigned(ctx context.Context, id, deviceID uuid.UUID, subscriptionID int32, at time.Time) error {
	defer j.s.lock()()
	if err := j.s.takeFailure(); err != nil {
		return err
	}
	job, ok := j.s.jobs[id]
	if !ok {
		return store.ErrNotFound
	}
	if job.Status != store.JobPending {
		return store.ErrConflict
	}
	dev := deviceID
	at2 := at
	job.Status = store.JobAssigned
	job.AssignedDeviceID = &dev
	job.AssignedAt = &at2
	job.AssignedSubscriptionID = nil
	if subscriptionID != 0 {
		sub := subscriptionID
		job.AssignedSubscriptionID = &sub
	}
	job.Attempts++
	job.UpdatedAt = at
	return nil
}

func (j jobStore) CountDispatched(ctx context.Context, hourAgo, dayAgo time.Time) ([]store.DispatchCount, error) {
	defer j.s.lock()()
	if err := j.s.takeFailure(); err != nil {
		return nil, err
	}
	type key struct {
		device uuid.UUID
		sub    int32
		hasSub bool
	}
	counts := map[key]*store.DispatchCount{}
	for _, job := range j.s.jobs {
		if job.AssignedDeviceID == nil || job.AssignedAt == nil || job.AssignedAt.Before(dayAgo) {
			continue
		}
		switch job.Status {
		case store.JobAssigned, store.JobSent, store.JobDelivered:
		default:
			continue
		}
		if job.Kind != store.KindSMS {
			continue
		}
		k := key{device: *job.AssignedDeviceID}
		if job.AssignedSubscriptionID != nil {
			k.sub, k.hasSub = *job.AssignedSubscriptionID, true
		}
		c, ok := counts[k]
		if !ok {
			c = &store.DispatchCount{DeviceID: k.device}
			if k.hasSub {
				sub := k.sub
				c.SubscriptionID = &sub
			}
			counts[k] = c
		}
		c.LastDay++
		if !job.AssignedAt.Before(hourAgo) {
			c.LastHour++
		}
	}
	out := make([]store.DispatchCount, 0, len(counts))
	for _, c := range counts {
		out = append(out, *c)
	}
	return out, nil
}

func (j jobStore) Release(ctx context.Context, id uuid.UUID, reason string) error {
	defer j.s.lock()()
	if err := j.s.takeFailure(); err != nil {
		return err
	}
	job, ok := j.s.jobs[id]
	if !ok {
		return store.ErrNotFound
	}
	if job.Status != store.JobAssigned {
		return store.ErrConflict
	}
	job.Status = store.JobPending
	job.AssignedDeviceID = nil
	job.AssignedAt = nil
	job.AssignedSubscriptionID = nil
	job.ErrorMessage = reason
	job.UpdatedAt = time.Now()
	return nil
}

// Complete treats a repeat of the same terminal status as a no-op, because
// results are at-least-once, and accepts a late DELIVERED after SENT, because
// that is a real transition.
func (j jobStore) Complete(ctx context.Context, id uuid.UUID, r store.JobResult) error {
	defer j.s.lock()()
	if err := j.s.takeFailure(); err != nil {
		return err
	}
	job, ok := j.s.jobs[id]
	if !ok {
		return store.ErrNotFound
	}
	if job.Status == r.Status {
		return nil
	}
	if job.Status.Terminal() && !(job.Status == store.JobSent && r.Status == store.JobDelivered) {
		return store.ErrConflict
	}
	at := r.CompletedAt
	job.Status = r.Status
	job.ErrorCode = r.ErrorCode
	job.ErrorMessage = r.ErrorMessage
	// A late DELIVERED carries no part count, so a plain assignment would wipe
	// the one the SENT result reported — and that number is what callers
	// reconcile cost against.
	if r.PartsSent > 0 {
		job.PartsSent = r.PartsSent
	}
	if len(r.Responses) > 0 {
		job.Responses = slices.Clone(r.Responses)
	}
	wipeSensitive(job)
	job.CompletedAt = &at
	job.UpdatedAt = at
	return nil
}

func (j jobStore) Cancel(ctx context.Context, id uuid.UUID, reason string) error {
	defer j.s.lock()()
	if err := j.s.takeFailure(); err != nil {
		return err
	}
	job, ok := j.s.jobs[id]
	if !ok {
		return store.ErrNotFound
	}
	if job.Status.Terminal() {
		return store.ErrConflict
	}
	now := time.Now()
	job.Status = store.JobCancelled
	job.ErrorMessage = reason
	wipeSensitive(job)
	job.CompletedAt = &now
	job.UpdatedAt = now
	return nil
}

func (j jobStore) ListAssignedTo(ctx context.Context, deviceID uuid.UUID) ([]*store.Job, error) {
	defer j.s.lock()()
	var out []*store.Job
	for _, job := range j.s.jobs {
		if job.Status == store.JobAssigned && job.AssignedDeviceID != nil && *job.AssignedDeviceID == deviceID {
			out = append(out, copyJob(job))
		}
	}
	slices.SortFunc(out, func(a, b *store.Job) int { return a.CreatedAt.Compare(b.CreatedAt) })
	return out, nil
}

// List mirrors the Postgres ordering exactly, including the tie-break on id:
// uuid comparison there is a byte-wise memcmp, which is what bytes.Compare
// does here. A fake that broke ties some other way would page differently.
func (j jobStore) List(ctx context.Context, f store.JobFilter) ([]*store.Job, error) {
	defer j.s.lock()()
	if err := j.s.takeFailure(); err != nil {
		return nil, err
	}

	var out []*store.Job
	for _, job := range j.s.jobs {
		if matchesFilter(job, f) {
			out = append(out, copyJob(job))
		}
	}
	slices.SortFunc(out, func(a, b *store.Job) int { return -compareJobOrder(a.CreatedAt, a.ID, b.CreatedAt, b.ID) })
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out, nil
}

func matchesFilter(job *store.Job, f store.JobFilter) bool {
	if f.AccountID != nil && job.AccountID != *f.AccountID {
		return false
	}
	if len(f.Kinds) > 0 && !slices.Contains(f.Kinds, job.Kind) {
		return false
	}
	if len(f.Statuses) > 0 && !slices.Contains(f.Statuses, job.Status) {
		return false
	}
	if f.DeviceID != nil {
		assigned := job.AssignedDeviceID != nil && *job.AssignedDeviceID == *f.DeviceID
		requested := job.RequestedDeviceID != nil && *job.RequestedDeviceID == *f.DeviceID
		if !assigned && !requested {
			return false
		}
	}
	if f.RecipientPrefix != "" && !strings.HasPrefix(job.Recipient, f.RecipientPrefix) {
		return false
	}
	if f.CreatedFrom != nil && job.CreatedAt.Before(*f.CreatedFrom) {
		return false
	}
	if f.CreatedTo != nil && !job.CreatedAt.Before(*f.CreatedTo) {
		return false
	}
	if f.After != nil && compareJobOrder(job.CreatedAt, job.ID, f.After.CreatedAt, f.After.ID) >= 0 {
		return false
	}
	return true
}

// compareJobOrder compares (createdAt, id) pairs ascending.
func compareJobOrder(aAt time.Time, aID uuid.UUID, bAt time.Time, bID uuid.UUID) int {
	if c := aAt.Compare(bAt); c != 0 {
		return c
	}
	return bytes.Compare(aID[:], bID[:])
}

func (j jobStore) CountByBucket(ctx context.Context, scope store.Scope, from time.Time, bucket time.Duration) ([]store.BucketCount, error) {
	defer j.s.lock()()
	if err := j.s.takeFailure(); err != nil {
		return nil, err
	}
	type key struct {
		start  int64
		status store.JobStatus
	}
	counts := map[key]int{}
	for _, job := range j.s.jobs {
		if job.CreatedAt.Before(from) || !scope.Includes(job.AccountID) || job.Kind != store.KindSMS {
			continue
		}
		n := job.CreatedAt.Sub(from) / bucket
		counts[key{from.Add(n * bucket).UnixNano(), job.Status}]++
	}
	out := make([]store.BucketCount, 0, len(counts))
	for k, n := range counts {
		out = append(out, store.BucketCount{Start: time.Unix(0, k.start).UTC(), Status: k.status, Count: n})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Start.Before(out[b].Start) })
	return out, nil
}

func (j jobStore) CountByStatus(ctx context.Context, scope store.Scope, since time.Time) (map[store.JobStatus]int, error) {
	defer j.s.lock()()
	if err := j.s.takeFailure(); err != nil {
		return nil, err
	}
	out := make(map[store.JobStatus]int)
	for _, job := range j.s.jobs {
		if !job.CreatedAt.Before(since) && scope.Includes(job.AccountID) && job.Kind == store.KindSMS {
			out[job.Status]++
		}
	}
	return out, nil
}

func (j jobStore) ClaimCallbacksDue(ctx context.Context, now time.Time, limit int) ([]*store.Job, error) {
	defer j.s.lock()()
	if err := j.s.takeFailure(); err != nil {
		return nil, err
	}
	var out []*store.Job
	for _, job := range j.s.jobs {
		if job.Callback.Due(now) {
			out = append(out, copyJob(job))
		}
	}
	slices.SortFunc(out, func(a, b *store.Job) int {
		return a.Callback.NextAttemptAt.Compare(*b.Callback.NextAttemptAt)
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (j jobStore) MarkCallbackDelivered(ctx context.Context, id uuid.UUID, at time.Time) error {
	defer j.s.lock()()
	if err := j.s.takeFailure(); err != nil {
		return err
	}
	job, ok := j.s.jobs[id]
	if !ok {
		return store.ErrNotFound
	}
	t := at
	job.Callback.DeliveredAt = &t
	job.Callback.NextAttemptAt = nil
	job.UpdatedAt = at
	return nil
}

func (j jobStore) AbandonCallback(ctx context.Context, id uuid.UUID, reason string) error {
	defer j.s.lock()()
	if err := j.s.takeFailure(); err != nil {
		return err
	}
	job, ok := j.s.jobs[id]
	if !ok {
		return store.ErrNotFound
	}
	job.Callback.Attempts++
	job.Callback.NextAttemptAt = nil
	job.Callback.LastError = reason
	job.UpdatedAt = time.Now()
	return nil
}

func (j jobStore) ScheduleCallbackRetry(ctx context.Context, id uuid.UUID, nextAt time.Time, lastErr string) error {
	defer j.s.lock()()
	if err := j.s.takeFailure(); err != nil {
		return err
	}
	job, ok := j.s.jobs[id]
	if !ok {
		return store.ErrNotFound
	}
	t := nextAt
	job.Callback.Attempts++
	job.Callback.NextAttemptAt = &t
	job.Callback.LastError = lastErr
	job.UpdatedAt = nextAt
	return nil
}

// ---------------------------------------------------------------------------
// EnrollmentStore
// ---------------------------------------------------------------------------

type enrollmentStore struct{ s *Store }

func (e enrollmentStore) Create(ctx context.Context, accountID uuid.UUID, tokenHash string, expiresAt time.Time) (*store.EnrollmentToken, error) {
	defer e.s.lock()()
	if err := e.s.takeFailure(); err != nil {
		return nil, err
	}
	if _, exists := e.s.enrollments[tokenHash]; exists {
		return nil, store.ErrConflict
	}
	t := &store.EnrollmentToken{
		ID:        uuid.New(),
		AccountID: store.AccountOrDefault(accountID),
		ExpiresAt: expiresAt,
		CreatedAt: time.Now(),
	}
	e.s.enrollments[tokenHash] = t
	return copyToken(t), nil
}

func (e enrollmentStore) Lookup(ctx context.Context, tokenHash string) (*store.EnrollmentToken, error) {
	defer e.s.lock()()
	t, ok := e.s.enrollments[tokenHash]
	if !ok {
		return nil, store.ErrNotFound
	}
	return copyToken(t), nil
}

// Consume reports reuse before expiry, because an operator seeing "already
// redeemed" needs to find out which phone claimed it, while "expired" just
// means mint another.
func (e enrollmentStore) Consume(ctx context.Context, tokenHash string, deviceID uuid.UUID, at time.Time) (*store.EnrollmentToken, error) {
	defer e.s.lock()()
	if err := e.s.takeFailure(); err != nil {
		return nil, err
	}
	t, ok := e.s.enrollments[tokenHash]
	if !ok {
		return nil, store.ErrNotFound
	}
	if t.ConsumedAt != nil {
		return nil, store.ErrConflict
	}
	if !at.Before(t.ExpiresAt) {
		return nil, store.ErrTokenExpired
	}
	when := at
	dev := deviceID
	t.ConsumedAt = &when
	t.DeviceID = &dev
	return copyToken(t), nil
}

func (e enrollmentStore) DeleteExpired(ctx context.Context, before time.Time) (int64, error) {
	defer e.s.lock()()
	if err := e.s.takeFailure(); err != nil {
		return 0, err
	}
	var n int64
	for hash, t := range e.s.enrollments {
		if t.ConsumedAt == nil && t.ExpiresAt.Before(before) {
			delete(e.s.enrollments, hash)
			n++
		}
	}
	return n, nil
}

// ---------------------------------------------------------------------------
// EventStore
// ---------------------------------------------------------------------------

type eventStore struct{ s *Store }

func (e eventStore) Append(ctx context.Context, in *store.JobEvent) error {
	defer e.s.lock()()
	cp := *in
	e.s.nextEventID++
	cp.ID = e.s.nextEventID
	if cp.CreatedAt.IsZero() {
		cp.CreatedAt = time.Now()
	}
	e.s.events = append(e.s.events, &cp)
	return nil
}

func (e eventStore) ListByJob(ctx context.Context, jobID uuid.UUID) ([]*store.JobEvent, error) {
	defer e.s.lock()()
	var out []*store.JobEvent
	for _, ev := range e.s.events {
		if ev.JobID == jobID {
			cp := *ev
			out = append(out, &cp)
		}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// SeedJob inserts a job directly, bypassing validation, and returns a copy.
func (s *Store) SeedJob(j *store.Job) *store.Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *j
	if cp.ID == uuid.Nil {
		cp.ID = uuid.New()
	}
	cp.AccountID = store.AccountOrDefault(cp.AccountID)
	if cp.Status == "" {
		cp.Status = store.JobPending
	}
	if cp.Kind == "" {
		cp.Kind = store.KindSMS
	}
	if cp.CreatedAt.IsZero() {
		cp.CreatedAt = time.Now()
	}
	s.jobs[cp.ID] = &cp
	return copyJob(&cp)
}

// SeedDevice inserts a device directly and returns a copy.
func (s *Store) SeedDevice(d *store.Device) *store.Device {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *d
	if cp.ID == uuid.Nil {
		cp.ID = uuid.New()
	}
	cp.AccountID = store.AccountOrDefault(cp.AccountID)
	cp.Sims = append([]store.SimInfo(nil), d.Sims...)
	cp.Health = copyHealth(d.Health)
	s.devices[cp.ID] = &cp
	return copyDevice(&cp)
}

// JobByID returns a job for assertions, or nil if there is none.
func (s *Store) JobByID(id uuid.UUID) *store.Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	if !ok {
		return nil
	}
	return copyJob(j)
}

// AllEvents returns every recorded event, in insertion order.
func (s *Store) AllEvents() []*store.JobEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*store.JobEvent, 0, len(s.events))
	for _, e := range s.events {
		cp := *e
		out = append(out, &cp)
	}
	return out
}

// ---------------------------------------------------------------------------
// copies
// ---------------------------------------------------------------------------
//
// Everything crossing the boundary is copied, matching the real store: a caller
// that mutated a returned struct and saw the change persist would be relying on
// behaviour Postgres will not reproduce.

func copyDevice(d *store.Device) *store.Device {
	if d == nil {
		return nil
	}
	cp := *d
	cp.Sims = append([]store.SimInfo(nil), d.Sims...)
	cp.Capabilities = slices.Clone(d.Capabilities)
	cp.Health = copyHealth(d.Health)
	cp.LastSeenAt = copyTime(d.LastSeenAt)
	return &cp
}

func copyHealth(h *store.DeviceHealth) *store.DeviceHealth {
	if h == nil {
		return nil
	}
	cp := *h
	cp.Sims = append([]store.SimHealth(nil), h.Sims...)
	return &cp
}

func copyJob(j *store.Job) *store.Job {
	if j == nil {
		return nil
	}
	cp := *j
	cp.ScheduledAt = copyTime(j.ScheduledAt)
	cp.ExpiresAt = copyTime(j.ExpiresAt)
	cp.AssignedAt = copyTime(j.AssignedAt)
	cp.CompletedAt = copyTime(j.CompletedAt)
	cp.RequestedDeviceID = copyUUID(j.RequestedDeviceID)
	cp.AssignedDeviceID = copyUUID(j.AssignedDeviceID)
	// Pointers into the stored job: copied, or a caller could edit the store
	// through the snapshot it was handed.
	if j.SubscriptionID != nil {
		sub := *j.SubscriptionID
		cp.SubscriptionID = &sub
	}
	if j.AssignedSubscriptionID != nil {
		sub := *j.AssignedSubscriptionID
		cp.AssignedSubscriptionID = &sub
	}
	cp.Callback.NextAttemptAt = copyTime(j.Callback.NextAttemptAt)
	cp.Callback.DeliveredAt = copyTime(j.Callback.DeliveredAt)
	cp.Replies = slices.Clone(j.Replies)
	cp.Responses = slices.Clone(j.Responses)
	return &cp
}

func copyToken(t *store.EnrollmentToken) *store.EnrollmentToken {
	if t == nil {
		return nil
	}
	cp := *t
	cp.ConsumedAt = copyTime(t.ConsumedAt)
	cp.DeviceID = copyUUID(t.DeviceID)
	return &cp
}

func copyTime(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	c := *t
	return &c
}

func copyUUID(u *uuid.UUID) *uuid.UUID {
	if u == nil {
		return nil
	}
	c := *u
	return &c
}

// Ping always succeeds: an in-memory store has nothing to be unreachable.
func (s *Store) Ping(context.Context) error { return nil }

type accountStore struct{ s *Store }

func (a accountStore) Create(ctx context.Context, name string) (*store.Account, error) {
	defer a.s.lock()()
	if err := a.s.takeFailure(); err != nil {
		return nil, err
	}
	acc := &store.Account{ID: uuid.New(), Name: name, CreatedAt: time.Now().UTC()}
	a.s.accounts[acc.ID] = acc
	c := *acc
	return &c, nil
}

func (a accountStore) Get(ctx context.Context, id uuid.UUID) (*store.Account, error) {
	defer a.s.lock()()
	acc, ok := a.s.accounts[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	c := *acc
	return &c, nil
}

func (a accountStore) List(ctx context.Context) ([]*store.Account, error) {
	defer a.s.lock()()
	out := make([]*store.Account, 0, len(a.s.accounts))
	for _, acc := range a.s.accounts {
		c := *acc
		out = append(out, &c)
	}
	slices.SortFunc(out, func(x, y *store.Account) int {
		if c := x.CreatedAt.Compare(y.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(x.ID.String(), y.ID.String())
	})
	return out, nil
}

// wipeSensitive mirrors the Postgres store: a sensitive job loses its code and
// replies once it is over.
func wipeSensitive(job *store.Job) {
	if job.Sensitive {
		job.Body = ""
		job.Replies = nil
	}
}
