package plugincontract

import "testing"

func TestSidebarPanelUsesProductionHostCapabilities(t *testing.T) {
	host := HostCapabilities()
	if !host.SurfaceTypes[SurfaceSidebarPanel] {
		t.Fatal("the shared Web/Desktop sidebar host must ship with its production capability")
	}
	manifest, _, err := ParseManifest([]byte(`{"manifest_version":1,"key":"com.example.sidebar","name":"Sidebar","version":"1.0.0","author":{"name":"Example"},"scopes":["issues:read"],"contributes":{"surfaces":[{"key":"sidebar","type":"sidebar_panel","name":"Sidebar","entry":"ui/main.js"}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := manifest.CheckCapabilities(host); err != nil {
		t.Fatal(err)
	}
	// Closing admission prevents new publishing/installing, but is not a
	// runtime revocation mechanism for existing installations.
	delete(host.SurfaceTypes, SurfaceSidebarPanel)
	if manifest.CheckCapabilities(host) == nil {
		t.Fatal("closed admission accepted a new sidebar contribution")
	}
}
