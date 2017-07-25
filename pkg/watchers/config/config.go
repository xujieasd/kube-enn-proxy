/*
just copy code from kubernetes/pkg/util/config/config.go

 */


package util

import (
	"sync"
)

type Operation int

const (
	ADD Operation = iota
	UPDATE
	REMOVE
	//SYNCED
)

type Listener interface {
	// OnUpdate is invoked when a change is made to an object.
	OnUpdate(instance interface{})
}

// ListenerFunc receives a representation of the change or object.
type ListenerFunc func(instance interface{})

func (f ListenerFunc) OnUpdate(instance interface{}) {
	f(instance)
}

type Broadcaster struct {
	// Listeners for changes and their lock.
	listenerLock sync.RWMutex
	listeners    []Listener
}

// NewBroadcaster registers a set of listeners that support the Listener interface
// and notifies them all on changes.
func NewBroadcaster() *Broadcaster {
	return &Broadcaster{}
}

// Add registers listener to receive updates of changes.
func (b *Broadcaster) Add(listener Listener) {
	b.listenerLock.Lock()
	defer b.listenerLock.Unlock()
	b.listeners = append(b.listeners, listener)
}

// Notify notifies all listeners.
func (b *Broadcaster) Notify(instance interface{}) {
	b.listenerLock.RLock()
	listeners := b.listeners
	b.listenerLock.RUnlock()
	for _, listener := range listeners {
		//listener.OnUpdate(instance)
		go listener.OnUpdate(instance)
	}
}
