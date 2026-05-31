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

// CodeFilenames matches code files that have no usable extension and so can't be caught by
// CodeTypes -- chiefly container build files. Patterns are matched case-insensitively against
// the file's base name (filepath.Match semantics; "*" and "?" only).
var CodeFilenames = []string{
	"Dockerfile",    // exact
	"Dockerfile.*",  // Dockerfile.dev, Dockerfile.prod, ...
	"Containerfile", // Podman equivalent
}

// IsDocumentFile checks if a file extension is a document type
func IsDocumentFile(filename string) bool {
	ext := strings.ToLower(strings.TrimPrefix(getFileExtension(filename), "."))
	if slices.Contains(DocumentTypes, ext) {
		return true
	}
	return false
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

// IsCodeFilename reports whether the file's base name matches one of the special, mostly
// extensionless code filenames in CodeFilenames (e.g. Dockerfile, Dockerfile.prod).
func IsCodeFilename(filename string) bool {
	base := strings.ToLower(filepath.Base(filename))
	for _, pattern := range CodeFilenames {
		if ok, err := filepath.Match(strings.ToLower(pattern), base); err == nil && ok {
			return true
		}
	}
	return false
}

// GetAllSupportedTypes returns all supported file types based on includeCode flag
func GetAllSupportedTypes(includeCode bool) []string {
	types := make([]string, len(DocumentTypes))
	copy(types, DocumentTypes)

	if includeCode {
		types = append(types, CodeTypes...)
	}

	return types
}

// BuildFileTypeMap creates a map for O(1) file type lookups
func BuildFileTypeMap(includeCode bool) map[string]bool {
	typeMap := make(map[string]bool)

	// Add document types
	for _, ext := range DocumentTypes {
		typeMap["."+ext] = true
	}

	// Add code types if requested
	if includeCode {
		for _, ext := range CodeTypes {
			typeMap["."+ext] = true
		}
	}

	return typeMap
}

// GetEstimatedSearchTime returns time estimate based on file count
func GetEstimatedSearchTime(fileCount int) string {
	switch {
	case fileCount < 100:
		return "under 10 seconds"
	case fileCount < 1000:
		return "10-30 seconds"
	case fileCount < 5000:
		return "30 seconds - 2 minutes"
	default:
		return "2-10 minutes (depends on file sizes)"
	}
}

// GetPerformanceProfile returns optimal settings based on file count
func GetPerformanceProfile(fileCount int) (workers int, bufferSize int) {
	switch {
	case fileCount < 100:
		return 2, 50 // Light workload
	case fileCount < 1000:
		return 4, 200 // Medium workload
	case fileCount < 10000:
		return 8, 500 // Heavy workload
	default:
		return 16, 1000 // Very heavy workload
	}
}

// getFileExtension extracts file extension from filename
func getFileExtension(filename string) string {
	lastDot := strings.LastIndex(filename, ".")
	if lastDot == -1 || lastDot == len(filename)-1 {
		return ""
	}
	return filename[lastDot:]
}

// IsHiddenFile checks if a file should be treated as hidden
func IsHiddenFile(filename string) bool {
	return strings.HasPrefix(filename, ".")
}

// ShouldSkipDirectory determines if a directory should be skipped during traversal
func ShouldSkipDirectory(dirName string) bool {
	skipDirs := map[string]bool{
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
		"build":        true,
		"dist":         true,
		".next":        true,
		".nuxt":        true,

		// Misc
		"coverage":  true,
		"tmp":       true,
		"temp":      true,
		".DS_Store": true,
	}

	// Note: do NOT blanket-skip all dot-directories; allow .config, .local, etc.
	return skipDirs[dirName]
}

// GetFileTypeDescription returns a human-readable description of file types
func GetFileTypeDescription(includeCode bool) string {
	if includeCode {
		return "documents (txt, md, html, xml, csv, yaml, yml, eml, mbox, msg, pdf, doc, docx, odt, rtf, log, cfg, conf, ini, sh, bat) + code files (go, js, ts, py, php, phtml, java, cpp, c, json, rs, rb, cs, swift, kt, scala, pas, dpr, dpk, inc, dfm, fmx, dproj, groupproj, Dockerfile)"
	}
	return "documents (txt, md, html, xml, csv, yaml, yml, eml, mbox, msg, pdf, doc, docx, odt, rtf, log, cfg, conf, ini, sh, bat)"
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
		codeGlobs := []string{
			"-g", "*.js", "-g", "*.ts", "-g", "*.sql", "-g", "*.py",
			"-g", "*.php", "-g", "*.phtml", "-g", "*.java", "-g", "*.cpp", "-g", "*.c",
			"-g", "*.json", "-g", "*.go", "-g", "*.rs", "-g", "*.rb",
			"-g", "*.cs", "-g", "*.swift", "-g", "*.kt", "-g", "*.scala",
			// Delphi / Object Pascal
			"-g", "*.pas", "-g", "*.dpr", "-g", "*.dpk", "-g", "*.inc",
			"-g", "*.dfm", "-g", "*.fmx", "-g", "*.dproj", "-g", "*.groupproj",
		}
		types = append(types, codeGlobs...)
		// Container build files, incl. the extensionless Dockerfile/Containerfile family.
		types = append(types, DockerfileGlobs()...)
	}

	return types
}

// DockerfileGlobs returns the -g glob arguments that match the Docker/Podman build-file family:
// the extensionless "Dockerfile"/"Containerfile", the "Dockerfile.*" variants, and the
// extension-bearing "*.dockerfile". Name globs (those not starting with "*.") are matched
// against the file's base name by the discovery walk.
func DockerfileGlobs() []string {
	return []string{"-g", "Dockerfile", "-g", "Dockerfile.*", "-g", "Containerfile", "-g", "*.dockerfile"}
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

// EstimateMemoryUsage provides memory usage estimate based on file count
func EstimateMemoryUsage(fileCount int) string {
	switch {
	case fileCount < 1000:
		return "~50-100 MB"
	case fileCount < 10000:
		return "~100-500 MB"
	case fileCount < 50000:
		return "~500MB-1GB"
	default:
		return "~1-2GB (large dataset)"
	}
}
