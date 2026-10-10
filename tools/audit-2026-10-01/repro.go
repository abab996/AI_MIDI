// Isolated audit probes. Positive regression assertions for the original nine backend observations.
// All data is created in a temporary directory; no server or audio engine starts.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"melodymate/internal/config"
	"melodymate/internal/engine"
	"melodymate/internal/project"
	"melodymate/internal/server"
	"melodymate/internal/soundlib"
)

var confirmed int

func must(err error) {
	if err != nil {
		panic(err)
	}
}
func check(name string, present bool, detail string) {
	if !present {
		panic("REGRESSION: " + name + ": " + detail)
	}
	confirmed++
	fmt.Printf("PASS %s: %s\n", name, detail)
}

func main() {
	root, err := os.MkdirTemp("", "melodymate-audit-20261001-")
	must(err)
	defer os.RemoveAll(root) // exact directory returned by MkdirTemp
	config.ProjectRoot = root
	config.ProjectsDir = filepath.Join(root, "projects")
	config.LibraryDir = filepath.Join(root, "Library")
	config.LibraryUserDir = filepath.Join(config.LibraryDir, "user")
	config.SettingsFile = filepath.Join(root, "settings.json")
	config.OutputDir = filepath.Join(root, "output")
	config.WebDir = filepath.Join(root, "frontend")
	project.ResetProjectsIndexCache()
	meta, err := project.CreateProject("isolated audit")
	must(err)
	router := server.NewRouter(nil, nil)
	request := func(method, path string, body []byte, token bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "http://127.0.0.1"+path, bytes.NewReader(body))
		if token {
			req.Header.Set("X-MM-Token", router.AuthToken())
		}
		req.Header.Set("Content-Type", "application/json")
		res := httptest.NewRecorder()
		router.ServeHTTP(res, req)
		return res
	}
	base := "/api/projects/" + meta.ID
	must(os.WriteFile(filepath.Join(project.MidiDir(meta.ID), "audit.mid"), []byte("audit"), 0600))
	project.SaveMidiManifest(meta.ID, []project.MidiFileInfo{{Name: "audit.mid", Path: filepath.Join(project.MidiDir(meta.ID), "audit.mid")}})
	unauth := request("GET", base+"/download?names=audit.mid", nil, false)
	auth := request("GET", base+"/download?names=audit.mid", nil, true)
	check("HTTP-DOWNLOAD", unauth.Code == 401 && auth.Code == 200,
		fmt.Sprintf("navigation=%d, authenticated fetch=%d", unauth.Code, auth.Code))

	arr := project.NewArrangement()
	arr["mixer"].(map[string]any)["master"] = map[string]any{"gain": 1.0}
	raw, err := json.Marshal(arr)
	must(err)
	_, err = project.WriteArrangementVersioned(meta.ID, raw)
	must(err)
	stale, err := project.ReadArrangement(meta.ID)
	must(err)
	changed, err := project.ApplyOps(meta.ID, []map[string]any{{"op": "setMaster", "gain": 0.2}})
	must(err)
	rev, err := project.WriteArrangementVersioned(meta.ID, stale)
	conflictErr := err
	saved := project.LoadArrangementV2(meta.ID)
	gain := saved["mixer"].(map[string]any)["master"].(map[string]any)["gain"].(float64)
	check("STALE-PUT", conflictErr != nil && gain == 0.2,
		fmt.Sprintf("AI gain remains at rev=%d; gain=%v, stale return rev=%d", changed.Rev, gain, rev))

	arr["padding"] = strings.Repeat("x", (2<<20)+1024)
	large, err := json.Marshal(arr)
	must(err)
	arr["rev"] = changed.Rev
	large, err = json.Marshal(arr)
	must(err)
	res := request("PUT", base+"/arrangement", large, true)
	check("ARRANGEMENT-LIMIT", res.Code == 200,
		fmt.Sprintf("%d-byte valid arrangement: HTTP status=%d", len(large), res.Code))

	pluginDir := filepath.Join(config.LibraryDir, "plugins", "com.audit.fx@1.0.0")
	must(os.MkdirAll(filepath.Join(pluginDir, "ui"), 0700))
	must(os.WriteFile(filepath.Join(pluginDir, "manifest.json"), []byte(`{"id":"com.audit.fx","name":"Audit FX","version":"1.0.0","kind":"effect","ui":{"entry":"ui/custom.html"}}`), 0600))
	must(os.WriteFile(filepath.Join(pluginDir, "ui", "custom.html"), []byte(`<script src="./control.js"></script>`), 0600))
	must(os.WriteFile(filepath.Join(pluginDir, "ui", "control.js"), []byte("/* audit */"), 0600))
	src, _, err := soundlib.SyncInstalledMM(pluginDir)
	must(err)
	check("MM-REGISTRY-PATH", soundlib.ResolvePath(src.Path) == pluginDir,
		"registry path resolves to the installed plugin directory")
	_, err = soundlib.Update(src.ID, func(s *soundlib.Source) error { s.Enabled = false; return nil })
	must(err)
	resynced, _, err := soundlib.SyncInstalledMM(pluginDir)
	must(err)
	check("MM-DISABLE-RESET", !resynced.Enabled, "refresh retains Enabled=false")

	packDir := filepath.Join(root, "user-samples")
	must(os.MkdirAll(packDir, 0700))
	pack, err := soundlib.Add(soundlib.Source{ID: "audit-pack", Name: "Audit Pack", Kind: soundlib.KindSamplePack, Origin: soundlib.OriginUser, Enabled: true, Dirs: []string{packDir}})
	must(err)
	soundlib.SyncMaterialDirs()
	_, err = soundlib.Migrate()
	must(err)
	_, err = soundlib.Delete(pack.ID)
	must(err)
	soundlib.SyncMaterialDirs()
	remaining := config.LoadSettings().MaterialDirs
	check("PACK-DELETE-MIRROR", len(remaining) == 0,
		"deleted user pack's directory is removed from the mirror")

	engine.SetGlobal(&engine.Supervisor{}) // route gate only; never Start or load native code
	defer engine.SetGlobal(nil)
	ticket := func() string {
		response := request("POST", "/api/engine/plugins/ui-ticket", []byte(`{"plugin":"`+src.ID+`","project":"`+meta.ID+`"}`), true)
		var data map[string]any
		must(json.Unmarshal(response.Body.Bytes(), &data))
		return data["ticket"].(string)
	}
	_, err = soundlib.Update(src.ID, func(s *soundlib.Source) error { s.Enabled = true; return nil })
	must(err)
	uipath := "/api/engine/plugins/ui?plugin=com.audit.fx&ticket="
	res = request("GET", uipath+ticket(), nil, false)
	check("MM-UI-ENTRY", res.Code == 303 && strings.HasSuffix(res.Header().Get("Location"), "ui/custom.html"), "manifest ui.entry=ui/custom.html is selected")
	must(os.WriteFile(filepath.Join(pluginDir, "ui", "index.html"), []byte(`<script src="./control.js"></script>`), 0600))
	pageURL, err := url.Parse("http://127.0.0.1" + uipath + ticket())
	must(err)
	page := request("GET", pageURL.RequestURI(), nil, false)
	pageURL, err = url.Parse("http://127.0.0.1" + page.Header().Get("Location"))
	must(err)
	page = request("GET", pageURL.RequestURI(), nil, false)
	assetURL := pageURL.ResolveReference(&url.URL{Path: "./control.js"})
	asset := request("GET", assetURL.RequestURI(), nil, false)
	check("MM-UI-ASSETS", page.Code == 200 && asset.Code == 200,
		fmt.Sprintf("HTML=%d, relative ./control.js resolves to %s and status=%d", page.Code, assetURL.Path, asset.Code))

	bootstrap := request("GET", "/api/bootstrap.js", nil, false)
	var boot map[string]any
	must(json.Unmarshal(bootstrap.Body.Bytes(), &boot))
	check("BOOTSTRAP-LOCAL-CLIENT", bootstrap.Code == 401 && boot["token"] == nil,
		"an unauthorized local client cannot retrieve a token")
	fmt.Printf("%d isolated backend regression assertions passed.\n", confirmed)
}
