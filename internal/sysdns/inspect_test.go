package sysdns

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestInspectWith(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		expected, actual           []string
		itemError                  string
		readError                  error
		wantMismatch, wantComplete bool
	}{
		{"match", []string{"127.0.0.1", "::1"}, []string{"::1", "127.0.0.1"}, "", nil, false, true},
		{"mismatch", []string{"127.0.0.1"}, []string{"192.168.1.1"}, "", nil, true, true},
		{"missing", []string{"127.0.0.1"}, nil, "", nil, true, true},
		{"observe only", nil, []string{"192.168.1.1"}, "", nil, false, true},
		{"unknown", []string{"127.0.0.1"}, nil, "автоматический DNS неизвестен", nil, false, false},
		{"read error", nil, nil, "", errors.New("fake"), false, false},
		{"equivalent ipv6", []string{"2001:db8::1"}, []string{"2001:0db8:0:0:0:0:0:1"}, "", nil, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := append([]string{}, tc.expected...)
			got := inspectWith(context.Background(), tc.expected, func(ctx context.Context, expected []string, emit func(DNSInterface) bool) error {
				emit(DNSInterface{Key: "link", Name: "link", Expected: expected, Actual: tc.actual, Error: tc.itemError})
				return tc.readError
			})
			if got.Mismatch != tc.wantMismatch || got.Complete != tc.wantComplete {
				t.Fatalf("%+v", got)
			}
			if !reflect.DeepEqual(got.Expected, original) {
				t.Fatal("expected changed")
			}
		})
	}
}

func TestInspectCancellationPreservesPartial(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	got := inspectWith(ctx, []string{"127.0.0.1"}, func(ctx context.Context, expected []string, emit func(DNSInterface) bool) error {
		defer close(done)
		if !emit(DNSInterface{Key: "one", Expected: expected, Actual: expected}) {
			return ctx.Err()
		}
		cancel()
		<-ctx.Done()
		return ctx.Err()
	})
	if got.Complete || len(got.Interfaces) != 1 || got.Error == "" {
		t.Fatalf("%+v", got)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reader leaked")
	}
}

func TestInspectEmptyAndCancelled(t *testing.T) {
	empty := inspectWith(context.Background(), nil, func(context.Context, []string, func(DNSInterface) bool) error { return nil })
	if empty.Complete || empty.Error == "" {
		t.Fatalf("%+v", empty)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	inspectWith(ctx, nil, func(context.Context, []string, func(DNSInterface) bool) error { t.Fatal("reader ran"); return nil })
}
