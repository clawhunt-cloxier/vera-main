package dataservice

import (
	"context"
	"testing"
	"time"
)

// mockProvider is a minimal Provider for testing the production guard.
type mockProvider struct{ synthetic bool }

func (m *mockProvider) ID() string                                   { return "mock" }
func (m *mockProvider) Name() string                                 { return "Mock" }
func (m *mockProvider) Synthetic() bool                              { return m.synthetic }
func (m *mockProvider) AllowModelProcessing() bool                   { return true }
func (m *mockProvider) Basis() map[string]string                     { return nil }
func (m *mockProvider) Now() time.Time                               { return time.Now() }
func (m *mockProvider) Entities(_ context.Context) ([]Entity, error) { return nil, nil }
func (m *mockProvider) Records(_ context.Context) ([]Record, error)  { return nil, nil }
func (m *mockProvider) Capabilities(_ context.Context) ([]Capability, error) {
	return nil, nil
}

func TestGuardProductionRejectsSynthetic(t *testing.T) {
	p := &mockProvider{synthetic: true}
	if err := GuardProduction(p, true); err == nil {
		t.Fatal("expected error for synthetic provider in production, got nil")
	}
}

func TestGuardProductionAllowsRealProvider(t *testing.T) {
	p := &mockProvider{synthetic: false}
	if err := GuardProduction(p, true); err != nil {
		t.Fatalf("unexpected error for real provider: %v", err)
	}
}

func TestGuardProductionAllowsSyntheticInDev(t *testing.T) {
	p := &mockProvider{synthetic: true}
	if err := GuardProduction(p, false); err != nil {
		t.Fatalf("unexpected error for synthetic provider in dev: %v", err)
	}
}
