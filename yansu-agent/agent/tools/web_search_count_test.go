package tools

import "testing"

func TestBashRecordsOnlyTheSearchAdapter(t *testing.T) {
	var services []string
	bash := NewBashTool()
	bash.OnWebSearch = func(service string) { services = append(services, service) }
	for _, cmd := range []string{"opencli list", "opencli grok ask -h", "opencli grok ask private --site-session ephemeral", "opencli gemini ask private --site-session ephemeral", "opencli doubao ask private --site-session ephemeral"} {
		bash.recordWebSearch(cmd)
	}
	if len(services) != 3 || services[0] != "grok" || services[1] != "gemini" || services[2] != "doubao" {
		t.Fatalf("services %v", services)
	}
}
