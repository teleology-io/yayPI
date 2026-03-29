package plugin

import (
	"bytes"
	"fmt"
	"go/format"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
	"unicode"

	"github.com/csullivan/yaypi/internal/config"
)

// PluginEntry holds resolved metadata for a single plugin that has a source path.
type PluginEntry struct {
	// Name is the plugin name from yaypi.yaml (e.g. "hash-password").
	Name string
	// SrcDir is the absolute path to the plugin's Go source directory.
	SrcDir string
	// PkgName is the Go package name (last path segment, sanitized).
	PkgName string
	// ImportAlias is the alias used in the generated import block (e.g. "p0").
	ImportAlias string
	// Entities is the list of entity names that reference this plugin in any hook.
	Entities []string
	// Config is the map from yaypi.yaml config: section for this plugin.
	Config map[string]interface{}
}

// BuildContext holds everything the code generator needs.
type BuildContext struct {
	// YaypiRoot is the absolute path to the yaypi module root (where go.mod lives).
	YaypiRoot string
	// YaypiModule is the module path read from the yaypi go.mod (e.g. "github.com/csullivan/yaypi").
	YaypiModule string
	// GoVersion is the Go version string from yaypi go.mod (e.g. "1.25.0").
	GoVersion string
	// Plugins is the resolved list of plugins with source paths.
	Plugins []PluginEntry
}

// ResolvePlugins inspects cfg.Plugins and cfg.Entities to build a BuildContext.
// configDir is the directory containing yaypi.yaml (used to resolve relative paths).
// yaypiRoot is the absolute path to the yaypi module root.
func ResolvePlugins(cfg *config.RootConfig, configDir, yaypiRoot, yaypiModule, goVersion string) (*BuildContext, error) {
	// Build hook map: pluginName → []entityName
	hookMap := make(map[string][]string)
	for _, ec := range cfg.Entities {
		def := ec.Entity
		addHooks(hookMap, def.Name, def.Hooks.BeforeCreate)
		addHooks(hookMap, def.Name, def.Hooks.AfterCreate)
		addHooks(hookMap, def.Name, def.Hooks.BeforeUpdate)
		addHooks(hookMap, def.Name, def.Hooks.AfterUpdate)
		addHooks(hookMap, def.Name, def.Hooks.BeforeDelete)
		addHooks(hookMap, def.Name, def.Hooks.AfterDelete)
	}

	var entries []PluginEntry
	for i, pc := range cfg.Plugins {
		if pc.Path == "" {
			continue
		}
		absPath := pc.Path
		if !filepath.IsAbs(absPath) {
			absPath = filepath.Join(configDir, absPath)
		}
		absPath = filepath.Clean(absPath)

		// Derive package name from the directory name, sanitized to a valid Go identifier.
		pkgName := sanitizeIdent(filepath.Base(absPath))
		if pkgName == "" {
			return nil, fmt.Errorf("plugin %q: cannot derive Go package name from path %q", pc.Name, absPath)
		}

		entries = append(entries, PluginEntry{
			Name:        pc.Name,
			SrcDir:      absPath,
			PkgName:     pkgName,
			ImportAlias: fmt.Sprintf("p%d", i),
			Entities:    hookMap[pc.Name],
			Config:      pc.Config,
		})
	}

	return &BuildContext{
		YaypiRoot:   yaypiRoot,
		YaypiModule: yaypiModule,
		GoVersion:   goVersion,
		Plugins:     entries,
	}, nil
}

// GenerateBuildDir writes a self-contained Go module into buildDir that imports
// the plugin packages and defines initPlugins(). Returns the path to the generated main.go.
//
// buildDir must already exist. The caller is responsible for cleaning it up.
func GenerateBuildDir(bctx *BuildContext, buildDir string) error {
	// 1. Copy each plugin source directory into buildDir/plugins/<pkgName>/
	for _, p := range bctx.Plugins {
		dst := filepath.Join(buildDir, "plugins", p.PkgName)
		if err := copyDir(p.SrcDir, dst); err != nil {
			return fmt.Errorf("copying plugin %q from %s: %w", p.Name, p.SrcDir, err)
		}
	}

	// 2. Write go.mod
	gomod := generateGoMod(bctx, buildDir)
	if err := os.WriteFile(filepath.Join(buildDir, "go.mod"), []byte(gomod), 0644); err != nil {
		return fmt.Errorf("writing go.mod: %w", err)
	}

	// 3. Write registry_gen.go
	regSrc, err := generateRegistry(bctx)
	if err != nil {
		return fmt.Errorf("generating registry: %w", err)
	}
	if err := os.WriteFile(filepath.Join(buildDir, "registry_gen.go"), regSrc, 0644); err != nil {
		return fmt.Errorf("writing registry_gen.go: %w", err)
	}

	// 4. Copy cmd/yaypi/main.go with initPlugins injected
	mainSrc, err := generateMain(bctx)
	if err != nil {
		return fmt.Errorf("generating main.go: %w", err)
	}
	if err := os.WriteFile(filepath.Join(buildDir, "main.go"), mainSrc, 0644); err != nil {
		return fmt.Errorf("writing main.go: %w", err)
	}

	return nil
}

// RunBuild executes `go mod tidy && go build` inside buildDir to produce outputBin.
func RunBuild(buildDir, outputBin string) error {
	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir = buildDir
	tidy.Stdout = os.Stdout
	tidy.Stderr = os.Stderr
	tidy.Env = append(os.Environ(), "GOFLAGS=") // clear any -mod flags
	if err := tidy.Run(); err != nil {
		return fmt.Errorf("go mod tidy: %w", err)
	}

	build := exec.Command("go", "build", "-o", outputBin, ".")
	build.Dir = buildDir
	build.Stdout = os.Stdout
	build.Stderr = os.Stderr
	build.Env = append(os.Environ(), "GOFLAGS=")
	if err := build.Run(); err != nil {
		return fmt.Errorf("go build: %w", err)
	}

	return nil
}

// FindYaypiRoot walks up from startDir until it finds a go.mod whose module
// declaration matches "github.com/csullivan/yaypi" (or any module containing "yaypi").
// Returns the directory and module path.
func FindYaypiRoot(startDir string) (root, module, goVersion string, err error) {
	dir := startDir
	for {
		gomodPath := filepath.Join(dir, "go.mod")
		data, readErr := os.ReadFile(gomodPath)
		if readErr == nil {
			mod, ver := parseGoMod(data)
			if strings.Contains(mod, "yaypi") {
				return dir, mod, ver, nil
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", "", "", fmt.Errorf("could not find yaypi module root (no go.mod with 'yaypi' in module path) starting from %s", startDir)
}

// ─── internal helpers ────────────────────────────────────────────────────────

func addHooks(m map[string][]string, entity string, hooks []string) {
	for _, h := range hooks {
		if !contains(m[h], entity) {
			m[h] = append(m[h], entity)
		}
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// sanitizeIdent turns an arbitrary string into a valid Go identifier by replacing
// non-alphanumeric characters with underscores and ensuring it starts with a letter.
func sanitizeIdent(s string) string {
	var b strings.Builder
	for i, r := range s {
		if unicode.IsLetter(r) || (i > 0 && unicode.IsDigit(r)) {
			b.WriteRune(r)
		} else if i == 0 {
			b.WriteRune('p')
		} else {
			b.WriteRune('_')
		}
	}
	return b.String()
}

// parseGoMod extracts the module path and go version from a go.mod file.
func parseGoMod(data []byte) (module, goVersion string) {
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			module = strings.TrimSpace(strings.TrimPrefix(line, "module "))
		}
		if strings.HasPrefix(line, "go ") {
			goVersion = strings.TrimSpace(strings.TrimPrefix(line, "go "))
		}
	}
	return
}

// copyDir recursively copies srcDir to dstDir.
func copyDir(srcDir, dstDir string) error {
	return filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		dst := filepath.Join(dstDir, rel)
		if info.IsDir() {
			return os.MkdirAll(dst, info.Mode())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, data, info.Mode())
	})
}

// generateGoMod returns the content of the go.mod for the generated module.
func generateGoMod(bctx *BuildContext, buildDir string) string {
	var b strings.Builder
	b.WriteString("module yaypi-server\n\n")
	goVer := bctx.GoVersion
	if goVer == "" {
		goVer = "1.21"
	}
	b.WriteString("go " + goVer + "\n\n")
	b.WriteString("require (\n")
	b.WriteString("\t" + bctx.YaypiModule + " v0.0.0\n")
	b.WriteString(")\n\n")
	b.WriteString("replace " + bctx.YaypiModule + " => " + bctx.YaypiRoot + "\n")
	_ = buildDir
	return b.String()
}

// registryTmpl is the template for registry_gen.go.
var registryTmpl = template.Must(template.New("registry").Parse(`// Code generated by "yaypi build". DO NOT EDIT.
package main

import (
	"github.com/rs/zerolog/log"
	yaypiconfig "{{.YaypiModule}}/internal/config"
	yaypiplugin "{{.YaypiModule}}/internal/plugin"
	"{{.YaypiModule}}/pkg/sdk"
{{- range .Plugins}}
	{{.ImportAlias}} "yaypi-server/plugins/{{.PkgName}}"
{{- end}}
)

// initPlugins is called from runServer() after cfg is loaded.
// It creates one instance per plugin and registers it for every entity
// that references the plugin in its hooks.
func initPlugins(d *yaypiplugin.Dispatcher, cfg *yaypiconfig.RootConfig) {
	cfgMap := make(map[string]map[string]any, len(cfg.Plugins))
	for _, pc := range cfg.Plugins {
		cfgMap[pc.Name] = pc.Config
	}

	logger := &sdkLogger{}
{{range $i, $p := .Plugins}}
	// {{$p.Name}}
	inst{{$i}} := {{$p.ImportAlias}}.New(cfgMap["{{$p.Name}}"])
	if err := inst{{$i}}.Init(sdk.InitContext{Config: cfgMap["{{$p.Name}}"], Logger: logger}); err != nil {
		log.Fatal().Err(err).Str("plugin", "{{$p.Name}}").Msg("plugin init failed")
	}
{{- range $p.Entities}}
	d.RegisterHook("{{.}}", inst{{$i}})
{{- end}}
{{end -}}
}

// sdkLogger adapts zerolog to the sdk.Logger interface.
type sdkLogger struct{}

func (l *sdkLogger) Info(msg string, fields ...any) {
	log.Info().Fields(fields).Msg(msg)
}
func (l *sdkLogger) Error(msg string, err error, fields ...any) {
	log.Error().Err(err).Fields(fields).Msg(msg)
}
`))

func generateRegistry(bctx *BuildContext) ([]byte, error) {
	var buf bytes.Buffer
	if err := registryTmpl.Execute(&buf, bctx); err != nil {
		return nil, err
	}
	// gofmt the output
	src, err := format.Source(buf.Bytes())
	if err != nil {
		// Return unformatted but don't fail the build — the compiler will catch real errors
		return buf.Bytes(), nil
	}
	return src, nil
}

// mainTmpl generates main.go for the plugin build: identical to cmd/yaypi/main.go
// except it calls initPlugins(dispatcher, cfg) after the dispatcher is created.
// Rather than copying the actual source file (which would create a maintenance burden),
// we embed the yaypi source path via a replace directive and generate a thin wrapper
// that delegates to the original runServer via a patched copy.
//
// Strategy: copy the actual main.go source from yaypiRoot/cmd/yaypi/main.go and
// inject the initPlugins call at the right place.
func generateMain(bctx *BuildContext) ([]byte, error) {
	srcPath := filepath.Join(bctx.YaypiRoot, "cmd", "yaypi", "main.go")
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", srcPath, err)
	}

	src := string(data)

	// Inject `initPlugins(dispatcher, cfg)` on the line after the dispatcher is created.
	// The sentinel we look for is the exact line in runServer:
	sentinel := "\tdispatcher := plugin.NewDispatcher()"
	injection := sentinel + "\n\tinitPlugins(dispatcher, cfg)"
	if !strings.Contains(src, sentinel) {
		return nil, fmt.Errorf("could not find sentinel %q in main.go — has the file changed?", sentinel)
	}
	src = strings.Replace(src, sentinel, injection, 1)

	return []byte(src), nil
}
