package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// Dedicated cores are for one copy, for now: a new Postgres with 3 instances
// and dedicated cores is refused at plan time (the platform answers 422
// dedicated_one_copy at apply). One instance, shared cores, either unknown,
// Redis (one instance, refused on its own) and object storage (no dedicated
// cores, refused on its own) are left to their own checks.
func TestDedicatedOneCopy(t *testing.T) {
	m := func(engine string, instances types.Int64, dedicated types.Bool) storageModel {
		s := baseStorage(engine)
		s.Instances, s.DedicatedCores = instances, dedicated
		return s
	}
	one, three := types.Int64Value(1), types.Int64Value(3)
	on, off := types.BoolValue(true), types.BoolValue(false)
	for _, c := range []struct {
		name    string
		m       storageModel
		refused bool
	}{
		{"postgres, 3 instances, dedicated", m("postgres", three, on), true},
		{"postgres, 1 instance, dedicated", m("postgres", one, on), false},
		{"postgres, instances left out, dedicated", m("postgres", types.Int64Null(), on), false},
		{"postgres, 3 instances, shared", m("postgres", three, off), false},
		{"postgres, 3 instances, cores left out", m("postgres", three, types.BoolNull()), false},
		{"postgres, instances not known yet", m("postgres", types.Int64Unknown(), on), false},
		{"postgres, cores not known yet", m("postgres", three, types.BoolUnknown()), false},
		{"redis, 3 instances, dedicated", m("redis", three, on), false},
		{"object storage, 3 servers, dedicated", m("s3", three, on), false},
	} {
		got := dedicatedOneCopy(c.m, nil)
		if (got != nil) != c.refused {
			t.Errorf("%s: %v, want refused %v", c.name, got, c.refused)
		}
		if got != nil && (got[0] != "Dedicated cores work with one copy for now" ||
			!strings.Contains(got[1], "Set instances = 1, or dedicated_cores = false (shared cores).")) {
			t.Errorf("%s: %q", c.name, got)
		}
	}
	// The configuration alone can't tell (the database may already run so,
	// which the platform keeps): ModifyPlan judges the pair with the state.
	// Redis and object storage keep their own single refusal.
	if got := storageConfigErrors(m("postgres", three, on)); len(got) != 0 {
		t.Errorf("postgres, 3 instances, dedicated: %v, want none from the configuration alone", got)
	}
	if got := storageConfigErrors(m("redis", three, on)); len(got) != 1 || got[0][0] != "Redis runs as one instance" {
		t.Errorf("redis, 3 instances, dedicated: %v", got)
	}
	if got := storageConfigErrors(m("s3", one, on)); len(got) != 1 || got[0][0] != "Object storage has no dedicated cores" {
		t.Errorf("object storage, dedicated: %v", got)
	}
}

// ModifyPlan refuses dedicated cores asked anew for a Postgres of 3
// instances, on dedicated_cores, as the platform judges them: a create (or a
// replacement), shared cores before, more instances, more whole cores, another
// placement. A database the platform already runs dedicated with 3 instances
// (an older platform made it so) plans as it is, and with less or a change
// that isn't its cores: the platform keeps it running so. One instance and
// shared cores pass.
func TestStorageModifyPlanDedicatedOneCopy(t *testing.T) {
	ctx := context.Background()
	var sr resource.SchemaResponse
	(&storageResource{}).Schema(ctx, resource.SchemaRequest{}, &sr)
	typ := sr.Schema.Type().TerraformType(ctx).(tftypes.Object)
	// value is a database of 3 instances unless set: instances, dedicated
	// cores, then attribute/value pairs.
	value := func(instances, dedicated any, more ...any) tftypes.Value {
		vals := map[string]tftypes.Value{}
		for name, at := range typ.AttributeTypes {
			vals[name] = tftypes.NewValue(at, nil)
		}
		vals["name"] = tftypes.NewValue(tftypes.String, "db")
		vals["engine"] = tftypes.NewValue(tftypes.String, "postgres")
		vals["password_wo_version"] = tftypes.NewValue(tftypes.Number, 1)
		vals["instances"] = tftypes.NewValue(tftypes.Number, instances)
		vals["dedicated_cores"] = tftypes.NewValue(tftypes.Bool, dedicated)
		for i := 0; i+1 < len(more); i += 2 {
			name := more[i].(string)
			vals[name] = tftypes.NewValue(typ.AttributeTypes[name], more[i+1])
		}
		return tftypes.NewValue(typ, vals)
	}
	state := func(instances, dedicated any, more ...any) tfsdk.State {
		return tfsdk.State{Schema: sr.Schema, Raw: value(instances, dedicated, more...)}
	}
	noState := tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(typ, nil)}
	// What an older platform left: 3 instances on dedicated cores, 1 core
	// each, 5 GiB, placed automatically.
	running := []any{"cpu", "1", "disk_gi", 5}
	runningAnd := func(more ...any) []any { return append(append([]any{}, running...), more...) }
	for _, c := range []struct {
		name         string
		config, plan tftypes.Value
		state        tfsdk.State
		refused      bool
	}{
		{"dedicated written, 3 instances kept", value(nil, true), value(3, true), state(3, false), true},
		{"3 instances written, dedicated kept", value(3, nil), value(3, true), state(1, true), true},
		{"a create of both", value(3, true), value(3, true), noState, true},
		{"one instance, dedicated", value(1, true), value(1, true), state(1, false), false},
		{"3 instances, shared", value(3, false), value(3, false), state(3, true), false},
		// Already dedicated with 3 instances: kept as it is.
		{"already so, nothing changes (0.18 configuration)", value(3, nil),
			value(3, true, running...), state(3, true, running...), false},
		{"already so, both written", value(3, true),
			value(3, true, running...), state(3, true, running...), false},
		{"already so, only the disk grows", value(3, nil, "disk_gi", 10),
			value(3, true, runningAnd("disk_gi", 10)...), state(3, true, running...), false},
		{"already so, fewer cores", value(3, nil, "cpu", "500m"),
			value(3, true, runningAnd("cpu", "500m")...), state(3, true, running...), false},
		{"already so, cores within the same whole core", value(3, nil, "cpu", "900m"),
			value(3, true, "cpu", "900m"), state(3, true, "cpu", "500m"), false},
		{"already so, state read before dedicated_cores", value(3, true),
			value(3, true, running...), state(3, nil, running...), false},
		{"already so, cpu not known yet", value(3, nil),
			value(3, true, "cpu", tftypes.UnknownValue), state(3, true, running...), false},
		// Already dedicated with 3 instances: its cores asked anew.
		{"already so, more cores", value(3, nil, "cpu", "2"),
			value(3, true, runningAnd("cpu", "2")...), state(3, true, running...), true},
		{"already so, a part core more rounds up", value(3, nil, "cpu", "1500m"),
			value(3, true, runningAnd("cpu", "1500m")...), state(3, true, running...), true},
		{"already so, pinned to a host", value(3, nil, "placement_strategy", "host", "placement_host", "h1"),
			value(3, true, runningAnd("placement_strategy", "host", "placement_host", "h1")...),
			state(3, true, running...), true},
		{"already so, another name (replaced)", value(3, nil, "name", "db2"),
			value(3, true, runningAnd("name", "db2")...), state(3, true, running...), true},
		{"already so, placement auto written", value(3, nil, "placement_strategy", "auto"),
			value(3, true, runningAnd("placement_strategy", "auto")...), state(3, true, running...), false},
	} {
		req := resource.ModifyPlanRequest{
			Config: tfsdk.Config{Schema: sr.Schema, Raw: c.config},
			Plan:   tfsdk.Plan{Schema: sr.Schema, Raw: c.plan},
			State:  c.state,
		}
		resp := &resource.ModifyPlanResponse{Plan: req.Plan}
		(&storageResource{}).ModifyPlan(ctx, req, resp)
		errs := resp.Diagnostics.Errors()
		if !c.refused {
			if len(errs) != 0 {
				t.Errorf("%s: %v", c.name, resp.Diagnostics)
			}
			continue
		}
		if len(errs) != 1 || errs[0].Summary() != "Dedicated cores work with one copy for now" {
			t.Errorf("%s: errors %v, want the one-copy refusal", c.name, resp.Diagnostics)
			continue
		}
		if wp, ok := errs[0].(diag.DiagnosticWithPath); !ok || !wp.Path().Equal(path.Root("dedicated_cores")) {
			t.Errorf("%s: error not on dedicated_cores: %v", c.name, errs[0])
		}
	}
}

// wholeCores counts cpu as the platform counts dedicated cores, rounded up to
// whole cores; what it can't read is left to the platform.
func TestWholeCores(t *testing.T) {
	for _, c := range []struct {
		in   types.String
		want int64
		ok   bool
	}{
		{types.StringValue("1"), 1, true},
		{types.StringValue("2"), 2, true},
		{types.StringValue(" 2 "), 2, true},
		{types.StringValue("500m"), 1, true},
		{types.StringValue("1000m"), 1, true},
		{types.StringValue("1001m"), 2, true},
		{types.StringValue("1.5"), 2, true},
		{types.StringValue("0.25"), 1, true},
		{types.StringValue("abc"), 0, false},
		{types.StringValue("-1"), 0, false},
		{types.StringValue("2Gi"), 0, false},
		{types.StringNull(), 0, false},
		{types.StringUnknown(), 0, false},
	} {
		got, ok := wholeCores(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("wholeCores(%v) = %d, %v; want %d, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}
