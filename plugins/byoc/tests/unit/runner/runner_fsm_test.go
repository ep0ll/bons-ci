package runner_test

import (
	"testing"

	"github.com/bons/bons-ci/plugins/byoc/internal/store"
	"github.com/stretchr/testify/assert"
)

// validTransitions mirrors the FSM from runner/service.go — kept in sync manually.
// In production, this table is the exported const in the runner package.
var validTransitions = map[store.RunnerStatus][]store.RunnerStatus{
	store.RunnerStatusProvisioning: {store.RunnerStatusRegistering, store.RunnerStatusTerminated},
	store.RunnerStatusRegistering:  {store.RunnerStatusIdle, store.RunnerStatusTerminated},
	store.RunnerStatusIdle:         {store.RunnerStatusBusy, store.RunnerStatusTerminating},
	store.RunnerStatusBusy:         {store.RunnerStatusTerminating, store.RunnerStatusIdle},
	store.RunnerStatusTerminating:  {store.RunnerStatusTerminated},
	store.RunnerStatusTerminated:   {},
}

func isValidTransition(from, to store.RunnerStatus) bool {
	targets, ok := validTransitions[from]
	if !ok {
		return false
	}
	for _, t := range targets {
		if t == to {
			return true
		}
	}
	return false
}

func TestRunnerFSM_ValidTransitions(t *testing.T) {
	validCases := []struct {
		from store.RunnerStatus
		to   store.RunnerStatus
	}{
		{store.RunnerStatusProvisioning, store.RunnerStatusRegistering},
		{store.RunnerStatusProvisioning, store.RunnerStatusTerminated},
		{store.RunnerStatusRegistering, store.RunnerStatusIdle},
		{store.RunnerStatusRegistering, store.RunnerStatusTerminated},
		{store.RunnerStatusIdle, store.RunnerStatusBusy},
		{store.RunnerStatusIdle, store.RunnerStatusTerminating},
		{store.RunnerStatusBusy, store.RunnerStatusTerminating},
		{store.RunnerStatusBusy, store.RunnerStatusIdle},
		{store.RunnerStatusTerminating, store.RunnerStatusTerminated},
	}

	for _, tc := range validCases {
		t.Run(string(tc.from)+"→"+string(tc.to), func(t *testing.T) {
			assert.True(t, isValidTransition(tc.from, tc.to),
				"expected %s→%s to be valid", tc.from, tc.to)
		})
	}
}

func TestRunnerFSM_InvalidTransitions(t *testing.T) {
	invalidCases := []struct {
		from store.RunnerStatus
		to   store.RunnerStatus
	}{
		{store.RunnerStatusTerminated, store.RunnerStatusIdle},
		{store.RunnerStatusTerminated, store.RunnerStatusBusy},
		{store.RunnerStatusTerminated, store.RunnerStatusProvisioning},
		{store.RunnerStatusIdle, store.RunnerStatusProvisioning},
		{store.RunnerStatusBusy, store.RunnerStatusRegistering},
		{store.RunnerStatusTerminating, store.RunnerStatusIdle},
		{store.RunnerStatusTerminating, store.RunnerStatusBusy},
	}

	for _, tc := range invalidCases {
		t.Run(string(tc.from)+"→"+string(tc.to), func(t *testing.T) {
			assert.False(t, isValidTransition(tc.from, tc.to),
				"expected %s→%s to be INVALID", tc.from, tc.to)
		})
	}
}

func TestRunnerFSM_TerminalState_NoOutgoingEdges(t *testing.T) {
	targets, ok := validTransitions[store.RunnerStatusTerminated]
	assert.True(t, ok, "Terminated must be in the transition map")
	assert.Empty(t, targets, "Terminated must have no outgoing transitions")
}
