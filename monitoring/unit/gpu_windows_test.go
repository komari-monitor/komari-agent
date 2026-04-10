//go:build windows
// +build windows

package monitoring

import "testing"

func TestNormalizeDeviceID(t *testing.T) {
	given := "  pci\\ven_10de&dev_24dd\\abc  "
	got := normalizeDeviceID(given)
	if got != "PCI\\VEN_10DE&DEV_24DD\\ABC" {
		t.Fatalf("normalizeDeviceID() = %q", got)
	}
}

func TestIsVirtualWindowsGPUByPNPPrefix(t *testing.T) {
	if !isVirtualWindowsGPU("Some GPU", "SWD\\Some\\Path") {
		t.Fatal("expected SWD PNPDeviceID to be treated as virtual")
	}
	if !isVirtualWindowsGPU("Some GPU", "ROOT\\Some\\Path") {
		t.Fatal("expected ROOT PNPDeviceID to be treated as virtual")
	}
	if isVirtualWindowsGPU("NVIDIA GeForce RTX 3070 Laptop GPU", "PCI\\VEN_10DE&DEV_24DD") {
		t.Fatal("expected physical PCI adapter to not be treated as virtual")
	}
}

func TestIsVirtualWindowsGPUByName(t *testing.T) {
	cases := []string{
		"Microsoft Basic Render Driver",
		"Microsoft Remote Display Adapter",
		"VMware SVGA 3D",
		"VirtIO GPU",
	}
	for _, name := range cases {
		if !isVirtualWindowsGPU(name, "") {
			t.Fatalf("expected %q to be treated as virtual", name)
		}
	}
}

func TestDecodeVideoControllersJSON(t *testing.T) {
	t.Run("array", func(t *testing.T) {
		got, err := decodeVideoControllersJSON(`[{"Name":"GPU1","PNPDeviceID":"PCI\\A","Status":"OK","ConfigManagerErrorCode":0},{"Name":"GPU2","PNPDeviceID":"PCI\\B","Status":"OK","ConfigManagerErrorCode":0}]`)
		if err != nil {
			t.Fatalf("decodeVideoControllersJSON(array) error: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("expected 2 controllers, got %d", len(got))
		}
	})

	t.Run("single", func(t *testing.T) {
		got, err := decodeVideoControllersJSON(`{"Name":"GPU1","PNPDeviceID":"PCI\\A","Status":"OK","ConfigManagerErrorCode":0}`)
		if err != nil {
			t.Fatalf("decodeVideoControllersJSON(single) error: %v", err)
		}
		if len(got) != 1 || got[0].Name != "GPU1" {
			t.Fatalf("unexpected decode result: %+v", got)
		}
	})

	t.Run("null", func(t *testing.T) {
		got, err := decodeVideoControllersJSON("null")
		if err != nil {
			t.Fatalf("decodeVideoControllersJSON(null) error: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("expected empty result for null, got %d", len(got))
		}
	})
}

func TestFormatVideoControllerNames(t *testing.T) {
	controllers := []win32VideoController{
		{Name: "NVIDIA GeForce RTX 3070 Laptop GPU", PNPDeviceID: "PCI\\VEN_10DE&DEV_24DD&SUBSYS_A", Status: "OK", ConfigManagerErrorCode: 0},
		{Name: "NVIDIA GeForce RTX 3070 Laptop GPU", PNPDeviceID: "PCI\\VEN_10DE&DEV_24DD&SUBSYS_A", Status: "OK", ConfigManagerErrorCode: 0}, // duplicate device id
		{Name: "NVIDIA GeForce RTX 3070 Laptop GPU", PNPDeviceID: "PCI\\VEN_10DE&DEV_24DE&SUBSYS_B", Status: "OK", ConfigManagerErrorCode: 0},
		{Name: "Intel(R) UHD Graphics", PNPDeviceID: "PCI\\VEN_8086&DEV_9A49", Status: "OK", ConfigManagerErrorCode: 0},
		{Name: "Microsoft Basic Render Driver", PNPDeviceID: "ROOT\\BASICRENDER", Status: "OK", ConfigManagerErrorCode: 0}, // filtered virtual
		{Name: "Disabled GPU", PNPDeviceID: "PCI\\VEN_1234&DEV_5678", Status: "Error", ConfigManagerErrorCode: 0},          // filtered by status
		{Name: "Broken GPU", PNPDeviceID: "PCI\\VEN_1234&DEV_5679", Status: "OK", ConfigManagerErrorCode: 22},              // filtered by config manager
	}

	got := formatVideoControllerNames(controllers)
	want := "NVIDIA GeForce RTX 3070 Laptop GPU × 2, Intel(R) UHD Graphics"
	if got != want {
		t.Fatalf("formatVideoControllerNames() = %q, want %q", got, want)
	}
}
