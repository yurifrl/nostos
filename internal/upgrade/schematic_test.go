package upgrade

import "testing"

func TestParseSchematic(t *testing.T) {
	out := []byte(`{"spec":{"metadata":{"name":"tailscale","version":"1.2"}}}
{"spec":{"metadata":{"name":"schematic","version":"8f04ea6b"}}}`)
	got, err := parseSchematic(out)
	if err != nil || got != "8f04ea6b" {
		t.Fatalf("got %q, %v", got, err)
	}
	if got, err := parseSchematic([]byte(`{"spec":{"metadata":{"name":"x"}}}`)); err != nil || got != "" {
		t.Fatalf("no schematic: got %q, %v", got, err)
	}
}

func TestAddSchematicDriftSameVersion(t *testing.T) {
	ordered := OrderNodes([]NodeRef{{Name: "cp1", Role: "controlplane"}, {Name: "w1"}, {Name: "w2"}})
	target := mustV(t, "v1.14.2")
	current := map[string]Version{"cp1": target, "w1": target, "w2": target}
	plan, err := BuildPlan("c", ordered, current, target, []Version{target})
	if err != nil || len(plan.Steps) != 0 {
		t.Fatalf("precondition: %v %+v", err, plan.Steps)
	}
	AddSchematicDrift(&plan, ordered, current, target, map[string]bool{"cp1": true})
	if len(plan.Steps) != 1 || plan.Steps[0].Version != "v1.14.2" || len(plan.Steps[0].Nodes) != 1 || plan.Steps[0].Nodes[0].Name != "cp1" {
		t.Fatalf("steps = %+v", plan.Steps)
	}
	if len(plan.Reinstall) != 1 || plan.Reinstall[0] != "cp1" {
		t.Fatalf("reinstall = %v", plan.Reinstall)
	}
}

func TestAddSchematicDriftMergesIntoTargetStep(t *testing.T) {
	ordered := OrderNodes([]NodeRef{{Name: "cp1", Role: "controlplane"}, {Name: "w1"}, {Name: "w2"}})
	target := mustV(t, "v1.14.2")
	current := map[string]Version{"cp1": target, "w1": mustV(t, "v1.14.1"), "w2": target}
	plan, _ := BuildPlan("c", ordered, current, target, []Version{target})
	AddSchematicDrift(&plan, ordered, current, target, map[string]bool{"cp1": true, "w1": true})
	if len(plan.Steps) != 1 {
		t.Fatalf("steps = %+v", plan.Steps)
	}
	var names []string
	for _, n := range plan.Steps[0].Nodes {
		names = append(names, n.Name)
	}
	if len(names) != 2 || names[0] != "w1" || names[1] != "cp1" {
		t.Fatalf("order = %v (want workers first, cp last; w2 untouched)", names)
	}
	if len(plan.Reinstall) != 1 || plan.Reinstall[0] != "cp1" {
		t.Fatalf("reinstall = %v (w1 is a real version hop)", plan.Reinstall)
	}
}

func TestAddSchematicDriftIgnoresBehindTarget(t *testing.T) {
	// A drifted node that is behind target is already in a step, so not reinstalled separately.
	ordered := []NodeRef{{Name: "w1"}}
	target := mustV(t, "v1.14.2")
	plan := Plan{}
	AddSchematicDrift(&plan, ordered, map[string]Version{"w1": mustV(t, "v1.13.0")}, target, map[string]bool{"w1": true})
	if len(plan.Steps) != 0 {
		t.Fatalf("steps = %+v", plan.Steps)
	}
}
