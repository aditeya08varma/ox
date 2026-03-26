package comments

import "testing"

func TestFamilyForLanguage_AllMapped(t *testing.T) {
	t.Parallel()

	// every language in familyMap should return a non-nil family
	tests := []struct {
		lang       string
		linePrefix string
		blockOpen  string
	}{
		// C-family
		{"go", "//", "/*"},
		{"c", "//", "/*"},
		{"cpp", "//", "/*"},
		{"java", "//", "/*"},
		{"javascript", "//", "/*"},
		{"typescript", "//", "/*"},
		{"tsx", "//", "/*"},
		{"jsx", "//", "/*"},
		{"rust", "//", "/*"},
		{"swift", "//", "/*"},
		{"kotlin", "//", "/*"},
		{"scala", "//", "/*"},
		{"csharp", "//", "/*"},
		{"dart", "//", "/*"},
		{"php", "//", "/*"},
		{"zig", "//", "/*"},
		{"protobuf", "//", "/*"},
		{"css", "//", "/*"},

		// Hash-family
		{"python", "#", ""},
		{"ruby", "#", ""},
		{"shell", "#", ""},
		{"r", "#", ""},
		{"perl", "#", ""},
		{"elixir", "#", ""},
		{"yaml", "#", ""},
		{"toml", "#", ""},

		// HTML-family
		{"html", "", "<!--"},
		{"xml", "", "<!--"},
		{"markdown", "", "<!--"},

		// Others
		{"sql", "--", "/*"},
		{"lua", "--", "--[["},
		{"haskell", "--", "{-"},
		{"ocaml", "", "(*"},
		{"erlang", "%", ""},
	}

	for _, tt := range tests {
		t.Run(tt.lang, func(t *testing.T) {
			fam := FamilyForLanguage(tt.lang)
			if fam == nil {
				t.Fatalf("FamilyForLanguage(%q) returned nil", tt.lang)
			}
			if fam.LinePrefix != tt.linePrefix {
				t.Errorf("LinePrefix = %q, want %q", fam.LinePrefix, tt.linePrefix)
			}
			if fam.BlockOpen != tt.blockOpen {
				t.Errorf("BlockOpen = %q, want %q", fam.BlockOpen, tt.blockOpen)
			}
		})
	}
}

func TestFamilyForLanguage_Unsupported(t *testing.T) {
	t.Parallel()

	unsupported := []string{"json", "binary", "unknown", "", "YAML"}
	for _, lang := range unsupported {
		t.Run(lang, func(t *testing.T) {
			fam := FamilyForLanguage(lang)
			if fam != nil {
				t.Errorf("FamilyForLanguage(%q) should return nil for unsupported language", lang)
			}
		})
	}
}

func TestSupportedLanguages_Coverage(t *testing.T) {
	t.Parallel()

	langs := SupportedLanguages()
	langSet := make(map[string]bool)
	for _, l := range langs {
		langSet[l] = true
	}

	// verify count matches familyMap size
	if len(langs) != len(familyMap) {
		t.Errorf("SupportedLanguages returned %d, familyMap has %d", len(langs), len(familyMap))
	}

	// spot check uniqueness (no duplicates)
	if len(langSet) != len(langs) {
		t.Error("SupportedLanguages contains duplicates")
	}
}

func TestClassifyComment(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		fam      *Family
		baseKind string
		text     string
		want     string
	}{
		// JavaDoc/JSDoc: /** ... */
		{"javadoc block", &cFamily, "block", "* @param x description", "doc"},
		{"regular block", &cFamily, "block", "regular block comment", "block"},
		{"empty block with star", &cFamily, "block", "* ", "doc"},

		// triple-slash: ///
		{"triple slash doc", &cFamily, "line", "/ doc comment", "doc"},
		{"regular line", &cFamily, "line", "regular comment", "line"},

		// hash doc: ##
		{"double hash doc", &hashFamily, "line", "# module docs", "doc"},
		{"single hash", &hashFamily, "line", "regular comment", "line"},

		// non-matching families
		{"html block not doc", &htmlFamily, "block", "regular html comment", "block"},
		{"sql line not doc", &sqlFamily, "line", "regular sql comment", "line"},

		// empty text with star (edge case for block doc detection)
		{"empty text block", &cFamily, "block", "", "block"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyComment(tt.fam, tt.baseKind, tt.text)
			if got != tt.want {
				t.Errorf("classifyComment = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMatchAt(t *testing.T) {
	t.Parallel()

	runes := []rune("hello world")

	tests := []struct {
		name string
		i    int
		s    string
		want bool
	}{
		{"match at start", 0, "hello", true},
		{"match in middle", 6, "world", true},
		{"no match", 0, "world", false},
		{"empty string", 0, "", false},
		{"past end", 9, "rld!", false},
		{"exact end", 6, "world", true},
		{"single char match", 0, "h", true},
		{"single char no match", 0, "x", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchAt(runes, tt.i, tt.s)
			if got != tt.want {
				t.Errorf("matchAt(runes, %d, %q) = %v, want %v", tt.i, tt.s, got, tt.want)
			}
		})
	}
}
