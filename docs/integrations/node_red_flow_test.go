package integrations

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

type flowNode struct {
	ID      string          `json:"id"`
	Name    string          `json:"name"`
	Func    string          `json:"func"`
	Format  string          `json:"format"`
	Outputs int             `json:"outputs"`
	Wires   [][]string      `json:"wires"`
	Raw     json.RawMessage `json:"-"`
}

func loadFlowNodes(t *testing.T) map[string]flowNode {
	t.Helper()
	data, err := os.ReadFile("node-red-example-flow.json")
	if err != nil {
		t.Fatal(err)
	}
	var nodes []flowNode
	if err := json.Unmarshal(data, &nodes); err != nil {
		t.Fatal(err)
	}
	byID := make(map[string]flowNode, len(nodes))
	for _, node := range nodes {
		byID[node.ID] = node
	}
	return byID
}

func TestMeterFanOutWiresEveryDocumentedWidget(t *testing.T) {
	nodes := loadFlowNodes(t)
	node := nodes["8b31b2cadb2abe80"]
	if node.Outputs != 8 || len(node.Wires) != 8 {
		t.Fatalf("meter fan-out outputs/wires = %d/%d, want 8/8", node.Outputs, len(node.Wires))
	}
	want := []string{
		"6c3bdd1ac3ebd622",
		"5ca3bb4b9c0087f1",
		"94747badeb9a33ec",
		"1ac07f2b26234a6c",
		"70e8539f10472eb2",
		"d019f7b6b522c810",
		"8658fc305da14952",
		"dae6566f6782a2cf",
	}
	for i, id := range want {
		if len(node.Wires[i]) != 1 || node.Wires[i][0] != id {
			t.Fatalf("wire %d = %v, want [%s]", i, node.Wires[i], id)
		}
	}
	for _, field := range []string{"antennaSwr", "powerWatts", "paSupplyVoltage", "paCurrent", "temperatureLowerC", "temperatureCombinerC", "temperatureC"} {
		if !strings.Contains(node.Func, field) {
			t.Fatalf("meter fan-out does not reference %s", field)
		}
	}
}

func TestBacklightButtonsUseDirectDocumentedActions(t *testing.T) {
	nodes := loadFlowNodes(t)
	router := nodes["24961f1d1d05eacd"]
	for id, action := range map[string]string{
		"002c386a7ad4efbf": "backlight-off",
		"1a752b7206921eb4": "backlight-on",
	} {
		if !strings.Contains(nodes[id].Format, "payload: '"+action+"'") {
			t.Fatalf("%s does not send %s", nodes[id].Name, action)
		}
		if !strings.Contains(router.Func, "'"+action+"'") {
			t.Fatalf("%s is not allowed through the action router", action)
		}
	}
}

func TestActionFailuresExposeFanNavigationCauseAndRecovery(t *testing.T) {
	nodes := loadFlowNodes(t)
	formatter := nodes["4b8044f456aaf8ba"]
	for _, field := range []string{"navigation", "lastError", "recoveryInstructions"} {
		if !strings.Contains(formatter.Func, field) {
			t.Fatalf("action result formatter does not expose %s", field)
		}
	}
}
