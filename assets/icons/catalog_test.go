package icons

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"io"
	"io/fs"
	"os"
	"regexp"
	"strings"
	"testing"
)

const galleryMarker = `<script id="icon-data" type="application/json">`

func TestIconCatalog(t *testing.T) {
	data, err := os.ReadFile("catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	var catalog struct {
		Version    int
		ViewBox    string
		Palette    map[string]string
		Categories []struct{ ID, Title, Description string }
		Icons      []struct{ ID, Title string }
		References []struct {
			Product, Scope string
			Tools          []struct{ Group, Name, Icon string }
		}
	}
	if err := json.Unmarshal(data, &catalog); err != nil {
		t.Fatal(err)
	}
	if catalog.Version != 1 || catalog.ViewBox != "0 0 24 24" {
		t.Fatalf("unexpected catalog version %d or viewBox %q", catalog.Version, catalog.ViewBox)
	}
	slug := regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	hex := regexp.MustCompile(`^#[A-Fa-f0-9]{6}$`)
	colors := map[string]bool{"none": true}
	for name, value := range catalog.Palette {
		if !hex.MatchString(value) {
			t.Errorf("invalid color %q for %s", value, name)
		}
		colors[value] = true
	}
	families := make(map[string]int, len(catalog.Categories))
	for _, category := range catalog.Categories {
		if _, duplicate := families[category.ID]; duplicate || !slug.MatchString(category.ID) || category.Title == "" || category.Description == "" {
			t.Errorf("invalid or duplicate family %q", category.ID)
		}
		families[category.ID] = 0
	}
	ids := make(map[string]bool, len(catalog.Icons))
	art := make(map[string]string, len(catalog.Icons))
	for _, icon := range catalog.Icons {
		family, name, ok := strings.Cut(icon.ID, "/")
		_, knownFamily := families[family]
		if !ok || !knownFamily || !slug.MatchString(name) || icon.Title == "" || ids[icon.ID] {
			t.Errorf("invalid or duplicate icon ID %q", icon.ID)
			continue
		}
		ids[icon.ID] = true
		families[family]++
		file, err := os.ReadFile(icon.ID + ".svg")
		if err != nil {
			t.Error(err)
			continue
		}
		var svg struct {
			XMLName xml.Name `xml:"http://www.w3.org/2000/svg svg"`
			Width   string   `xml:"width,attr"`
			Height  string   `xml:"height,attr"`
			ViewBox string   `xml:"viewBox,attr"`
			Title   string   `xml:"title"`
			Body    string   `xml:",innerxml"`
		}
		if err := xml.Unmarshal(file, &svg); err != nil {
			t.Errorf("%s: %v", icon.ID, err)
			continue
		}
		if svg.Width != "24" || svg.Height != "24" || svg.ViewBox != catalog.ViewBox || svg.Title != icon.Title {
			t.Errorf("%s: unexpected dimensions or title %q", icon.ID, svg.Title)
		}
		checkStaticSVG(t, icon.ID, file, colors)
		_, geometry, found := strings.Cut(svg.Body, "</title>")
		if !found || strings.TrimSpace(geometry) == "" {
			t.Errorf("%s: missing title or artwork", icon.ID)
			continue
		}
		signature := strings.Join(strings.Fields(geometry), "")
		if other, duplicate := art[signature]; duplicate {
			t.Errorf("%s repeats the artwork of %s", icon.ID, other)
		}
		art[signature] = icon.ID
	}
	for family, count := range families {
		if count == 0 {
			t.Errorf("family %s is empty", family)
		}
	}
	if err := fs.WalkDir(os.DirFS("."), ".", func(path string, entry fs.DirEntry, err error) error {
		if err == nil && !entry.IsDir() && strings.HasSuffix(path, ".svg") && !ids[strings.TrimSuffix(path, ".svg")] {
			t.Errorf("uncataloged artwork %s", path)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	products := make(map[string]bool)
	for _, reference := range catalog.References {
		if reference.Product == "" || reference.Scope == "" || len(reference.Tools) == 0 || products[reference.Product] {
			t.Errorf("invalid or duplicate reference product %q", reference.Product)
		}
		products[reference.Product] = true
		names := make(map[[2]string]bool)
		for _, tool := range reference.Tools {
			key := [2]string{tool.Group, tool.Name}
			if tool.Group == "" || tool.Name == "" || !ids[tool.Icon] || names[key] {
				t.Errorf("%s: invalid or duplicate reference mapping %q / %q to %q", reference.Product, tool.Group, tool.Name, tool.Icon)
			}
			names[key] = true
		}
	}
}

func checkStaticSVG(t *testing.T, id string, data []byte, colors map[string]bool) {
	t.Helper()
	decoder := xml.NewDecoder(bytes.NewReader(data))
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return
		}
		if err != nil {
			t.Errorf("%s: %v", id, err)
			return
		}
		switch element := token.(type) {
		case xml.Directive, xml.ProcInst:
			t.Errorf("%s: unexpected XML directive", id)
		case xml.StartElement:
			switch element.Name.Local {
			case "svg", "title", "g", "path", "line", "rect", "circle", "ellipse", "polygon", "polyline":
			default:
				t.Errorf("%s: unsupported SVG element %s", id, element.Name.Local)
			}
			if element.Name.Space != "http://www.w3.org/2000/svg" {
				t.Errorf("%s: foreign namespace on %s", id, element.Name.Local)
			}
			for _, attr := range element.Attr {
				switch attr.Name.Local {
				case "fill", "stroke":
					if !colors[attr.Value] {
						t.Errorf("%s: %s uses non-catalog color %q", id, attr.Name.Local, attr.Value)
					}
				case "xmlns", "width", "height", "viewBox", "transform", "opacity",
					"stroke-width", "stroke-linecap", "stroke-linejoin", "stroke-dasharray", "fill-opacity",
					"d", "x", "y", "rx", "ry", "x1", "y1", "x2", "y2", "cx", "cy", "r", "points":
				default:
					t.Errorf("%s: unsupported SVG attribute %s", id, attr.Name.Local)
				}
			}
		}
	}
}

func TestGalleryUsesCatalog(t *testing.T) {
	catalog, err := os.ReadFile("catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	gallery, err := os.ReadFile("gallery.html")
	if err != nil {
		t.Fatal(err)
	}
	_, body, ok := strings.Cut(string(gallery), galleryMarker)
	embedded, _, closed := strings.Cut(body, "</script>")
	normalize := func(s string) string {
		return strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n"))
	}
	want := strings.ReplaceAll(normalize(string(catalog)), "<", `\u003c`)
	if !ok || !closed || normalize(embedded) != want {
		t.Fatal("gallery catalog is out of date; run scripts/sync-icon-gallery.ps1")
	}
}
