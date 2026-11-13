package main

import "testing"

func TestConnectorOfLabel(t *testing.T) {
	if id, err := connectorOf("crm:42"); err != nil || id != 42 {
		t.Fatalf("id = %d %v", id, err)
	}
	for _, bad := range []string{"", "42", "crm:", "crm:0", "crm:-1", "crm:x", "other:4"} {
		if _, err := connectorOf(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
