package provider

import (
	"reflect"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/qalby-tech/terraform-provider-livellm/internal/client"
)

// What is left out takes the platform's default; a home folder's size goes
// only with keep_files.
func TestDesktopSpec(t *testing.T) {
	m := desktopAppModel{
		Replicas: types.Int64Value(3), CPU: types.StringValue("2"), Memory: types.StringValue("4Gi"),
		Image: types.StringNull(), Resolution: types.StringValue("1920x1080"),
		KeepFiles: types.BoolValue(true), StorageGi: types.Int64Value(20),
	}
	want := map[string]any{"replicas": int64(3), "cpu": "2", "memory": "4Gi", "resolution": "1920x1080",
		"keepFiles": true, "storageSize": "20Gi"}
	if got := desktopSpec(m); !reflect.DeepEqual(got, want) {
		t.Errorf("spec %v, want %v", got, want)
	}
	m.KeepFiles = types.BoolValue(false)
	if got := desktopSpec(m); got["keepFiles"] != nil || got["storageSize"] != nil {
		t.Errorf("without keep_files: %v", got)
	}
}

// Reading back what the platform holds: a change made in the console shows
// in the plan, and an import is complete.
func TestReadDesktopSpec(t *testing.T) {
	var m desktopAppModel
	readDesktopSpec(&m, &client.Workload{ID: "desks", Type: "desktop", Stopped: true, Desktop: map[string]any{
		"replicas": float64(2), "memory": "8Gi", "keepFiles": true, "storageSize": "15Gi",
	}})
	if m.Replicas.ValueInt64() != 2 || m.Memory.ValueString() != "8Gi" || !m.CPU.IsNull() || !m.Image.IsNull() ||
		!m.KeepFiles.ValueBool() || m.StorageGi.ValueInt64() != 15 || !m.Stopped.ValueBool() {
		t.Errorf("read %+v", m)
	}
	readDesktopSpec(&m, &client.Workload{ID: "desks", Type: "desktop", Desktop: map[string]any{}})
	if m.Replicas.ValueInt64() != 1 || m.KeepFiles.ValueBool() || !m.StorageGi.IsNull() || m.Stopped.ValueBool() {
		t.Errorf("defaults read %+v", m)
	}
}

func TestResolution(t *testing.T) {
	for s, ok := range map[string]bool{"1280x800": true, "1920x1080": true, "800": false, "1280 x 800": false, "12800x800": false} {
		if resolutionRe.MatchString(s) != ok {
			t.Errorf("%q: %v", s, !ok)
		}
	}
}
