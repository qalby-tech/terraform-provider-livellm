package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestReadDefaulted(t *testing.T) {
	null := types.StringNull()
	cases := []struct {
		name string
		prev types.String
		api  string
		want types.String
	}{
		{"unset stays unset when the api echoes the default", null, "Dockerfile", null},
		{"unset stays unset when the api reports nothing", null, "", null},
		{"unset picks up a real change made elsewhere", null, "build/Dockerfile", types.StringValue("build/Dockerfile")},
		{"explicit default survives an api that strips it", types.StringValue("Dockerfile"), "", types.StringValue("Dockerfile")},
		{"explicit value cleared elsewhere shows as drift", types.StringValue("x"), "", null},
		{"explicit value round-trips", types.StringValue("x"), "x", types.StringValue("x")},
	}
	for _, c := range cases {
		if got := readDefaulted(c.prev, c.api, "Dockerfile"); !got.Equal(c.want) {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestReadEnvMap(t *testing.T) {
	prev := types.MapValueMust(types.StringType, map[string]attr.Value{"A": types.StringValue("secret-a")})
	got := readEnvMap(prev, []any{
		map[string]any{"name": "A"},
		map[string]any{"name": "B"},
	}, false)
	el := got.Elements()
	if el["A"].(types.String).ValueString() != "secret-a" {
		t.Errorf("stored value not kept: %v", el["A"])
	}
	if el["B"].(types.String).ValueString() != "" {
		t.Errorf("unknown name should read as empty: %v", el["B"])
	}
	if !readEnvMap(types.MapNull(types.StringType), nil, false).IsNull() {
		t.Error("null stays null when the api reports nothing")
	}
	if !readEnvMap(prev, nil, false).IsNull() {
		t.Error("a populated map must show drift when the api reports nothing")
	}
	plain := readEnvMap(types.MapNull(types.StringType), []any{map[string]any{"name": "X", "value": "1"}}, true)
	if plain.Elements()["X"].(types.String).ValueString() != "1" {
		t.Error("plain env values come from the api")
	}
}

func TestContainerAppSpec(t *testing.T) {
	ctx := context.Background()
	m := containerAppModel{
		Name: types.StringValue("api"),
		Source: &sourceModel{
			Token: types.StringValue("tok"),
			Git: &gitSourceModel{
				URL:        types.StringValue("https://github.com/acme/api.git"),
				Ref:        types.StringValue("main"),
				Dockerfile: types.StringNull(),
				Context:    types.StringValue("services/api"),
			},
		},
		SecretEnv: types.MapValueMust(types.StringType, map[string]attr.Value{
			"B": types.StringValue("2"), "A": types.StringValue("1"),
		}),
	}
	spec := containerAppSpec(ctx, m, true)
	src := spec["source"].(map[string]any)
	git := src["git"].(map[string]any)
	if git["url"] != "https://github.com/acme/api.git" || git["ref"] != "main" || git["context"] != "services/api" {
		t.Errorf("git block: %v", git)
	}
	if _, ok := git["dockerfile"]; ok {
		t.Error("unset dockerfile must be omitted")
	}
	if src["gitAuth"].(map[string]any)["token"] != "tok" {
		t.Errorf("token not sent: %v", src)
	}
	if _, ok := containerAppSpec(ctx, m, false)["source"].(map[string]any)["gitAuth"]; ok {
		t.Error("token must be omitted when unchanged")
	}
	se := spec["secretEnv"].([]map[string]any)
	if len(se) != 2 || se[0]["name"] != "A" || se[0]["value"] != "1" || se[1]["name"] != "B" {
		t.Errorf("secretEnv: %v", se)
	}
	if _, ok := spec["image"]; ok {
		t.Error("no image for a source app")
	}
}

func TestContainerAppImageAuth(t *testing.T) {
	ctx := context.Background()
	m := containerAppModel{
		Name:  types.StringValue("grafana"),
		Image: types.StringValue("ghcr.io/acme/grafana:11"),
		ImageAuth: &imageAuthModel{
			Username: types.StringValue("acme-bot"),
			Password: types.StringValue("ghp_x"),
		},
	}
	spec := containerAppSpec(ctx, m, true)
	auth, ok := spec["imageAuth"].(map[string]any)
	if !ok || auth["username"] != "acme-bot" || auth["password"] != "ghp_x" {
		t.Fatalf("imageAuth: %v", spec["imageAuth"])
	}
	if s, _ := appShapeError(m); s != "" {
		t.Errorf("image + image_auth must be valid, got %q", s)
	}

	withSource := m
	withSource.Image = types.StringNull()
	withSource.Source = &sourceModel{Git: &gitSourceModel{URL: types.StringValue("https://github.com/acme/api.git")}}
	if s, _ := appShapeError(withSource); s != "Conflicting image_auth and source" {
		t.Errorf("source + image_auth must be rejected, got %q", s)
	}
	if _, ok := containerAppSpec(ctx, withSource, true)["imageAuth"]; ok {
		t.Error("imageAuth must never be sent for a source app")
	}

	if _, ok := containerAppSpec(ctx, containerAppModel{Image: types.StringValue("nginx")}, true)["imageAuth"]; ok {
		t.Error("no image_auth block, no imageAuth")
	}

	prev := &imageAuthModel{Username: types.StringValue("old"), Password: types.StringValue("kept")}
	got := readImageAuth(prev, map[string]any{"username": "acme-bot"})
	if got == nil || got.Username.ValueString() != "acme-bot" || got.Password.ValueString() != "kept" {
		t.Errorf("read keeps the password from state and takes the username from the api: %+v", got)
	}
	if readImageAuth(prev, nil) != nil {
		t.Error("api without imageAuth reads as no block (drift when configured)")
	}
	if got := readImageAuth(nil, map[string]any{"username": "u"}); got == nil || !got.Password.IsNull() {
		t.Errorf("import without state leaves the password null: %+v", got)
	}
}

// A block's needs are checked only when the block is there: an app that just
// runs an image must not be asked for registry credentials or a repo.
func TestAppConfigErrors(t *testing.T) {
	str := types.StringValue
	null := types.StringNull()
	unknown := types.StringUnknown()
	git := func(url types.String) *sourceModel { return &sourceModel{Git: &gitSourceModel{URL: url}} }
	cases := []struct {
		name string
		m    containerAppModel
		want string
	}{
		{"an image alone", containerAppModel{Image: str("nginx")}, ""},
		{"a repo alone", containerAppModel{Image: null, Source: git(str("https://github.com/acme/app"))}, ""},
		{"a private image", containerAppModel{Image: str("x"), ImageAuth: &imageAuthModel{Username: str("bot"), Password: unknown}}, ""},
		{"an image from a variable", containerAppModel{Image: unknown}, ""},
		{"neither", containerAppModel{Image: null}, "Missing image"},
		{"both", containerAppModel{Image: str("nginx"), Source: git(str("https://github.com/acme/app"))}, "Conflicting image and source"},
		{"a source block without a repo", containerAppModel{Image: null, Source: &sourceModel{}}, "Missing source.git.url"},
		{"a git block without a url", containerAppModel{Image: null, Source: git(null)}, "Missing source.git.url"},
		{"credentials without a password", containerAppModel{Image: str("x"), ImageAuth: &imageAuthModel{Username: str("bot"), Password: null}}, "Incomplete image_auth"},
		{"credentials for a repo", containerAppModel{Image: null, Source: git(str("https://github.com/acme/app")), ImageAuth: &imageAuthModel{Username: str("bot"), Password: str("pw")}}, "Conflicting image_auth and source"},
	}
	for _, c := range cases {
		got := ""
		if errs := appConfigErrors(c.m); len(errs) > 0 {
			got = errs[0][0]
		}
		if got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// A service of a composed app carries its stack, its hostname, what it
// starts after and which ports are internal — the settings that were lost on
// every apply before 0.6.0.
func TestContainerAppSpecStack(t *testing.T) {
	ctx := context.Background()
	ports, _ := types.ListValueFrom(ctx, types.ObjectType{AttrTypes: map[string]attr.Type{
		"name": types.StringType, "port": types.Int64Type, "internal": types.BoolType,
	}}, []appPortModel{
		{Name: types.StringValue("pg"), Port: types.Int64Value(5432), Internal: types.BoolValue(true)},
		{Name: types.StringValue("http"), Port: types.Int64Value(80), Internal: types.BoolNull()},
	})
	after, _ := types.ListValueFrom(ctx, types.StringType, []string{"shop-cache"})
	m := containerAppModel{
		Name: types.StringValue("shop-db"), Image: types.StringValue("postgres:17"),
		Stack: types.StringValue("shop"), Hostname: types.StringValue("db"), StartsAfter: after, Port: ports,
	}
	spec := containerAppSpec(ctx, m, false)
	if spec["stack"] != "shop" || spec["hostname"] != "db" {
		t.Errorf("stack/hostname: %v %v", spec["stack"], spec["hostname"])
	}
	if got, _ := spec["dependsOn"].([]string); len(got) != 1 || got[0] != "shop-cache" {
		t.Errorf("dependsOn: %v", spec["dependsOn"])
	}
	got := spec["ports"].([]map[string]any)
	if got[0]["internal"] != true || got[1]["internal"] != nil {
		t.Errorf("ports: %v", got)
	}
	// without a stack nothing about a stack is sent, and the hostname settles to nothing
	m2 := containerAppModel{Name: types.StringValue("web"), Image: types.StringValue("nginx"), Hostname: types.StringUnknown()}
	if spec := containerAppSpec(ctx, m2, false); spec["stack"] != nil || spec["hostname"] != nil || spec["dependsOn"] != nil {
		t.Errorf("a lone app sent stack fields: %v", spec)
	}
	settleHostname(&m2)
	if !m2.Hostname.IsNull() {
		t.Errorf("hostname without a stack: %v", m2.Hostname)
	}
	m.Hostname = types.StringUnknown()
	settleHostname(&m)
	if m.Hostname.ValueString() != "shop-db" {
		t.Errorf("hostname defaulted to %q, want the name", m.Hostname.ValueString())
	}
}
