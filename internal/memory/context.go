package memory

import (
	"context"

	"github.com/kyzrfranz/ag0/internal/llm"
)

// AgentContext is the data assembled for a single agent invocation:
// the user's profile plus recent conversation history. Pure data, no
// behavior.
type AgentContext struct {
	Profile Profile
	History []llm.Message
}

// Builder assembles an AgentContext via a fluent API.
type Builder struct {
	store   Store
	profile Profile
	history []llm.Message
}

// NewBuilder constructs a Builder backed by store.
func NewBuilder(store Store) *Builder {
	return &Builder{store: store, profile: Profile{}}
}

// WithProfile loads the profile for id from the store. A missing or
// failed load degrades to an empty profile so Build always succeeds.
func (b *Builder) WithProfile(ctx context.Context, id string) *Builder {
	if p, err := b.store.Get(ctx, id); err == nil {
		b.profile = p
	}
	return b
}

// WithHistory attaches recent conversation messages.
func (b *Builder) WithHistory(msgs []llm.Message) *Builder {
	b.history = msgs
	return b
}

// Build returns the assembled AgentContext.
func (b *Builder) Build() AgentContext {
	return AgentContext{Profile: b.profile, History: b.history}
}