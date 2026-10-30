package main

import (
	"html/template"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The AV Setup pages the patch is tested against.
//
// The synthetic page below ships with the repo; it is written here, not copied
// from a device, because the real template is BirdDog's copyrighted UI source
// and this repo is public — the same line bdts and bd-play-usb-player draw. It
// reproduces the structure the patch depends on: the decode-settings tab bar
// and the content div, inside the {{define}} block the page renders.
//
// Any real firmware pages dropped into testdata/firmware/ (gitignored) as
// videoset.<version>.html are tested too.
const syntheticVideoset = `{{template "layout.html" .}}
{{define "videoset"}}
<div class="row m-0 p-0 row_in_page cm_h_lg_auto">
    <div class="col-12 m-0 p-2">
        <div class="div_content_box back_gray_color">
		  {{if or (eq .HWVersion "BirdDog 4K QUAD") (eq .HWVersion "BirdDog 4K GEAR")}}
		 <div class="tab">
		 <button {{if or (ne .Mode "decode")}} style="display:none;" {{end}} id="tab1" class="dectablinks" onclick="opendecTab(event, 'dec1_form')">Decode-1 Settings</button>
		 </div>
		{{else}}
            <div class="tab">
                <button  id="tab1" class="dectablinks" onclick="opendecTab(event, 'dec1_form')">Decode Settings</button>
            </div>
        {{end}}
            <div class="div_box_content">
                <div class="row m-0 p-0 cm_h_lg_100">
                    <div class="col-12 m-0 p-0 v_center_parent">
                        <div class="pl-3 pr-3 pb-3 pt-0" id="div_advanced_settings_content">
                            <form class="dectabcontent" id="dec1_form" method="post" action="/videoset">
                                <select id="Source_Selection" onchange='this.form.submit();' name="SourceSelection">
                                    <option value="NDI" {{if eq .Source_Selection "NDI"}} selected {{end}}>NDI</option>
                                    <option value="SRT" {{if eq .Source_Selection "SRT"}} selected  {{end}} >SRT</option>
                                </select>
                            </form>
                        </div>
                    </div>
                </div>
            </div>
        </div>
    </div>
</div>
<script>
    function opendecTab(evt, target) {
        $("#div_advanced_settings_content .div_tab_contents").hide();
        $("#div_advanced_settings_content #"+target).show();
    }
</script>
{{end}}`

// bdcamPatched and bdgwPatched are the page after bdcam's UVC Converter tab
// and bdgw's Streaming tab have been applied — their markers and anchors are
// reproduced from converter/patch.go and gateway/patch.go, so this test breaks
// if any of the three projects changes how it anchors.
func bdcamPatched(src string) string {
	return otherTab(src, "<!-- bdcam-uvc-tab:start -->", "<!-- bdcam-uvc-tab:end -->", "tab_uvc", "uvc_form", "UVC Converter", "uvc-converter.js")
}

func bdgwPatched(src string) string {
	return otherTab(src, "<!-- bdgw-streaming-tab:start -->", "<!-- bdgw-streaming-tab:end -->", "tab_bdgw", "bdgw_form", "Streaming", "bdgw-streaming.js")
}

func otherTab(src, start, end, tab, form, label, asset string) string {
	btn := start + `<button id="` + tab + `" class="dectablinks" onclick="opendecTab(event, '` + form + `')">` + label + `</button>` + end
	body := start + `<div class="dectabcontent" id="` + form + `" style="display:none;"></div><script src="/static/` + asset + `?v=abcd1234"></script>` + end
	out := strings.Replace(src, anchorTabButton, anchorTabButton+"\n\t\t\t\t"+btn, 1)
	return strings.Replace(out, anchorContent, anchorContent+"\n\t\t\t\t\t\t"+body, 1)
}

func pages(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{
		"synthetic/stock":              syntheticVideoset,
		"synthetic/bdcam-applied":      bdcamPatched(syntheticVideoset),
		"synthetic/bdcam-bdgw-applied": bdgwPatched(bdcamPatched(syntheticVideoset)),
	}
	matches, _ := filepath.Glob("testdata/firmware/videoset.*.html")
	for _, m := range matches {
		b, err := os.ReadFile(m)
		if err != nil {
			t.Fatal(err)
		}
		name := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(m), "videoset."), ".html")
		out["firmware/"+name] = string(b)
		out["firmware/"+name+"/bdcam-applied"] = bdcamPatched(string(b))
	}
	if len(matches) == 0 {
		t.Log("no real firmware pages in testdata/firmware/ — testing the synthetic page only. " +
			"Copy a device's /srv/birddog-web-ui/videoset.html there to test the genuine markup.")
	}
	return out
}

func eachPage(t *testing.T, fn func(t *testing.T, src string)) {
	t.Helper()
	for name, src := range pages(t) {
		t.Run(name, func(t *testing.T) { fn(t, src) })
	}
}

func TestAnchorsAreUniqueInEveryPage(t *testing.T) {
	eachPage(t, func(t *testing.T, src string) {
		if n := strings.Count(src, anchorTabButton); n != 1 {
			t.Fatalf("tab anchor appears %d times, want 1", n)
		}
		if n := strings.Count(src, anchorContent); n != 1 {
			t.Fatalf("content anchor appears %d times, want 1", n)
		}
	})
}

func TestPatchIsIdempotentAndExactlyReversible(t *testing.T) {
	js := renderAsset(8094)
	eachPage(t, func(t *testing.T, src string) {
		once, err := PatchVideoset(src, js)
		if err != nil {
			t.Fatal(err)
		}
		if once == src {
			t.Fatal("patch changed nothing")
		}
		twice, err := PatchVideoset(once, js)
		if err != nil {
			t.Fatal(err)
		}
		if twice != once {
			t.Fatal("patching twice changed the file again")
		}
		if got := UnpatchVideoset(once); got != src {
			t.Fatal("unpatch did not restore the page byte for byte")
		}
		if strings.Count(once, patchStart) != 2 || strings.Count(once, patchEnd) != 2 {
			t.Fatal("expected exactly two marked blocks")
		}
	})
}

func TestPatchCoexistsWithTheOtherTabsInAnyOrder(t *testing.T) {
	js := renderAsset(8094)
	stock := syntheticVideoset
	ours := func(s string) string {
		out, err := PatchVideoset(s, js)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	orders := map[string]string{
		"bdcam-bdgw-us": ours(bdgwPatched(bdcamPatched(stock))),
		"us-bdcam-bdgw": bdgwPatched(bdcamPatched(ours(stock))),
		"bdcam-us-bdgw": bdgwPatched(ours(bdcamPatched(stock))),
	}
	for name, page := range orders {
		for _, id := range []string{`id="tab_uvc"`, `id="tab_bdgw"`, `id="tab_bdomt"`, `id="uvc_form"`, `id="bdgw_form"`, `id="bdomt_form"`} {
			if strings.Count(page, id) != 1 {
				t.Fatalf("%s: %s appears %d times, want 1", name, id, strings.Count(page, id))
			}
		}
		// Removing ours leaves the others exactly as they were.
		got := UnpatchVideoset(page)
		if strings.Contains(got, patchStart) {
			t.Fatalf("%s: our markers survived unpatch", name)
		}
		if strings.Count(got, "bdcam-uvc-tab:start") != 2 || strings.Count(got, "bdgw-streaming-tab:start") != 2 {
			t.Fatalf("%s: unpatch touched another module's tab", name)
		}
	}
	if UnpatchVideoset(orders["bdcam-bdgw-us"]) != bdgwPatched(bdcamPatched(stock)) {
		t.Fatal("unpatch did not give back the other modules' page byte for byte")
	}
}

// A reinstall with a changed tab script must swap the block, so the page asks
// for the script under its new hash.
func TestApplySwapsAChangedScript(t *testing.T) {
	dir := t.TempDir()
	tpl := filepath.Join(dir, "videoset.html")
	if err := os.MkdirAll(filepath.Join(dir, "static"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tpl, []byte(syntheticVideoset), 0o644); err != nil {
		t.Fatal(err)
	}
	u := UIPaths{Dir: dir}
	if err := ApplyPatch(u, 8094); err != nil {
		t.Fatal(err)
	}
	if err := ApplyPatch(u, 9999); err != nil {
		t.Fatalf("apply with a different port: %v", err)
	}
	got, _ := os.ReadFile(tpl)
	want := tabContent(renderAsset(9999))
	if !strings.Contains(string(got), want) || strings.Count(string(got), patchStart) != 2 {
		t.Fatal("the page does not reference the new script exactly once")
	}
}

func TestPatchAddsNoTemplateActions(t *testing.T) {
	js := renderAsset(8094)
	eachPage(t, func(t *testing.T, src string) {
		out, err := PatchVideoset(src, js)
		if err != nil {
			t.Fatal(err)
		}
		for _, tok := range []string{"{{", "}}"} {
			if b, a := strings.Count(src, tok), strings.Count(out, tok); b != a {
				t.Fatalf("%q count changed from %d to %d", tok, b, a)
			}
		}
	})
}

// The test this file exists for: birddog-web-ui parses the page with
// html/template at startup, and a parse error takes the whole web UI down.
func TestPatchedTemplateStillParses(t *testing.T) {
	js := renderAsset(8094)
	eachPage(t, func(t *testing.T, src string) {
		out, err := PatchVideoset(src, js)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := template.New("videoset.html").Parse(out); err != nil {
			t.Fatalf("patched page does not parse: %v", err)
		}
		at := strings.Index(out, patchStart)
		end := strings.LastIndex(out, "{{end}}")
		if at < 0 || end < 0 || at > end {
			t.Fatal("patch landed outside the define block")
		}
	})
}

func TestUnterminatedMarkerIsLeftAlone(t *testing.T) {
	src := "a\n" + patchStart + "half-edited by hand\n"
	if got := UnpatchVideoset(src); got != src {
		t.Fatal("an unterminated block should be left as is, not eaten to the end of the file")
	}
}

func TestApplyAndRemoveOnDisk(t *testing.T) {
	dir := t.TempDir()
	tpl := filepath.Join(dir, "videoset.html")
	if err := os.MkdirAll(filepath.Join(dir, "static"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tpl, []byte(syntheticVideoset), 0o644); err != nil {
		t.Fatal(err)
	}
	u := UIPaths{Dir: dir}
	if err := ApplyPatch(u, 8094); err != nil {
		t.Fatal(err)
	}
	asset, err := os.ReadFile(u.Asset())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(asset), "var API_PORT = 8094;") || strings.Contains(string(asset), portPlaceholder) {
		t.Fatal("port not substituted into the asset")
	}
	backup, err := os.ReadFile(u.Backup())
	if err != nil || string(backup) != syntheticVideoset {
		t.Fatal("backup missing or not pristine")
	}
	if err := ApplyPatch(u, 8094); err != errAlreadyPatched {
		t.Fatalf("second apply: got %v, want errAlreadyPatched", err)
	}
	if err := RemovePatch(u); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(tpl)
	if string(got) != syntheticVideoset {
		t.Fatal("remove did not restore the page")
	}
	if _, err := os.Stat(u.Asset()); !os.IsNotExist(err) {
		t.Fatal("asset not removed")
	}
	// RestoreBackup works even after a hand edit.
	_ = os.WriteFile(tpl, []byte("garbage"), 0o644)
	if err := RestoreBackup(u); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(tpl)
	if string(got) != syntheticVideoset {
		t.Fatal("restore did not put the backup back")
	}
}

func TestAssetVersionTracksContent(t *testing.T) {
	if assetVersion("a") == assetVersion("b") {
		t.Fatal("different scripts must get different cache-busters")
	}
	if !strings.Contains(tabContent(renderAsset(8094)), "?v="+assetVersion(renderAsset(8094))) {
		t.Fatal("script URL does not carry the asset version")
	}
}
