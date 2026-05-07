package coordinator

import (
	"encoding/json"
	"testing"
)

func TestSessionUnmarshalsLookupResponse(t *testing.T) {
	body := `{"session_id":"ses_abc","port":22099,"state":"pending","target_hostname":"lcm-a57d","authorized_user":"wyatt"}`
	var s Session
	if err := json.Unmarshal([]byte(body), &s); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if s.SessionID != "ses_abc" {
		t.Errorf("SessionID = %q", s.SessionID)
	}
	if s.Port != 22099 {
		t.Errorf("Port = %d", s.Port)
	}
	if s.State != "pending" {
		t.Errorf("State = %q", s.State)
	}
	if s.TargetHostname != "lcm-a57d" {
		t.Errorf("TargetHostname = %q", s.TargetHostname)
	}
}

func TestActivateRequestMarshalsToContractFields(t *testing.T) {
	req := ActivateRequest{
		TargetHostname:   "lcm-a57d",
		ActualPortBound:  22099,
		ClientRemoteAddr: "10.5.6.7:55432",
	}
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(b)
	for _, want := range []string{
		`"target_hostname":"lcm-a57d"`,
		`"actual_port_bound":22099`,
		`"client_remote_addr":"10.5.6.7:55432"`,
	} {
		if !contains(got, want) {
			t.Errorf("missing %q in JSON: %s", want, got)
		}
	}
}

func TestDeactivateRequestMarshalsToContractFields(t *testing.T) {
	req := DeactivateRequest{
		TargetHostname:  "lcm-a57d",
		ActualPortBound: 22099,
		Reason:          "client_disconnect",
	}
	b, _ := json.Marshal(req)
	got := string(b)
	for _, want := range []string{
		`"target_hostname":"lcm-a57d"`,
		`"actual_port_bound":22099`,
		`"reason":"client_disconnect"`,
	} {
		if !contains(got, want) {
			t.Errorf("missing %q in JSON: %s", want, got)
		}
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
