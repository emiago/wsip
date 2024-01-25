package psip

import (
	"errors"
	"sync"
	"time"

	"github.com/emiago/sipgo/sip"
)

var (
	ErrRegistryDoesNotExist = errors.New("registry does not exist")
)

type RegistryStore interface {
	// TODO add context
	CreateRegisterBinding(r RegisterBinding) error
	DeleteRegisterBinding(user string) error
	DeleteRegisterBindingContact(user string, contactAddress sip.Uri) error
	GetRegisterBinding(user string) (RegisterBinding, error)
}

type RegisterBinding struct {
	Aor      sip.Uri
	CallID   sip.CallIDHeader
	Contacts []sip.Uri
	Expiry   time.Duration
}

type RegistryMemory struct {
	m map[string]RegisterBinding
	sync.RWMutex
}

func NewRegistryMemory() *RegistryMemory {
	return &RegistryMemory{
		m: make(map[string]RegisterBinding),
	}
}

func (r *RegistryMemory) CreateRegisterBinding(rec RegisterBinding) error {
	r.Lock()
	r.m[rec.Aor.User] = rec
	r.Unlock()
	return nil
}

func (r *RegistryMemory) DeleteRegisterBinding(user string) error {
	r.Lock()
	delete(r.m, user)
	r.Unlock()
	return nil
}

func (r *RegistryMemory) DeleteRegisterBindingContact(user string, contact sip.Uri) error {
	r.Lock()
	rec, ok := r.m[user]
	if !ok {
		return ErrRegistryDoesNotExist
	}
	for i, c := range rec.Contacts {
		if c.String() == contact.String() {
			rec.Contacts = append(rec.Contacts[:i], rec.Contacts[i+1:]...)
			break
		}
	}
	r.Unlock()
	return nil
}

func (r *RegistryMemory) GetRegisterBinding(user string) (RegisterBinding, error) {
	r.RLock()
	defer r.RUnlock()
	rec, ok := r.m[user]
	if !ok {
		return rec, ErrRegistryDoesNotExist
	}
	return rec, nil
}
