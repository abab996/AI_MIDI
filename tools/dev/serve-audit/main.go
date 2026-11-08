package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"melodymate/internal/config"
	"melodymate/internal/server"
	"net/http"
	"os"
	"path/filepath"
)

func main() {
	root := flag.String("root", "output/audit-2026-10-01/live-data", "isolated data root")
	port := flag.Int("port", 7912, "local test port")
	flag.Parse()
	repo, _ := os.Getwd()
	abs, _ := filepath.Abs(*root)
	os.MkdirAll(abs, 0700)
	config.ProjectRoot = abs
	config.ProjectsDir = filepath.Join(abs, "projects")
	config.LibraryDir = filepath.Join(abs, "Library")
	config.LibraryUserDir = filepath.Join(config.LibraryDir, "user")
	config.SettingsFile = filepath.Join(abs, "settings.json")
	config.OutputDir = filepath.Join(abs, "output")
	config.WebDir = filepath.Join(repo, "frontend")
	router := server.NewRouter(os.DirFS(config.WebDir), nil)
	base := fmt.Sprintf("http://127.0.0.1:%d", *port)
	router.AllowOrigins([]string{base})
	raw, _ := json.Marshal(map[string]string{"base": base, "token": router.AuthToken(), "launch": router.BrowserLaunchURL(base + "/chat.html")})
	os.WriteFile(filepath.Join(abs, "authorization.json"), raw, 0600)
	fmt.Println("isolated audit service ready")
	if err := http.ListenAndServe(fmt.Sprintf("127.0.0.1:%d", *port), router); err != nil {
		panic(err)
	}
}
