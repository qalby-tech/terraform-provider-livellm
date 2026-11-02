package provider

import (
	"context"
	"fmt"
	"net/netip"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// A machine's private address: a fixed address of its own on a second
// network card, in a network only the workspace's machines share. Both
// private_network and private_address are asked for when the machine is made
// (left out, the platform decides: an address wherever it offers one, the
// next free one) and fixed for the machine's life: they are sent on the
// create alone, a later save leaves them out (the platform keeps what the
// machine has), and another value on an existing machine is refused at plan
// time, never a replacement, which would delete the disk. private_mac is the
// platform's, read only.

// keepOrPlatform plans an attribute the configuration leaves out as what the
// resource already has, null included, and on a create as unknown: the
// platform decides it then. (UseStateForUnknown keeps nothing when state
// holds null, so a machine without a private address would plan "known after
// apply" on every run.)
type keepOrPlatform struct{}

func (keepOrPlatform) Description(context.Context) string {
	return "Keeps the resource's current value when the configuration leaves it out; the platform's on a new resource."
}

func (m keepOrPlatform) MarkdownDescription(ctx context.Context) string { return m.Description(ctx) }

func (keepOrPlatform) PlanModifyBool(_ context.Context, req planmodifier.BoolRequest, resp *planmodifier.BoolResponse) {
	if req.ConfigValue.IsNull() && !req.State.Raw.IsNull() {
		resp.PlanValue = req.StateValue
	}
}

func (keepOrPlatform) PlanModifyString(_ context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	if req.ConfigValue.IsNull() && !req.State.Raw.IsNull() {
		resp.PlanValue = req.StateValue
	}
}

// withPrivateAddress adds private_network, private_address and private_mac
// to a machine's attributes.
func withPrivateAddress(attrs map[string]schema.Attribute) {
	attrs["private_network"] = schema.BoolAttribute{
		Optional: true,
		Computed: true,
		Description: "Whether the machine has a private address: a fixed address of its own on a second network card " +
			"(enp2s0 on Linux), in a network only the workspace's machines share. Set when the machine is made: left " +
			"out, it gets one wherever the platform offers private addresses; false makes it with one card. After " +
			"that it is fixed: another value on an existing machine is refused at plan time.",
		PlanModifiers: []planmodifier.Bool{keepOrPlatform{}},
	}
	attrs["private_address"] = schema.StringAttribute{
		Optional: true,
		Computed: true,
		Description: "The machine's private address, such as \"172.16.0.12\". Asked for when the machine is made " +
			"(left out: the next free one in the workspace's range, the livellm_workspace data source's " +
			"private_network), then fixed for the machine's life: another value on an existing machine is refused " +
			"at plan time. Null on a machine without one.",
		PlanModifiers: []planmodifier.String{keepOrPlatform{}},
	}
	attrs["private_mac"] = schema.StringAttribute{
		Computed:      true,
		Description:   "The MAC of the machine's private card, for Windows and setups by hand. Null on a machine without a private address.",
		PlanModifiers: []planmodifier.String{keepOrPlatform{}},
	}
}

// privateConfigErrors are the platform's rules for a private address, at
// plan time: {attribute, summary, detail}. Whether it is inside the range,
// free or offered at all is the platform's to say at apply.
func privateConfigErrors(m vmResourceModel) [][3]string {
	if !known(m.PrivateAddress) {
		return nil
	}
	addr := m.PrivateAddress.ValueString()
	if a, err := netip.ParseAddr(addr); err != nil || !a.Is4() || a.String() != addr {
		return [][3]string{{"private_address", "Not a private address",
			fmt.Sprintf("%q isn't one IPv4 address such as 172.16.0.12. Leave private_address out for the next free one.", addr)}}
	}
	if known(m.PrivateNetwork) && !m.PrivateNetwork.ValueBool() {
		return [][3]string{{"private_address", "A private address needs private_network",
			"private_address is for a machine with a private address: leave private_network out or set it to true, " +
				"or leave private_address out."}}
	}
	return nil
}

// privateChange refuses another private_network or private_address on an
// existing machine: {attribute, summary, detail}, nil when the plan keeps
// them or doesn't know them yet. A replacement is never asked here.
func privateChange(plan, state vmResourceModel) *[3]string {
	name := plan.Name.ValueString()
	if known(plan.PrivateNetwork) && known(state.PrivateNetwork) && !plan.PrivateNetwork.Equal(state.PrivateNetwork) {
		return &[3]string{"private_network", "A machine's private network is set when it is made",
			fmt.Sprintf("%s was made %s a private address, and that is fixed for its life: keep private_network = %t "+
				"or leave it out. To make the machine anew, taint it first (tofu taint or terraform taint) or rename "+
				"it: -replace alone is refused while private_network differs.",
				name, map[bool]string{true: "with", false: "without"}[state.PrivateNetwork.ValueBool()], state.PrivateNetwork.ValueBool())}
	}
	if plan.PrivateAddress.IsUnknown() || state.PrivateAddress.IsUnknown() || plan.PrivateAddress.Equal(state.PrivateAddress) {
		return nil
	}
	keep := "leave private_address out"
	if !state.PrivateAddress.IsNull() {
		keep = fmt.Sprintf("keep private_address = %q or leave it out", state.PrivateAddress.ValueString())
	}
	return &[3]string{"private_address", "A machine's private address is set when it is made",
		fmt.Sprintf("%s keeps the private address it was made with for its life: %s. To make the machine anew with "+
			"another, taint it first (tofu taint or terraform taint) or rename it: -replace alone is refused while "+
			"private_address differs.", name, keep)}
}

// vmPrivateSpec adds what the create asks for: private_network and
// private_address when the configuration sets them. Left out, nothing is
// sent and the platform decides. Never on a later save: the machine keeps
// what it was made with.
func vmPrivateSpec(m vmResourceModel, spec map[string]any) {
	if known(m.PrivateNetwork) {
		spec["privateNetwork"] = m.PrivateNetwork.ValueBool()
	}
	if known(m.PrivateAddress) && m.PrivateAddress.ValueString() != "" {
		spec["privateAddress"] = m.PrivateAddress.ValueString()
	}
}

// vmPrivate is a machine's private address as the platform answers it.
type vmPrivate struct {
	network      types.Bool
	address, mac types.String
	said         bool // the platform names privateNetwork
}

// readVMPrivate is the machine's private address from the platform's copy of
// its settings. A platform that offers private addresses always says whether
// the machine has one (said); one that doesn't, or doesn't know them, says
// nothing, and then the machine has none.
func readVMPrivate(sp map[string]any) vmPrivate {
	p := vmPrivate{network: types.BoolValue(false), address: types.StringNull(), mac: types.StringNull()}
	if sp == nil {
		return p
	}
	_, p.said = sp["privateNetwork"]
	if a, _ := sp["privateAddress"].(string); a != "" {
		p.address = types.StringValue(a)
	}
	if m, _ := sp["privateMac"].(string); m != "" {
		p.mac = types.StringValue(m)
	}
	p.network = types.BoolValue(sp["privateNetwork"] == true || !p.address.IsNull())
	return p
}

// refreshVMPrivate fills a refresh from the platform's answer. When the
// platform says nothing (an older one, or one that offers no private
// addresses), state keeps what it holds: reading that as "none" would turn a
// configured private_network = true into a change that is refused.
func refreshVMPrivate(m *vmResourceModel, sp map[string]any) {
	p := readVMPrivate(sp)
	if !p.said {
		return
	}
	m.PrivateNetwork, m.PrivateAddress, m.PrivateMac = p.network, p.address, p.mac
}

// settleVMPrivate resolves what a write left unknown from the platform's copy
// (nil when it could not be read: none), and warns when the platform holds
// other than the create asked: a platform without private addresses ignores
// them, and the machine is made with one card.
func settleVMPrivate(m *vmResourceModel, sp map[string]any, diags *diag.Diagnostics) {
	p := readVMPrivate(sp)
	if m.PrivateNetwork.IsUnknown() {
		m.PrivateNetwork = p.network
	} else if sp != nil && known(m.PrivateNetwork) && !m.PrivateNetwork.Equal(p.network) {
		diags.AddWarning("Private network not kept as written",
			fmt.Sprintf("%q has %s private address. A platform without private addresses doesn't keep private_network, "+
				"and the machine has one network card.", m.Name.ValueString(), map[bool]string{true: "a", false: "no"}[p.network.ValueBool()]))
	}
	if m.PrivateAddress.IsUnknown() {
		m.PrivateAddress = p.address
	} else if sp != nil && known(m.PrivateAddress) && !m.PrivateAddress.Equal(p.address) {
		diags.AddWarning("Private address not kept as written",
			fmt.Sprintf("%q doesn't hold private address %q. A platform without private addresses doesn't keep it.",
				m.Name.ValueString(), m.PrivateAddress.ValueString()))
	}
	if m.PrivateMac.IsUnknown() {
		m.PrivateMac = p.mac
	}
}

// privateRefusals are the platform's refusals of a private address, by its
// code (409 or 422): what the diagnostic is called and what to do next. The
// platform's own words come first in the detail.
var privateRefusals = map[string]struct{ summary, next string }{
	"private_network_not_offered": {"Private addresses aren't offered here",
		"The platform gives this kind of machine no private address here. Leave private_network and private_address " +
			"out (or set private_network = false), then apply again."},
	"private_address_invalid": {"Not a private address",
		"private_address is one IPv4 address such as 172.16.0.12, for a machine with private_network left out or " +
			"true. Or leave private_address out for the next free one."},
	"private_address_outside": {"Private address outside the workspace's range",
		"Pick an address inside the workspace's private network (the livellm_workspace data source's " +
			"private_network.range), or leave private_address out for the next free one."},
	"private_address_reserved": {"Private address reserved",
		"The range's first ten addresses and its last are the network's own. Pick another, or leave private_address " +
			"out for the next free one."},
	"private_address_taken": {"Private address in use",
		"Another machine of the workspace has this address. Pick another, or leave private_address out for the next " +
			"free one."},
	"private_address_releasing": {"Private address still being freed",
		"A deleted machine still holds this address while it is freed. Apply again in a minute, or pick another."},
	"private_address_fixed": {"A machine's private address is set when it is made",
		"private_network and private_address are fixed for a machine's life. Keep the values it has, or leave them out."},
	"private_range_full": {"No private address left",
		"Every address of the workspace's private network is taken. Delete a machine that has one, or set " +
			"private_network = false for this machine."},
}
