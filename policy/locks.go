package policy

import (
	"encoding/json"
	"fmt"
)

// Locks are automations a device's rules must keep as they are, by id, each
// with why — an automation a payment method installed, say, which only goes
// with that method. An embedding program says which (api.Options.Locks); the
// gateway alone locks none.
type Locks map[string]string

// LockError is a change to a locked automation.
type LockError struct {
	ID, Name, Reason string
	Removed          bool
}

func (e *LockError) Error() string {
	verb := "changed"
	if e.Removed {
		verb = "removed"
	}
	return fmt.Sprintf("automation %q is locked (%s): it cannot be %s here", e.Name, e.Reason, verb)
}

// CheckLocks refuses next when it drops or changes a locked automation of
// current. A lock whose automation current no longer has binds nothing.
// Compared as JSON, so an empty list and a missing one are the same.
func CheckLocks(current, next Policy, locks Locks) error {
	for id, reason := range locks {
		was, ok := current.Automation(id)
		if !ok {
			continue
		}
		now, ok := next.Automation(id)
		if !ok {
			return &LockError{ID: id, Name: was.Name, Reason: reason, Removed: true}
		}
		a, _ := json.Marshal(was)
		b, _ := json.Marshal(now)
		if string(a) != string(b) {
			return &LockError{ID: id, Name: was.Name, Reason: reason}
		}
	}
	return nil
}

// Duplicate is a copy of a under no id (one is given when it is saved),
// disabled until switched on — two copies on the same SMS would both act — and
// named as a copy. A webhook's secret is dropped, for a new one.
func Duplicate(a Automation) Automation {
	raw, _ := json.Marshal(a)
	var c Automation
	_ = json.Unmarshal(raw, &c)
	c.ID = ""
	c.Enabled = false
	c.Name = a.Name + " (copie)"
	if r := []rune(c.Name); len(r) > maxNameLen {
		c.Name = string(r[:maxNameLen])
	}
	for i := range c.Actions {
		c.Actions[i].Secret = ""
	}
	return c
}
