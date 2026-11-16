package privacy

import (
	"reflect"
	"testing"
)

func TestPublisherReachesEverySubscriber(t *testing.T) {
	p := NewPublisher()
	var first, second []Withdrawal
	p.Subscribe(func(w Withdrawal) { first = append(first, w) })
	p.Subscribe(func(w Withdrawal) { second = append(second, w) })
	want := Withdrawal{Scope: Scope{Kind: Source, SourceKind: "mail", ID: "m1"}}
	p.Publish(want)
	if !reflect.DeepEqual(first, []Withdrawal{want}) || !reflect.DeepEqual(second, []Withdrawal{want}) {
		t.Fatalf("first=%+v second=%+v, want one %+v each", first, second, want)
	}
}

func TestPublisherUnsubscribeStopsEvents(t *testing.T) {
	p := NewPublisher()
	count := 0
	unsubscribe := p.Subscribe(func(Withdrawal) { count++ })
	p.Publish(Withdrawal{Scope: Scope{Kind: PolicyChange}})
	unsubscribe()
	p.Publish(Withdrawal{Scope: Scope{Kind: PolicyChange}})
	if count != 1 {
		t.Fatalf("count=%d, want 1", count)
	}
}

func TestPublisherAllowsSubscribeFromSubscriber(t *testing.T) {
	p := NewPublisher()
	nested := 0
	p.Subscribe(func(Withdrawal) {
		p.Subscribe(func(Withdrawal) { nested++ })
	})
	p.Publish(Withdrawal{Retention: true})
	p.Publish(Withdrawal{Retention: true})
	if nested != 1 {
		t.Fatalf("nested=%d, want 1 (added during the first publish, called on the second)", nested)
	}
}

func TestScopeKindWireValues(t *testing.T) {
	// The values are saved and compared as strings; they must not change.
	got := []ScopeKind{App, Window, Session, Source, TimeRange, Account, PolicyChange}
	want := []ScopeKind{"app", "window", "session", "source", "time_range", "account", "policy changed"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("kinds=%q, want %q", got, want)
	}
}

func TestPublisherRunsFirstSubscriberBeforeOthers(t *testing.T) {
	p := NewPublisher()
	var got []string
	for i := 0; i < 5; i++ {
		p.Subscribe(func(Withdrawal) { got = append(got, "other") })
	}
	p.SetFirst(func(Withdrawal) { got = append(got, "first") })
	p.Publish(Withdrawal{})
	if len(got) != 6 || got[0] != "first" {
		t.Fatalf("order = %v, want first before the others", got)
	}
}
