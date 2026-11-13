package main

import (
	"bytes"
	"embed"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"sort"
	"strings"

	"github.com/gorilla/mux"
)

// The admin dashboard's front end: one shell (index.html), one stylesheet,
// one core script and one script per page under admin_ui/pages/. A page
// registers itself with Admin.page({...}) (see core.js), so adding a page is
// dropping a file into pages/ -- the shell lists that folder itself.
//
// None of these files holds data: everything a page shows comes from
// /admin/api/*, which wants the admin key. That is why the files are served
// without one (the sign-in form has to load somehow).
//
//go:embed admin_ui
var adminUIFiles embed.FS

// adminUIPageScripts is the <script> list that replaces <!--PAGES--> in the
// shell: every pages/*.js, in name order.
func adminUIPageScripts() string {
	entries, err := fs.ReadDir(adminUIFiles, "admin_ui/pages")
	if err != nil {
		return ""
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".js") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		b.WriteString(`<script src="/admin/ui/pages/` + n + `"></script>` + "\n")
	}
	return b.String()
}

// adminUIIndex is the shell with the page scripts filled in.
func adminUIIndex() []byte {
	raw, err := adminUIFiles.ReadFile("admin_ui/index.html")
	if err != nil {
		return []byte("admin dashboard files are missing from this build")
	}
	return bytes.Replace(raw, []byte("<!--PAGES-->"), []byte(adminUIPageScripts()), 1)
}

// adminAPIExtra holds the route sets of the dashboard's newer pages. Each
// lives in its own file and adds itself from init() with adminRegisterAPI, so
// a page's back end is one file, like its front end; registerAdminDashboard
// mounts them all behind the admin key.
var adminAPIExtra []func(api *mux.Router)

func adminRegisterAPI(f func(api *mux.Router)) { adminAPIExtra = append(adminAPIExtra, f) }

func registerAdminUI(r *mux.Router) {
	index := adminUIIndex()
	r.HandleFunc("/admin/dashboard", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(index)
	}).Methods(http.MethodGet)
	r.PathPrefix("/admin/ui/").HandlerFunc(adminUIAsset).Methods(http.MethodGet)
}

// adminUIAsset serves one embedded file. path.Clean on a rooted path cannot
// climb out, and an embed.FS holds nothing but admin_ui/ anyway.
func adminUIAsset(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(path.Clean("/"+strings.TrimPrefix(r.URL.Path, "/admin/ui/")), "/")
	if name == "" || name == "index.html" {
		http.NotFound(w, r)
		return
	}
	raw, err := adminUIFiles.ReadFile("admin_ui/" + name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ctype := mime.TypeByExtension(path.Ext(name))
	switch path.Ext(name) {
	case ".js":
		ctype = "text/javascript; charset=utf-8"
	case ".css":
		ctype = "text/css; charset=utf-8"
	}
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ctype)
	// The files change with every build; a stale script against a new API is
	// worse than a few kilobytes per visit.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(raw)
}
