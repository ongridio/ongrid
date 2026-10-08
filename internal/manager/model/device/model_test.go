package device

import (
	"reflect"
	"testing"
)

func TestRolesSupportGPUAndCombinations(t *testing.T) {
	tests := []struct {
		name  string
		input []string
		want  uint8
		roles []string
	}{
		{name: "gpu", input: []string{RoleGPU}, want: RoleBitGPU, roles: []string{RoleGPU}},
		{name: "gpu with server", input: []string{RoleServer, RoleGPU}, want: RoleBitServer | RoleBitGPU, roles: []string{RoleServer, RoleGPU}},
		{name: "unknown clears", input: []string{RoleGPU, RoleUnknown}, want: 0, roles: []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := EncodeRoles(tt.input); got != tt.want {
				t.Fatalf("EncodeRoles(%v) = %d, want %d", tt.input, got, tt.want)
			}
			if got := DecodeRoles(tt.want); !reflect.DeepEqual(got, tt.roles) {
				t.Fatalf("DecodeRoles(%d) = %v, want %v", tt.want, got, tt.roles)
			}
		})
	}
}

func TestMatchingRoleValuesIncludesGPUCombinations(t *testing.T) {
	got := MatchingRoleValues(RoleBitGPU)
	want := []int{int(RoleBitGPU), int(RoleBitServer | RoleBitGPU), int(RoleBitStorage | RoleBitGPU), int(RoleBitServer | RoleBitStorage | RoleBitGPU)}
	if len(got) != 16 {
		t.Fatalf("MatchingRoleValues(gpu) returned %d values, want 16", len(got))
	}
	for _, value := range want {
		found := false
		for _, candidate := range got {
			if candidate == value {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("MatchingRoleValues(gpu) does not contain %d", value)
		}
	}
}

func TestRolesRejectUnknownNamesAndBits(t *testing.T) {
	if IsValidRoleName("accelerator") {
		t.Fatal("unknown role name was accepted")
	}
	if IsValidRoles(RoleBitGPU | 1<<5) {
		t.Fatal("reserved role bit was accepted")
	}
}
