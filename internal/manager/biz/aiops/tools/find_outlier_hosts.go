package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// ToolNameFindOutlierHosts is the stable wire name the LLM sees.
const ToolNameFindOutlierHosts = "find_outlier_hosts"

// FindOutlierHostsDescription pushes the model toward this tool when the
// question is about which edges deviate from the fleet baseline.
const FindOutlierHostsDescription = "Find hosts whose metric value is more than N standard deviations above the fleet mean. " +
	"Use this for 'who's an outlier on cpu/mem/disk' style questions. Default sigma is 2."

// FindOutlierHostsSchema is the JSON Schema of the tool's argument object.
var FindOutlierHostsSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "metric": {
      "type": "string",
      "enum": ["cpu", "mem", "disk"],
      "description": "Which closed-set host metric to compare across the fleet."
    },
    "sigma": {
      "type": "number",
      "minimum": 0.5,
      "maximum": 10,
      "description": "z-score threshold (default 2). Hosts with z > sigma are returned."
    }
  },
  "required": ["metric"]
}`)

// FindOutlierHostsArgs is the typed form of FindOutlierHostsSchema.
type FindOutlierHostsArgs struct {
	Metric string  `json:"metric"`
	Sigma  float64 `json:"sigma,omitempty"`
}

// OutlierHostRow is one outlier hit.
type OutlierHostRow struct {
	DeviceID   uint64  `json:"device_id"`
	DeviceName string  `json:"device_name"`
	ZScore     float64 `json:"z_score"`
	Metric     string  `json:"metric"`
}

const outlierCallTimeout = 60 * time.Second

// executeFindOutlierHosts builds a z-score PromQL of the shape
//
//	(per_edge_metric - on() group_left avg(per_edge_metric)) /
//	    on() group_left stddev(per_edge_metric)
//
// then filters > sigma. The result is decorated with edge names like
// rank_hosts does.
func (r *Registry) executeFindOutlierHosts(ctx context.Context, args json.RawMessage) (ExecuteResult, error) {
	if r.devices == nil {
		return ExecuteResult{}, fmt.Errorf("find_outlier_hosts: device usecase not configured")
	}
	result, err := NewFindOutlierHostsTool(r.promQuery, r.devices, r.log).InvokableRun(ctx, string(args))
	if err != nil {
		return ExecuteResult{}, err
	}
	return ExecuteResult{ResultJSON: json.RawMessage(result)}, nil
}
