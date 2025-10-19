package devops

import (
	"reflect"
	"testing"

	"github.com/microsoft/azure-devops-go-api/azuredevops/v7/taskagent"
)

func req(result *taskagent.TaskResult, reservedID *int, demands ...interface{}) taskagent.TaskAgentJobRequest {
	j := taskagent.TaskAgentJobRequest{RequestId: uint64Ptr(1)}
	j.Result = result
	if reservedID != nil {
		j.ReservedAgent = &taskagent.TaskAgentReference{Id: reservedID}
	}
	if len(demands) > 0 {
		j.Demands = &demands
	}
	return j
}

func uint64Ptr(u uint64) *uint64 { return &u }
func intPtr(i int) *int          { return &i }

func TestParseJobRequests(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		want    int
		wantErr bool
	}{
		{"raw array", `[{"requestId":1},{"requestId":2}]`, 2, false},
		{"envelope", `{"count":1,"value":[{"requestId":3}]}`, 1, false},
		{"empty envelope value", `{"count":0,"value":[]}`, 0, false},
		{"empty object", `{}`, 0, false},
		{"whitespace", `   `, 0, false},
		{"garbage (lenient: empty list)", `not json`, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseJobRequests([]byte(tt.body))
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && len(got) != tt.want {
				t.Fatalf("len = %d, want %d", len(got), tt.want)
			}
		})
	}
}

func TestQueuedCounting(t *testing.T) {
	succeeded := taskagent.TaskResult("succeeded")
	jobs := []taskagent.TaskAgentJobRequest{
		req(nil, nil),        // queued
		req(&succeeded, nil), // finished
		req(nil, intPtr(7)),  // queued + reserved
	}
	if got := CountQueued(jobs); got != 2 {
		t.Fatalf("CountQueued = %d, want 2", got)
	}
	if got := len(QueuedJobs(jobs)); got != 2 {
		t.Fatalf("QueuedJobs = %d, want 2", got)
	}
	if got := CountInFlight(jobs); got != 1 {
		t.Fatalf("CountInFlight = %d, want 1", got)
	}
	if IsQueued(jobs[1]) {
		t.Fatal("job with result must not be queued")
	}
}

func TestDemands(t *testing.T) {
	job := req(nil, nil, "docker", "  os  linux  ", 42, " ")
	if got := JobDemandStrings(job); !reflect.DeepEqual(got, []string{"docker", "os  linux"}) {
		t.Fatalf("JobDemandStrings = %v", got)
	}
	if got := FirstDemand(job); got != "docker" {
		t.Fatalf("FirstDemand = %q", got)
	}
	if !QueuedJobForDemand(job, "Docker") {
		t.Fatal("expected case-insensitive bare demand match")
	}
	if !QueuedJobForDemand(job, "os") {
		t.Fatal("expected 'capability value' expression match for 'os'")
	}
	if QueuedJobForDemand(job, "windows") {
		t.Fatal("unexpected demand match for windows")
	}
}

func TestCountQueuedByCapability(t *testing.T) {
	succeeded := taskagent.TaskResult("succeeded")
	jobs := []taskagent.TaskAgentJobRequest{
		req(nil, nil, "docker", "linux"),
		req(nil, nil, "DOCKER"),
		req(&succeeded, nil, "docker"), // finished: not counted
	}
	want := map[string]int{"docker": 2, "linux": 1}
	if got := CountQueuedByCapability(jobs); !reflect.DeepEqual(got, want) {
		t.Fatalf("CountQueuedByCapability = %v, want %v", got, want)
	}
}

func TestExtractOrganizationName(t *testing.T) {
	tests := map[string]string{
		"https://dev.azure.com/my-org":                   "my-org",
		"https://dev.azure.com/my-org/project":           "my-org",
		"https://my-org.visualstudio.com":                "my-org",
		"https://my-org.visualstudio.com/collections/1":  "my-org",
		"https://tfs.company.com/tfs/CollectionName":     "CollectionName",
		"https://azuredevops.company.com/CollectionName": "CollectionName",
		"https://azuredevops.company.com":                "azuredevops.company.com",
		"not a url":                                      "Invalid",
	}
	for in, want := range tests {
		if got := ExtractOrganizationName(in); got != want {
			t.Errorf("ExtractOrganizationName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsOperatorManagedAgentName(t *testing.T) {
	tests := map[string]bool{
		"mypool-agent-0":        true,  // numeric index
		"mypool-agent-12":       true,  // numeric index
		"mypool-agent-a1b2c3d4": true,  // legacy 8-char alphanumeric
		"mypool-agent-abc":      false, // not numeric, not 8 chars
		"mypool-agent-":         false, // empty index
		"other-agent-0":         false, // wrong pool prefix
		"mypool-agent-0-001":    false, // non-numeric suffix
		"mypoolagent-0":         false, // missing -
		"MYPool-agent-0":        false, // prefix is case sensitive
	}
	for name, want := range tests {
		if got := IsOperatorManagedAgentName(name, "mypool"); got != want {
			t.Errorf("IsOperatorManagedAgentName(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestAgentHelpers(t *testing.T) {
	name, status, id := "a-b", "online", 9
	agent := taskagent.TaskAgent{Id: &id, Name: &name, Status: func() *taskagent.TaskAgentStatus { s := taskagent.TaskAgentStatus(status); return &s }()}
	if AgentName(agent) != name || AgentStatus(agent) != status || AgentID(agent) != id {
		t.Fatalf("agent helpers mismatch: %q %q %d", AgentName(agent), AgentStatus(agent), AgentID(agent))
	}
	empty := taskagent.TaskAgent{}
	if AgentName(empty) != "" || AgentStatus(empty) != "" || AgentID(empty) != 0 {
		t.Fatal("agent helpers must be nil-safe")
	}
}
