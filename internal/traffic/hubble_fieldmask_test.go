package traffic

import (
	"fmt"
	"reflect"
	"testing"

	flowpb "github.com/cilium/cilium/api/v1/flow"
	"github.com/cilium/cilium/pkg/hubble/parser/fieldmask"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

// fillFlow sets every field of a message, recursively. variant picks which
// member of each oneof is set, which enum value and which bool value is used,
// so a handful of variants covers each branch the conversion takes.
func fillFlow(m protoreflect.Message, variant, depth int) {
	if depth > 6 {
		return
	}
	fields := m.Descriptor().Fields()
	for i := range fields.Len() {
		fd := fields.Get(i)
		if oo := fd.ContainingOneof(); oo != nil && !oo.IsSynthetic() {
			if oo.Fields().Get(variant%oo.Fields().Len()) != fd {
				continue
			}
		}
		switch {
		case fd.IsList():
			list := m.Mutable(fd).List()
			for j := range 2 {
				list.Append(fillValue(list.NewElement(), fd, variant, j, depth))
			}
		case fd.IsMap():
			// Flow carries no maps the conversion reads.
		default:
			m.Set(fd, fillValue(m.NewField(fd), fd, variant, 0, depth))
		}
	}
}

func fillValue(v protoreflect.Value, fd protoreflect.FieldDescriptor, variant, index, depth int) protoreflect.Value {
	switch fd.Kind() {
	case protoreflect.MessageKind, protoreflect.GroupKind:
		fillFlow(v.Message(), variant, depth+1)
		return v
	case protoreflect.StringKind:
		return protoreflect.ValueOfString(fmt.Sprintf("%s-%d", fd.Name(), index))
	case protoreflect.BytesKind:
		return protoreflect.ValueOfBytes([]byte("b"))
	case protoreflect.BoolKind:
		return protoreflect.ValueOfBool(variant%2 == 0)
	case protoreflect.EnumKind:
		values := fd.Enum().Values()
		return protoreflect.ValueOfEnum(values.Get((1 + variant) % values.Len()).Number())
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		return protoreflect.ValueOfInt32(int32(7 + index))
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return protoreflect.ValueOfInt64(int64(7 + index))
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		return protoreflect.ValueOfUint32(uint32(7 + index))
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return protoreflect.ValueOfUint64(uint64(7 + index))
	case protoreflect.FloatKind:
		return protoreflect.ValueOfFloat32(1.5)
	case protoreflect.DoubleKind:
		return protoreflect.ValueOfFloat64(1.5)
	}
	return v
}

// The mask Radar sends must hold everything the conversion reads: a field
// left out arrives empty from a Cilium 1.19+ Relay, and the flow converts
// differently with no error anywhere. Each flow here is converted whole and
// again after Cilium's own mask code has pruned it the way an agent would,
// and the two must agree.
func TestHubbleFlowFieldsCoverConversion(t *testing.T) {
	mask, err := fieldmask.New(&fieldmaskpb.FieldMask{Paths: hubbleFlowFields})
	if err != nil {
		t.Fatalf("hubbleFlowFields is not a valid Flow field mask: %v", err)
	}

	var flows []*flowpb.Flow
	for variant := range 8 {
		full := &flowpb.Flow{}
		fillFlow(full.ProtoReflect(), variant, 0)
		flows = append(flows, full)

		// The paths the conversion takes on what is missing: an endpoint
		// identified by its labels alone, and a flow with no L7 record.
		bare := proto.Clone(full).(*flowpb.Flow)
		bare.Source.PodName = ""
		bare.Source.Labels = []string{"reserved:world", "cidr:10.0.0.0/8"}
		bare.Destination.PodName = ""
		bare.Destination.Labels = []string{"reserved:remote-node"}
		bare.L7 = nil
		flows = append(flows, bare)

		noEndpoints := proto.Clone(full).(*flowpb.Flow)
		noEndpoints.Source, noEndpoints.Destination, noEndpoints.L4 = nil, nil, nil
		flows = append(flows, noEndpoints)
	}

	for i, full := range flows {
		masked := &flowpb.Flow{}
		mask.Copy(masked.ProtoReflect(), full.ProtoReflect())

		want, wantOK := callerOrientedFlow(full)
		got, gotOK := callerOrientedFlow(masked)
		if wantOK != gotOK || !reflect.DeepEqual(want, got) {
			t.Errorf("flow %d converts differently once masked; add the field it reads to hubbleFlowFields\nwhole:  %+v (%v)\nmasked: %+v (%v)", i, want, wantOK, got, gotOK)
		}
		if !masked.GetTime().AsTime().Equal(full.GetTime().AsTime()) || masked.GetNodeName() != full.GetNodeName() {
			t.Errorf("flow %d: the fetch's time or node name did not survive the mask", i)
		}
	}
}

func TestHubbleFlowsRequestSendsTheFieldMask(t *testing.T) {
	for _, follow := range []bool{false, true} {
		if got := hubbleFlowsRequest(FlowOptions{}, follow).GetFieldMask().GetPaths(); !reflect.DeepEqual(got, hubbleFlowFields) {
			t.Errorf("follow=%v: field mask = %v, want hubbleFlowFields", follow, got)
		}
	}
}
