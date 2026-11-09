package config

import (
	"path/filepath"
	"slices"
	"strings"
)

// DocumentTypes defines the file extensions for document files
var DocumentTypes = []string{
	"txt", "md", "html", "xml", "csv", "yaml", "yml",
	"eml", "mbox", "msg",
	"pdf", "doc", "docx", "xls", "xlsx", "ppt", "pptx",
	"odt", "ods", "odp", "rtf",
	"log", "cfg", "conf", "ini", "sh", "bat",
}

// CodeTypes defines the file extensions for programming files
var CodeTypes = []string{
	"js", "ts", "sql", "py", "php", "phtml", "java", "cpp", "c", "json",
	"go", "rs", "rb", "cs", "swift", "kt", "scala", "clj",
	"h", "hpp", "cc", "cxx", "pl", "r", "m", "mm",
	// Delphi / Object Pascal: units, programs, package source, includes,
	// VCL/FireMonkey form definitions, and MSBuild project files.
	"pas", "dpr", "dpk", "inc", "dfm", "fmx", "dproj", "groupproj",
	// Container build files that carry an extension (e.g. app.Dockerfile, web.dockerfile).
	// The extensionless "Dockerfile"/"Containerfile" family is matched by name -- see CodeFilenames.
	"dockerfile",
}

// CodeFilenames matches code files that cannot be safely selected by extension alone.
// Patterns are matched case-insensitively against the file's base name
// (filepath.Match semantics; "*" and "?" only).
var CodeFilenames = []string{
	"Dockerfile",    // exact
	"Dockerfile.*",  // Dockerfile.dev, Dockerfile.prod, ...
	"Containerfile", // Podman equivalent
	".env.example",  // safe environment template
	".env.sample",   // safe environment template
	".env.template", // safe environment template
}

// IsCodeFile reports whether a file should be treated as source code -- by extension
// (CodeTypes) or, for extensionless files like Dockerfiles, by name (CodeFilenames).
// Used both to gate discovery and to pick the code-safe content cleaner.
func IsCodeFile(filename string) bool {
	ext := strings.ToLower(strings.TrimPrefix(getFileExtension(filename), "."))
	if slices.Contains(CodeTypes, ext) {
		return true
	}
	return IsCodeFilename(filename)
}

// IsCodeFilename reports whether the file's base name matches a special code filename in
// CodeFilenames (for example, Dockerfile, Dockerfile.prod, or .env.example).
func IsCodeFilename(filename string) bool {
	// filepath.Base only recognizes the host OS separator. Normalize first so
	// Windows paths passed to a Unix build still match Dockerfile name globs.
	base := strings.ToLower(filepath.Base(strings.ReplaceAll(filename, "\\", "/")))
	for _, pattern := range CodeFilenames {
		if ok, err := filepath.Match(strings.ToLower(pattern), base); err == nil && ok {
			return true
		}
	}
	return false
}

// getFileExtension extracts file extension from filename
func getFileExtension(filename string) string {
	lastDot := strings.LastIndex(filename, ".")
	if lastDot == -1 || lastDot == len(filename)-1 {
		return ""
	}
	return filename[lastDot:]
}

// skipDirs is the set of directory base names pruned during the tree walk. It is
// built once at package load rather than rebuilt on every directory entry (this
// lookup sits on the walk hot path). Note: do NOT blanket-skip all dot-directories;
// .config, .local, etc. must still be traversed.
var skipDirs = map[string]bool{
	// VCS and caches
	".git":   true,
	".svn":   true,
	".hg":    true,
	".cache": true,

	// Language/tool chains and local caches
	".cargo":        true,
	".rustup":       true,
	".npm":          true,
	".yarn":         true,
	".gradle":       true,
	".m2":           true,
	".tox":          true,
	".venv":         true,
	"venv":          true,
	".terraform":    true,
	".terraform.d":  true,
	".pytest_cache": true,
	".mypy_cache":   true,
	"__pycache__":   true,

	// Browsers and large app caches
	".mozilla":  true,
	".chromium": true,

	// IDE/project artifacts
	".vscode":      true,
	".idea":        true,
	"node_modules": true,
	"vendor":       true,
	"target":       true,
	"dist":         true,
	".next":        true,
	".nuxt":        true,

	// Misc
	"coverage":  true,
	"tmp":       true,
	"temp":      true,
	".DS_Store": true,
}

// ShouldSkipDirectory reports whether a directory base name should be skipped
// during traversal. Called once per directory entry on the walk hot path.
func ShouldSkipDirectory(dirName string) bool {
	return skipDirs[dirName]
}

// GetFileTypeDescription returns a human-readable description of file types
func GetFileTypeDescription(includeCode bool) string {
	documents := "documents (txt, md, html, xml, csv, yaml, yml, eml, mbox, msg, pdf, doc, docx, odt, rtf, log, cfg, conf, ini, sh, bat)"
	if includeCode {
		return documents + " + code extensions (" + strings.Join(CodeTypes, ", ") +
			") + code filenames (" + strings.Join(CodeFilenames, ", ") + ")"
	}
	return documents
}

// BuildRipgrepFileTypes creates ripgrep file type arguments
func BuildRipgrepFileTypes(includeCode bool) []string {
	// Use glob patterns for ALL document types
	types := []string{
		// Text files
		"-g", "*.txt", "-g", "*.md", "-g", "*.log", "-g", "*.rtf",
		// HTML/Web files - all variants
		"-g", "*.html", "-g", "*.htm", "-g", "*.xhtml", "-g", "*.shtml",
		// XML/Data files
		"-g", "*.xml", "-g", "*.csv", "-g", "*.yaml", "-g", "*.yml",
		// Config files
		"-g", "*.cfg", "-g", "*.conf", "-g", "*.ini",
		// Email files
		"-g", "*.eml", "-g", "*.mbox", "-g", "*.msg",
		// Office documents
		"-g", "*.pdf", "-g", "*.doc", "-g", "*.docx",
		"-g", "*.odt",
		// Scripts
		"-g", "*.sh", "-g", "*.bat", "-g", "*.cmd",
		// Other text formats
		"-g", "*.tex", "-g", "*.rst", "-g", "*.asciidoc",
	}

	// Add code file patterns if requested
	if includeCode {
		for _, ext := range CodeTypes {
			types = append(types, "-g", "*."+ext)
		}
		for _, name := range CodeFilenames {
			types = append(types, "-g", name)
		}
	}

	return types
}

// DockerfileGlobs returns the -g glob arguments that match the Docker/Podman build-file family:
// the extensionless "Dockerfile"/"Containerfile", the "Dockerfile.*" variants, and the
// extension-bearing "*.dockerfile". Name globs (those not starting with "*.") are matched
// against the file's base name by the discovery walk.
func DockerfileGlobs() []string {
	var globs []string
	for _, ext := range CodeTypes {
		if strings.EqualFold(ext, "dockerfile") {
			globs = append(globs, "-g", "*."+ext)
		}
	}
	for _, name := range CodeFilenames {
		lowerName := strings.ToLower(name)
		if strings.Contains(lowerName, "dockerfile") || lowerName == "containerfile" {
			globs = append(globs, "-g", name)
		}
	}
	return globs
}

// OnlyTypeGlobs returns the -g glob arguments for a single `--only <type>` restriction.
// Most types map to one "*.<ext>" glob; "dockerfile" expands to the full Dockerfile family
// so `--only dockerfile` also catches the extensionless files.
func OnlyTypeGlobs(onlyType string) []string {
	ext := strings.TrimPrefix(strings.ToLower(onlyType), ".")
	if ext == "dockerfile" {
		return DockerfileGlobs()
	}
	return []string{"-g", "*." + ext}
}

// OnlyTypesGlobs returns the ordered union for repeated type restrictions.
func OnlyTypesGlobs(onlyTypes []string) []string {
	globs := make([]string, 0, len(onlyTypes)*2)
	seen := make(map[string]struct{}, len(onlyTypes))
	for _, onlyType := range onlyTypes {
		typeGlobs := OnlyTypeGlobs(onlyType)
		for i := 0; i+1 < len(typeGlobs); i += 2 {
			glob := typeGlobs[i+1]
			key := strings.ToLower(glob)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			globs = append(globs, "-g", glob)
		}
	}
	return globs
}
