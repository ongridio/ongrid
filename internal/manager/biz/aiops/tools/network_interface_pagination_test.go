package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	devicebiz "github.com/ongridio/ongrid/internal/manager/biz/device"
	devicemodel "github.com/ongridio/ongrid/internal/manager/model/device"
)

type interfacePage struct {
	Interfaces []networkInterfaceRow `json:"interfaces"`
	Count      int                   `json:"count"`
	Total      int                   `json:"total"`
	Offset     int                   `json:"offset"`
	Limit      int                   `json:"limit"`
	HasMore    bool                  `json:"has_more"`
	NextOffset *int                  `json:"next_offset"`
}

func largeSwitchTool(t *testing.T) *QueryNetworkInterfacesTool {
	t.Helper()
	rows := make([]networkInterfaceRow, 1203)
	for i := range rows {
		status := "up"
		if i%2 == 0 {
			status = "down"
		}
		rows[i] = networkInterfaceRow{IfIndex: i + 1, Name: fmt.Sprintf("port-%04d", i+1), AdminStatus: "up", OperStatus: status}
	}
	encoded, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	return NewQueryNetworkInterfacesTool(
		&fakeNetworkDeviceReader{getByID: map[uint64]*devicemodel.Device{10: {ID: 10, Name: "core-switch", OS: "network"}}},
		&fakeNetworkDetailReader{details: map[uint64]*devicebiz.NetworkDeviceDetail{10: {Candidate: &devicemodel.NetworkDiscoveryCandidate{InterfacesJSON: string(encoded)}}}}, nil)
}

func readInterfacePage(t *testing.T, tool *QueryNetworkInterfacesTool, args string) interfacePage {
	t.Helper()
	raw, err := tool.InvokableRun(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	var page interfacePage
	if err := json.Unmarshal([]byte(raw), &page); err != nil {
		t.Fatal(err)
	}
	return page
}

func TestNetworkInterfacePaginationTraversesLargeSwitch(t *testing.T) {
	tool := largeSwitchTool(t)
	offset, seen := 0, 0
	for pageNumber := 0; pageNumber < 3; pageNumber++ {
		page := readInterfacePage(t, tool, fmt.Sprintf(`{"network_device_id":10,"limit":500,"offset":%d}`, offset))
		want := 500
		if pageNumber == 2 {
			want = 203
		}
		if page.Count != want || len(page.Interfaces) != want {
			t.Fatalf("page %d returned %d interfaces, want %d", pageNumber, page.Count, want)
		}
		if page.Total != 1203 || page.Offset != offset || page.Limit != 500 {
			t.Fatalf("page metadata: %+v", page)
		}
		for _, row := range page.Interfaces {
			seen++
			if row.IfIndex != seen {
				t.Fatalf("missing or duplicate interface: %d, want %d", row.IfIndex, seen)
			}
		}
		if pageNumber < 2 {
			if !page.HasMore || page.NextOffset == nil {
				t.Fatal("missing continuation")
			}
			offset = *page.NextOffset
		} else if page.HasMore || page.NextOffset != nil {
			t.Fatal("last page advertises continuation")
		}
	}
}

func TestNetworkInterfacePaginationFiltersBeforeOffset(t *testing.T) {
	tool := largeSwitchTool(t)
	page := readInterfacePage(t, tool, `{"network_device_id":10,"only_attention":true,"oper_status":"down","name_contains":"port-","offset":500,"limit":500}`)
	if page.Total != 602 || page.Count != 102 || page.Interfaces[0].IfIndex != 1001 || page.HasMore {
		t.Fatalf("incorrect filtered pagination: total=%d count=%d", page.Total, page.Count)
	}
	for _, row := range page.Interfaces {
		if !row.NeedsAttention {
			t.Fatal("filter returned healthy interface")
		}
	}
}

func TestNetworkInterfacePaginationBoundaries(t *testing.T) {
	tool := largeSwitchTool(t)
	cases := []struct {
		name, args                  string
		count, total, limit, offset int
		more                        bool
	}{
		{"default", `{"network_device_id":10}`, 50, 1203, 50, 0, true},
		{"oversized limit", `{"network_device_id":10,"limit":9999}`, 500, 1203, 500, 0, true},
		{"exact end", `{"network_device_id":10,"offset":1200,"limit":3}`, 3, 1203, 3, 1200, false},
		{"beyond end", `{"network_device_id":10,"offset":1203}`, 0, 1203, 50, 1203, false},
		{"no match", `{"network_device_id":10,"name_contains":"missing"}`, 0, 0, 50, 0, false},
		{"very large offset", fmt.Sprintf(`{"network_device_id":10,"offset":%d}`, int(^uint(0)>>1)), 0, 1203, 50, int(^uint(0) >> 1), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			page := readInterfacePage(t, tool, tc.args)
			if page.Count != tc.count || page.Total != tc.total || page.Limit != tc.limit || page.Offset != tc.offset || page.HasMore != tc.more {
				t.Fatalf("unexpected pagination metadata: count=%d total=%d limit=%d offset=%d has_more=%v", page.Count, page.Total, page.Limit, page.Offset, page.HasMore)
			}
			if page.Interfaces == nil {
				t.Fatal("interfaces must be an array, including empty pages")
			}
			if tc.more {
				if page.NextOffset == nil || *page.NextOffset != tc.offset+tc.count {
					t.Fatal("incorrect next_offset")
				}
			} else if page.NextOffset != nil {
				t.Fatal("unexpected next_offset")
			}
		})
	}
	if _, err := tool.InvokableRun(context.Background(), `{"network_device_id":10,"offset":-1}`); err == nil {
		t.Fatal("negative offset accepted")
	}
}

func TestNetworkInterfacePaginationAdvertisedSchema(t *testing.T) {
	tool := largeSwitchTool(t)
	info, err := tool.Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Properties map[string]struct {
			Minimum int `json:"minimum"`
			Maximum int `json:"maximum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(info.Parameters, &schema); err != nil {
		t.Fatal(err)
	}
	if offset, ok := schema.Properties["offset"]; !ok || offset.Minimum != 0 {
		t.Fatal("offset missing from schema")
	}
	if schema.Properties["limit"].Maximum != 500 {
		t.Fatal("schema and implementation disagree on limit")
	}
}
