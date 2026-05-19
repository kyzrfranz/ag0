package memory

import (
	"context"
	"errors"
	"os"
	"sync"
)

const (
	envMongoURI        = "MONGODB_URI"
	profilesCollection = "profiles"
)

// ErrNotFound is returned when no profile exists for the given id.
var ErrNotFound = errors.New("profile not found")

// Profile is a flexible per-user profile. Callers store whatever they
// need under string keys.
type Profile map[string]any

// Store reads and writes user profiles.
type Store interface {
	Get(ctx context.Context, id string) (Profile, error)
	Save(ctx context.Context, id string, p Profile) error
}

// InMemoryStore is a goroutine-safe in-memory Store, useful for tests
// and local development without MongoDB.
type InMemoryStore struct {
	mu       sync.RWMutex
	profiles map[string]Profile
}

// NewInMemoryStore constructs an empty InMemoryStore.
func NewInMemoryStore() *InMemoryStore {
	return &InMemoryStore{profiles: make(map[string]Profile)}
}

// Get returns a copy of the profile for id, or ErrNotFound.
func (s *InMemoryStore) Get(ctx context.Context, id string) (Profile, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.profiles[id]
	if !ok {
		return nil, ErrNotFound
	}
	return clone(p), nil
}

// Save stores a copy of p under id, overwriting any existing profile.
func (s *InMemoryStore) Save(ctx context.Context, id string, p Profile) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.profiles[id] = clone(p)
	return nil
}

func clone(p Profile) Profile {
	out := make(Profile, len(p))
	for k, v := range p {
		out[k] = v
	}
	return out
}

// MongoStore is a MongoDB-backed Store using the "profiles" collection.
type MongoStore struct {
	URI        string
	Collection string
}

// NewMongoStore constructs a MongoStore from MONGODB_URI.
func NewMongoStore() (*MongoStore, error) {
	// TODO: connect to MongoDB.
	return &MongoStore{
		URI:        os.Getenv(envMongoURI),
		Collection: profilesCollection,
	}, nil
}

// Get loads a profile by id from MongoDB.
func (s *MongoStore) Get(ctx context.Context, id string) (Profile, error) {
	// TODO: query s.Collection for {_id: id}.
	return Profile{}, nil
}

// Save upserts a profile by id into MongoDB.
func (s *MongoStore) Save(ctx context.Context, id string, p Profile) error {
	// TODO: upsert into s.Collection.
	return nil
}