// Package identity resolves chat senders, speakers, and other captured
// signals back to the Principal entities stored in storage.Store. It is the
// semantic layer that turns "@xxx said Y" into "<this is Self / a known
// Contact / an anonymous bucket>" so the activity pipeline downstream can
// decide whether a statement is attributable to the local user.
package identity

import (
	"context"
	"strings"

	"wails-gui/storage"
)

// SubjectKind is the resolved attribution category.
type SubjectKind string

const (
	SubjectSelf      SubjectKind = "self"
	SubjectContact   SubjectKind = "contact"
	SubjectAnonymous SubjectKind = "anonymous"
	SubjectUnknown   SubjectKind = "unknown"
)

// AttributionMethod records which lookup path produced the resolution.
// Useful for telemetry and for debugging mis-attributions reported by users.
type AttributionMethod string

const (
	MethodHandleID      AttributionMethod = "handle_id"
	MethodDisplayHandle AttributionMethod = "display_handle"
	MethodAlias         AttributionMethod = "alias"
	MethodAutoAnonymous AttributionMethod = "auto_anonymous"
	MethodUnknown       AttributionMethod = "unknown"
)

// SubjectRef is the resolver's output: the resolved kind plus enough
// metadata for downstream prompt builders and write gates.
type SubjectRef struct {
	PrincipalID  string
	Kind         SubjectKind
	DisplayName  string
	Relationship string
	Method       AttributionMethod
	Confidence   float64
}

// IsSelf is a convenience for callers that only care about "is this the
// user's own message?". Anonymous and unknown both report false.
func (s SubjectRef) IsSelf() bool { return s.Kind == SubjectSelf }

// Storage is the subset of storage.Store the resolver depends on. Defined as
// an interface so resolver tests can inject a fake without spinning up
// SQLite, and so the resolver stays decoupled from storage internals.
type Storage interface {
	ResolveByAppHandle(ctx context.Context, app, handleID string) (*storage.PrincipalRow, error)
	ResolveByDisplayHandle(ctx context.Context, app, displayHandle string) (*storage.PrincipalRow, error)
	ResolveByAlias(ctx context.Context, name string) (*storage.PrincipalRow, error)
	EnsureAnonymousContact(ctx context.Context, app, handleID, displayHandle string) (storage.PrincipalRow, error)
}

// Resolver resolves a captured sender into a SubjectRef.
type Resolver struct {
	store Storage
	// autoBucket controls whether unmatched senders should be persisted as
	// anonymous Contact buckets. Production callers want this on (so the
	// onboarding UI can later prompt "who is this?"). Read-only callers and
	// tests turn it off.
	autoBucket bool
}

// NewResolver returns a resolver backed by store. autoBucket controls
// whether unknown senders are auto-persisted as anonymous contacts.
func NewResolver(store Storage, autoBucket bool) *Resolver {
	return &Resolver{store: store, autoBucket: autoBucket}
}

// ResolveSender attempts, in order:
//
//  1. (app, handleID) lookup against app_handles.handle_id
//  2. (app, displayName) lookup against app_handles.display_handle
//  3. displayName lookup against principals.display_name and aliases
//  4. If autoBucket: ensure an anonymous-bucket Contact for (app, handleID|displayName),
//     unless the name is a platform or integration account (IsSystemSender)
//
// Returns an unknown SubjectRef when no signal at all can be derived.
// The returned error is non-nil only for storage failures — a clean "no
// match" is conveyed by Kind=SubjectUnknown with Method=MethodUnknown.
func (r *Resolver) ResolveSender(ctx context.Context, app, handleID, displayName string) (SubjectRef, error) {
	return r.resolve(ctx, app, handleID, displayName, r.autoBucket)
}

// ResolveExistingSender is ResolveSender without creating anything: a
// sender the person already named still resolves, an unknown one stays
// unknown. Callers use it for senders marked as apps or bots.
func (r *Resolver) ResolveExistingSender(ctx context.Context, app, handleID, displayName string) (SubjectRef, error) {
	return r.resolve(ctx, app, handleID, displayName, false)
}

func (r *Resolver) resolve(ctx context.Context, app, handleID, displayName string, autoBucket bool) (SubjectRef, error) {
	app = strings.TrimSpace(strings.ToLower(app))
	handleID = strings.TrimSpace(handleID)
	displayName = CollapseNameSpace(displayName)

	if app != "" && handleID != "" {
		if p, err := r.store.ResolveByAppHandle(ctx, app, handleID); err != nil {
			return SubjectRef{}, err
		} else if p != nil {
			return toSubjectRef(*p, MethodHandleID), nil
		}
	}

	if app != "" && displayName != "" {
		if p, err := r.store.ResolveByDisplayHandle(ctx, app, displayName); err != nil {
			return SubjectRef{}, err
		} else if p != nil {
			return toSubjectRef(*p, MethodDisplayHandle), nil
		}
	}

	if displayName != "" {
		if p, err := r.store.ResolveByAlias(ctx, displayName); err != nil {
			return SubjectRef{}, err
		} else if p != nil {
			return toSubjectRef(*p, MethodAlias), nil
		}
	}

	if autoBucket && app != "" && (handleID != "" || displayName != "") && IsChatApp(app) && !IsSystemSender(displayName) {
		// Guard against message body / system-notice text that bled into
		// the sender field becoming a permanent anonymous principal. A
		// stable handleID is trustworthy on its own; a display-name-only
		// sighting must look like a real name (see PlausibleAutoContactName
		// — motivated by a Discord welcome-card heading leaking through).
		if handleID != "" || PlausibleAutoContactName(displayName) {
			p, err := r.store.EnsureAnonymousContact(ctx, app, handleID, displayName)
			if err != nil {
				return SubjectRef{}, err
			}
			return toSubjectRef(p, MethodAutoAnonymous), nil
		}
	}

	return SubjectRef{
		Kind:        SubjectUnknown,
		DisplayName: displayName,
		Method:      MethodUnknown,
		Confidence:  0,
	}, nil
}

func toSubjectRef(p storage.PrincipalRow, method AttributionMethod) SubjectRef {
	return SubjectRef{
		PrincipalID:  p.ID,
		Kind:         principalKindToSubjectKind(p.Kind),
		DisplayName:  p.DisplayName,
		Relationship: p.Relationship,
		Method:       method,
		Confidence:   p.Confidence,
	}
}

func principalKindToSubjectKind(kind string) SubjectKind {
	switch kind {
	case storage.PrincipalKindSelf:
		return SubjectSelf
	case storage.PrincipalKindContact:
		return SubjectContact
	case storage.PrincipalKindAnonymous:
		return SubjectAnonymous
	default:
		return SubjectUnknown
	}
}
