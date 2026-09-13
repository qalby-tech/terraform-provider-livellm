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
