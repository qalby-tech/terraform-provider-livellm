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

// Dedicated cores are for one copy, for now: Postgres with 3 instances and
// dedicated cores is refused at plan time (the platform answers 422
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
		got := dedicatedOneCopy(c.m)
		if (got != nil) != c.refused {
			t.Errorf("%s: %v, want refused %v", c.name, got, c.refused)
		}
		if got != nil && (got[0] != "Dedicated cores work with one copy for now" ||
			!strings.Contains(got[1], "Set instances = 1, or dedicated_cores = false (shared cores).")) {
			t.Errorf("%s: %q", c.name, got)
		}
	}
	// The configuration's pair, at plan time, once: Redis and object storage
	// keep their own single refusal.
	if got := storageConfigErrors(m("postgres", three, on)); len(got) != 1 || got[0][0] != "Dedicated cores work with one copy for now" {
		t.Errorf("postgres, 3 instances, dedicated: %v", got)
	}
	if got := storageConfigErrors(m("redis", three, on)); len(got) != 1 || got[0][0] != "Redis runs as one instance" {
		t.Errorf("redis, 3 instances, dedicated: %v", got)
	}
	if got := storageConfigErrors(m("s3", one, on)); len(got) != 1 || got[0][0] != "Object storage has no dedicated cores" {
		t.Errorf("object storage, dedicated: %v", got)
	}
}

// ModifyPlan refuses the pair a plan keeps from what the database has (the
// configuration writes one of the two, the other is kept), on dedicated_cores,
// and on a create too; one instance and shared cores pass.
func TestStorageModifyPlanDedicatedOneCopy(t *testing.T) {
	ctx := context.Background()
	var sr resource.SchemaResponse
	(&storageResource{}).Schema(ctx, resource.SchemaRequest{}, &sr)
	typ := sr.Schema.Type().TerraformType(ctx).(tftypes.Object)
	value := func(instances, dedicated any) tftypes.Value {
		vals := map[string]tftypes.Value{}
		for name, at := range typ.AttributeTypes {
			vals[name] = tftypes.NewValue(at, nil)
		}
		vals["name"] = tftypes.NewValue(tftypes.String, "db")
		vals["engine"] = tftypes.NewValue(tftypes.String, "postgres")
		vals["password_wo_version"] = tftypes.NewValue(tftypes.Number, 1)
		vals["instances"] = tftypes.NewValue(tftypes.Number, instances)
		vals["dedicated_cores"] = tftypes.NewValue(tftypes.Bool, dedicated)
		return tftypes.NewValue(typ, vals)
	}
	state := func(instances, dedicated any) tfsdk.State {
		return tfsdk.State{Schema: sr.Schema, Raw: value(instances, dedicated)}
	}
	noState := tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(typ, nil)}
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
