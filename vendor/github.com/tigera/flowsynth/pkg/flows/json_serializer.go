package flows

import (
	"encoding/json"
	"net"
	"strconv"
	"time"
)

var protoNames = map[int]string{
	1:  "icmp",
	6:  "tcp",
	17: "udp",
	4:  "ipip",
	50: "esp",
	58: "icmp6",
}

// FlowLogJSONOutput represents the JSON representation of a flow log.
type flowLogJSONOutput struct {
	StartTime int64 `json:"start_time"`
	EndTime   int64 `json:"end_time"`

	// Some empty values should be json marshalled as null and NOT with golang null values such as "" for
	// a empty string
	// Having such values as pointers ensures that json marshalling will render it as such.
	SourceIP        *string                  `json:"source_ip"`
	SourceName      string                   `json:"source_name"`
	SourceNameAggr  string                   `json:"source_name_aggr"`
	SourceNamespace string                   `json:"source_namespace"`
	SourcePort      *int64                   `json:"source_port"`
	SourceType      string                   `json:"source_type"`
	SourceLabels    *FlowLogLabelsJSONOutput `json:"source_labels"`
	DestIP          *string                  `json:"dest_ip"`
	DestName        string                   `json:"dest_name"`
	DestNameAggr    string                   `json:"dest_name_aggr"`
	DestNamespace   string                   `json:"dest_namespace"`
	DestPort        *int64                   `json:"dest_port"`
	DestType        string                   `json:"dest_type"`
	DestLabels      *FlowLogLabelsJSONOutput `json:"dest_labels"`
	Proto           string                   `json:"proto"`

	Action   string `json:"action"`
	Reporter string `json:"reporter"`

	Policies *FlowLogPoliciesJSONOutput `json:"policies"`

	BytesIn               int64 `json:"bytes_in"`
	BytesOut              int64 `json:"bytes_out"`
	NumFlows              int64 `json:"num_flows"`
	NumFlowsStarted       int64 `json:"num_flows_started"`
	NumFlowsCompleted     int64 `json:"num_flows_completed"`
	PacketsIn             int64 `json:"packets_in"`
	PacketsOut            int64 `json:"packets_out"`
	HTTPRequestsAllowedIn int64 `json:"http_requests_allowed_in"`
	HTTPRequestsDeniedIn  int64 `json:"http_requests_denied_in"`

	Host string `json:"host"`
}

type FlowLogLabelsJSONOutput struct {
	Labels []string `json:"labels"`
}

type FlowLogPoliciesJSONOutput struct {
	AllPolicies []string `json:"all_policies"`
}

func (l *FlowLog) MarshalJSON() ([]byte, error) {
	j := toJSONOutput(l)
	return json.Marshal(j)
}

func toJSONOutput(l *FlowLog) flowLogJSONOutput {
	var out flowLogJSONOutput

	out.StartTime = l.StartTime.Unix()
	out.EndTime = l.EndTime.Unix()
	out.Host = l.Host

	ip := net.IP(l.Tuple.Src[:16])
	if !ip.IsUnspecified() {
		s := ip.String()
		out.SourceIP = &s
	}
	if l.Tuple.Proto == 1 || l.Tuple.L4Src == UnsetIntField {
		out.SourcePort = nil
	} else {
		t := int64(l.Tuple.L4Src)
		out.SourcePort = &t
	}
	out.SourceName = l.SrcMeta.Name
	out.SourceNameAggr = l.SrcMeta.AggregatedName
	out.SourceNamespace = l.SrcMeta.Namespace
	out.SourceType = string(l.SrcMeta.Type)
	if l.SrcLabels == nil {
		out.SourceLabels = nil
	} else {
		out.SourceLabels = &FlowLogLabelsJSONOutput{
			Labels: flattenLabels(l.SrcLabels),
		}
	}

	ip = net.IP(l.Tuple.Dst[:16])
	if !ip.IsUnspecified() {
		s := ip.String()
		out.DestIP = &s
	}
	if l.Tuple.Proto == 1 || l.Tuple.L4Dst == UnsetIntField {
		out.DestPort = nil
	} else {
		t := int64(l.Tuple.L4Dst)
		out.DestPort = &t
	}
	out.DestName = l.DstMeta.Name
	out.DestNameAggr = l.DstMeta.AggregatedName
	out.DestNamespace = l.DstMeta.Namespace
	out.DestType = string(l.DstMeta.Type)
	if l.DstLabels == nil {
		out.DestLabels = nil
	} else {
		out.DestLabels = &FlowLogLabelsJSONOutput{
			Labels: flattenLabels(l.DstLabels),
		}
	}

	out.Proto = protoToString(l.Tuple.Proto)

	out.Action = string(l.Action)
	out.Reporter = string(l.Reporter)

	if l.FlowPolicies == nil {
		out.Policies = nil
	} else {
		all_p := make([]string, 0, len(l.FlowPolicies))
		for pol := range l.FlowPolicies {
			all_p = append(all_p, pol)
		}
		out.Policies = &FlowLogPoliciesJSONOutput{
			AllPolicies: all_p,
		}
	}

	out.BytesIn = int64(l.BytesIn)
	out.BytesOut = int64(l.BytesOut)
	out.PacketsIn = int64(l.PacketsIn)
	out.PacketsOut = int64(l.PacketsOut)
	out.NumFlows = int64(l.NumFlows)
	out.NumFlowsCompleted = int64(l.NumFlowsCompleted)
	out.NumFlowsStarted = int64(l.NumFlowsStarted)
	out.HTTPRequestsAllowedIn = int64(l.HTTPRequestsAllowedIn)
	out.HTTPRequestsDeniedIn = int64(l.HTTPRequestsDeniedIn)
	return out
}

func protoToString(p int) string {
	s, ok := protoNames[p]
	if ok {
		return s
	}
	return strconv.Itoa(p)
}

func stringToProto(s string) int {
	for i, st := range protoNames {
		if s == st {
			return i
		}
	}
	p, _ := strconv.Atoi(s)
	return p
}

func (l *FlowLog) UnmarshalJSON(b []byte) error {
	o := flowLogJSONOutput{}
	err := json.Unmarshal(b, &o)
	if err != nil {
		return err
	}
	return o.toFlowLog(l)
}

func (o flowLogJSONOutput) toFlowLog(fl *FlowLog) error {
	fl.StartTime = time.Unix(o.StartTime, 0)
	fl.EndTime = time.Unix(o.EndTime, 0)

	var sip, dip [16]byte
	if o.SourceIP != nil && *o.SourceIP != "" {
		sip = ipStrTo16Byte(*o.SourceIP)
	}
	if o.DestIP != nil && *o.DestIP != "" {
		dip = ipStrTo16Byte(*o.DestIP)
	}
	p := stringToProto(o.Proto)
	var sPort, dPort int
	if o.SourcePort != nil {
		sPort = int(*o.SourcePort)
	}
	if o.DestPort != nil {
		dPort = int(*o.DestPort)
	}
	fl.Tuple = Tuple{sip, dip, p, sPort, dPort}

	var srcType, dstType FlowLogEndpointType
	switch o.SourceType {
	case "wep":
		srcType = FlowLogEndpointTypeWep
	case "hep":
		srcType = FlowLogEndpointTypeHep
	case "ns":
		srcType = FlowLogEndpointTypeNs
	case "net":
		srcType = FlowLogEndpointTypeNet
	}

	fl.SrcMeta = EndpointMetadata{
		Type:           srcType,
		Namespace:      o.SourceNamespace,
		Name:           o.SourceName,
		AggregatedName: o.SourceNameAggr,
	}
	if o.SourceLabels == nil {
		fl.SrcLabels = nil
	} else {
		fl.SrcLabels = unflattenLabels(o.SourceLabels.Labels)
	}

	switch o.DestType {
	case "wep":
		dstType = FlowLogEndpointTypeWep
	case "hep":
		dstType = FlowLogEndpointTypeHep
	case "ns":
		dstType = FlowLogEndpointTypeNs
	case "net":
		dstType = FlowLogEndpointTypeNet
	}

	fl.DstMeta = EndpointMetadata{
		Type:           dstType,
		Namespace:      o.DestNamespace,
		Name:           o.DestName,
		AggregatedName: o.DestNameAggr,
	}
	if o.DestLabels == nil {
		fl.DstLabels = nil
	} else {
		fl.DstLabels = unflattenLabels(o.DestLabels.Labels)
	}

	fl.Action = FlowLogAction(o.Action)
	fl.Reporter = FlowLogReporter(o.Reporter)
	fl.BytesIn = int(o.BytesIn)
	fl.BytesOut = int(o.BytesOut)
	fl.PacketsIn = int(o.PacketsIn)
	fl.PacketsOut = int(o.PacketsOut)
	fl.NumFlows = int(o.NumFlows)
	fl.NumFlowsStarted = int(o.NumFlowsStarted)
	fl.NumFlowsCompleted = int(o.NumFlowsCompleted)

	if o.Policies == nil {
		fl.FlowPolicies = nil
	} else {
		fl.FlowPolicies = make(FlowPolicies)
		for _, pol := range o.Policies.AllPolicies {
			fl.FlowPolicies[pol] = emptyValue
		}
	}

	return nil
}
