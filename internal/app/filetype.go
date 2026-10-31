package app

import (
	"embed"
	"path"
	"strings"

	"github.com/egoist/mygo/ui"
)

// The icons of file types are Material Icon Theme's, in their own colors.
//
//go:embed icons/files/*.svg
var fileIconFiles embed.FS

var fileIcons = map[string]*ui.SVG{}

// fileNameIcons are the icons of files known by their names, lowercase.
var fileNameIcons = map[string]string{
	"go.mod": "go-mod", "go.sum": "go-mod", "go.work": "go-mod",
	"dockerfile": "docker", "docker-compose.yml": "docker", "docker-compose.yaml": "docker", "compose.yml": "docker", "compose.yaml": "docker",
	"makefile": "makefile", "gnumakefile": "makefile",
	".gitignore": "git", ".gitattributes": "git", ".gitmodules": "git", ".gitkeep": "git",
	"package.json": "nodejs", "package-lock.json": "npm", ".npmrc": "npm", "bun.lock": "bun", "bun.lockb": "bun",
	"tsconfig.json": "tsconfig", "jsconfig.json": "tsconfig",
	"license": "license", "license.md": "license", "license.txt": "license", "copying": "license",
	"readme": "readme", "readme.md": "readme", "readme.txt": "readme",
	"changelog": "changelog", "changelog.md": "changelog",
	".editorconfig": "editorconfig", ".babelrc": "babel", "nginx.conf": "nginx",
}

// fileNamePrefixIcons are the icons of files whose names start so.
var fileNamePrefixIcons = []struct{ prefix, icon string }{
	{".env", "tune"}, {"dockerfile", "docker"}, {".eslintrc", "eslint"}, {"eslint.config.", "eslint"},
	{".prettierrc", "prettier"}, {"prettier.config.", "prettier"}, {"vite.config.", "vite"},
	{"tailwind.config.", "tailwindcss"}, {"jest.config.", "jest"}, {"webpack.config.", "webpack"},
	{"babel.config.", "babel"},
}

// fileExtIcons are the icons of files by extension.
var fileExtIcons = map[string]string{
	".go": "go", ".ts": "typescript", ".mts": "typescript", ".cts": "typescript", ".tsx": "react-ts",
	".js": "javascript", ".mjs": "javascript", ".cjs": "javascript", ".jsx": "react",
	".rs": "rust", ".py": "python", ".pyi": "python", ".md": "markdown", ".mdx": "markdown",
	".json": "json", ".jsonc": "json", ".json5": "json", ".yaml": "yaml", ".yml": "yaml", ".toml": "toml",
	".xml": "xml", ".plist": "xml", ".html": "html", ".htm": "html", ".css": "css", ".scss": "sass",
	".sass": "sass", ".less": "less", ".vue": "vue", ".svelte": "svelte", ".swift": "swift",
	".java": "java", ".kt": "kotlin", ".kts": "kotlin", ".scala": "scala", ".gradle": "gradle",
	".c": "c", ".h": "h", ".cc": "cpp", ".cpp": "cpp", ".cxx": "cpp", ".hpp": "hpp", ".hh": "hpp",
	".m": "objective-c", ".mm": "objective-c", ".cs": "csharp", ".rb": "ruby", ".php": "php",
	".dart": "dart", ".ex": "elixir", ".exs": "elixir", ".erl": "erlang", ".hs": "haskell",
	".clj": "clojure", ".r": "r", ".zig": "zig", ".nix": "nix", ".lua": "lua", ".vim": "vim",
	".sh": "console", ".bash": "console", ".zsh": "console", ".fish": "console", ".ps1": "powershell",
	".sql": "database", ".prisma": "prisma", ".graphql": "graphql", ".gql": "graphql", ".proto": "proto",
	".tf": "terraform", ".lock": "lock", ".log": "log", ".txt": "document",
	".ini": "settings", ".cfg": "settings", ".conf": "settings",
	".pem": "certificate", ".crt": "certificate", ".key": "certificate",
	".png": "image", ".jpg": "image", ".jpeg": "image", ".gif": "image", ".webp": "image",
	".ico": "image", ".bmp": "image", ".icns": "image", ".svg": "svg", ".pdf": "pdf",
	".zip": "zip", ".tar": "zip", ".gz": "zip", ".tgz": "zip", ".xz": "zip", ".7z": "zip", ".rar": "zip",
	".mp4": "video", ".mov": "video", ".webm": "video", ".mp3": "audio", ".wav": "audio", ".flac": "audio",
	".ttf": "font", ".otf": "font", ".woff": "font", ".woff2": "font",
}

// fileIconName is the name of the icon of a file, by its name, then its
// extension: "document" for one of no type known.
func fileIconName(p string) string {
	name := strings.ToLower(path.Base(p))
	if icon, ok := fileNameIcons[name]; ok {
		return icon
	}
	for _, f := range fileNamePrefixIcons {
		if strings.HasPrefix(name, f.prefix) {
			return f.icon
		}
	}
	if strings.HasSuffix(name, ".d.ts") {
		return "typescript-def"
	}
	if icon, ok := fileExtIcons[path.Ext(name)]; ok {
		return icon
	}
	return "document"
}

// fileIcon is the icon of a file's type, to show with ui.Image.
func fileIcon(p string) *ui.SVG {
	name := fileIconName(p)
	if svg, ok := fileIcons[name]; ok {
		return svg
	}
	data, err := fileIconFiles.ReadFile("icons/files/" + name + ".svg")
	if err != nil {
		panic("no file icon " + name)
	}
	svg := ui.MustParseSVG(data)
	fileIcons[name] = svg
	return svg
}
