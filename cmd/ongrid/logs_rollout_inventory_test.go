package main

import (
	"testing"

	managermodeledge "github.com/ongridio/ongrid/internal/manager/model/edge"
)

func TestIsHostLogsRolloutEdge(t *testing.T) {
	hostDeviceID := uint64(122)
	zeroDeviceID := uint64(0)
	tests := []struct {
		name string
		edge *managermodeledge.Edge
		want bool
	}{
		{name: "host edge", edge: &managermodeledge.Edge{ID: 64, DeviceID: &hostDeviceID}, want: true},
		{name: "controller without host", edge: &managermodeledge.Edge{ID: 63}, want: false},
		{name: "zero host id", edge: &managermodeledge.Edge{ID: 63, DeviceID: &zeroDeviceID}, want: false},
		{name: "zero edge id", edge: &managermodeledge.Edge{DeviceID: &hostDeviceID}, want: false},
		{name: "nil edge", edge: nil, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isHostLogsRolloutEdge(tt.edge); got != tt.want {
				t.Fatalf("isHostLogsRolloutEdge() = %v, want %v", got, tt.want)
			}
		})
	}
}
