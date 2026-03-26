package language

import "testing"

func TestDetect_AllExtensions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		path string
		want string
	}{
		// already covered in detect_test.go but included for completeness
		{"main.rs", "rust"},
		{"lib.py", "python"},
		{"app.js", "javascript"},
		{"app.ts", "typescript"},
		{"app.tsx", "tsx"},
		{"app.jsx", "jsx"},
		{"Main.java", "java"},
		{"main.c", "c"},
		{"header.h", "c"},
		{"main.cpp", "cpp"},
		{"main.cc", "cpp"},
		{"main.cxx", "cpp"},
		{"header.hpp", "cpp"},
		{"header.hxx", "cpp"},
		{"header.hh", "cpp"},
		{"main.go", "go"},
		{"script.rb", "ruby"},
		{"index.php", "php"},
		{"main.swift", "swift"},
		{"main.kt", "kotlin"},
		{"build.kts", "kotlin"},
		{"main.scala", "scala"},
		{"Program.cs", "csharp"},
		{"script.sh", "shell"},
		{"script.bash", "shell"},
		{"query.sql", "sql"},
		{"page.html", "html"},
		{"page.htm", "html"},
		{"style.css", "css"},
		{"data.json", "json"},
		{"config.yaml", "yaml"},
		{"config.yml", "yaml"},
		{"Cargo.toml", "toml"},
		{"pom.xml", "xml"},
		{"README.md", "markdown"},
		{"README.markdown", "markdown"},
		{"analysis.r", "r"},
		{"script.lua", "lua"},
		{"main.zig", "zig"},
		{"app.ex", "elixir"},
		{"test.exs", "elixir"},
		{"server.erl", "erlang"},
		{"header.hrl", "erlang"},
		{"main.hs", "haskell"},
		{"lib.ml", "ocaml"},
		{"lib.mli", "ocaml"},
		{"script.pl", "perl"},
		{"Module.pm", "perl"},
		{"api.proto", "protobuf"},
		{"main.dart", "dart"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := Detect(tt.path)
			if got != tt.want {
				t.Errorf("Detect(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestDetect_CaseInsensitivity(t *testing.T) {
	t.Parallel()

	// extensions are lowercased before matching
	tests := []struct {
		path string
		want string
	}{
		{"main.GO", "go"},
		{"main.PY", "python"},
		{"main.Rs", "rust"},
		{"main.Ts", "typescript"},
		{"main.JAVA", "java"},
		{"main.CPP", "cpp"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := Detect(tt.path)
			if got != tt.want {
				t.Errorf("Detect(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestDetect_UnrecognizedExtensions(t *testing.T) {
	t.Parallel()

	paths := []string{
		"file.xyz",
		"file.docx",
		"file.pdf",
		"file.png",
		"file.lock",
		"file.sum",
		"file.wasm",
	}

	for _, p := range paths {
		t.Run(p, func(t *testing.T) {
			got := Detect(p)
			if got != "" {
				t.Errorf("Detect(%q) = %q, want empty string", p, got)
			}
		})
	}
}

func TestDetect_NoExtension(t *testing.T) {
	t.Parallel()

	paths := []string{
		"Makefile",
		"Dockerfile",
		"LICENSE",
		"README",
		".gitignore",
		".env",
	}

	for _, p := range paths {
		t.Run(p, func(t *testing.T) {
			got := Detect(p)
			if got != "" {
				t.Errorf("Detect(%q) = %q, want empty string for no extension", p, got)
			}
		})
	}
}

func TestDetect_NestedPaths(t *testing.T) {
	t.Parallel()

	tests := []struct {
		path string
		want string
	}{
		{"src/main/java/App.java", "java"},
		{"internal/pkg/handler.go", "go"},
		{"/absolute/path/to/script.py", "python"},
		{"deeply/nested/dir/file.rs", "rust"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := Detect(tt.path)
			if got != tt.want {
				t.Errorf("Detect(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestDetect_EmptyAndEdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path string
		want string
	}{
		{"empty string", "", ""},
		{"dot only", ".", ""},
		{"trailing dot", "file.", ""},
		{"multiple dots takes last", "archive.tar.gz", ""},
		{"double extension recognized", "file.test.go", "go"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Detect(tt.path)
			if got != tt.want {
				t.Errorf("Detect(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}
