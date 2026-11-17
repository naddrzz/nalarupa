package text

// The part of fontconfig-parser 0.5 that fontdb uses on Linux: font directories and generic-family
// aliases, read from the same config files in the same order. <match>, <selectfont> and <config> are
// not interpreted (fontconfig-parser rejects some malformed ones and then skips the rest of that file;
// this reader does not).

import (
	"encoding/xml"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// xmlNode is an element or (name "") a text node, like roxmltree's nodes.
type xmlNode struct {
	name     string
	attrs    []xml.Attr
	text     string
	isText   bool
	children []*xmlNode
}

// firstText is roxmltree's `text()`: the element's first child when it is a text node.
func (n *xmlNode) firstText() (string, bool) {
	if len(n.children) > 0 && n.children[0].isText {
		return n.children[0].text, true
	}
	return "", false
}

func (n *xmlNode) attr(name string) (string, bool) {
	for _, a := range n.attrs {
		if a.Name.Local == name {
			return a.Value, true
		}
	}
	return "", false
}

func parseXML(r io.Reader) (*xmlNode, error) {
	d := xml.NewDecoder(r)
	root := &xmlNode{}
	stack := []*xmlNode{root}
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		top := stack[len(stack)-1]
		switch t := tok.(type) {
		case xml.StartElement:
			n := &xmlNode{name: t.Name.Local, attrs: t.Attr}
			top.children = append(top.children, n)
			stack = append(stack, n)
		case xml.EndElement:
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if k := len(top.children); k > 0 && top.children[k-1].isText {
				top.children[k-1].text += string(t)
			} else {
				top.children = append(top.children, &xmlNode{text: string(t), isText: true})
			}
		case xml.Comment:
			top.children = append(top.children, &xmlNode{})
		}
	}
	for _, c := range root.children {
		if !c.isText && c.name != "" {
			return c, nil
		}
	}
	return nil, errors.New("no root element")
}

type fcAlias struct {
	alias                   string
	prefer, accept, defalts []string
}

type fontconfig struct {
	dirs    []string
	aliases []fcAlias
	files   map[string]bool
}

func fcHome() string {
	if h, ok := os.LookupEnv("HOME"); ok {
		return h
	}
	return "/"
}

func expandTilde(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		return filepath.Join(fcHome(), strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/"))
	}
	return p
}

// fcPath resolves a <dir> or <include> path; prefix "" is the element's default.
func fcPath(p, prefix, def, xdgEnv, xdgFallback, configFile string) (string, error) {
	p = expandTilde(p)
	if filepath.IsAbs(p) {
		return p, nil
	}
	if prefix == "" || prefix == "default" {
		prefix = def
	}
	switch prefix {
	case "config":
		return filepath.Join("/etc/fonts", p), nil
	case "cwd":
		return filepath.Join(".", p), nil
	case "relative":
		return filepath.Join(filepath.Dir(configFile), p), nil
	case "xdg":
		x, ok := os.LookupEnv(xdgEnv)
		if !ok {
			x = xdgFallback
		}
		return filepath.Join(expandTilde(x), p), nil
	}
	return "", errors.New("bad prefix")
}

// merge reads one config file (fontconfig-parser `merge_config`); an error stops at the bad element.
func (fc *fontconfig) merge(path string) error {
	canon, err := filepath.EvalSymlinks(path)
	if err == nil {
		canon, err = filepath.Abs(canon)
	}
	if err != nil {
		return err
	}
	if fc.files[canon] {
		return nil
	}
	fc.files[canon] = true
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	root, err := parseXML(f)
	f.Close()
	if err != nil {
		return err
	}
	if root.name != "fontconfig" {
		return errors.New("not a fontconfig file")
	}
	prefixOf := func(n *xmlNode) (string, error) {
		p, _ := n.attr("prefix")
		switch p {
		case "", "default", "cwd", "xdg", "relative":
			return p, nil
		}
		return "", errors.New("bad prefix")
	}
	for _, c := range root.children {
		switch c.name {
		case "description", "cachedir", "remap-dir":
			if _, err := prefixOf(c); err != nil && c.name != "description" {
				return err
			}
			if _, ok := c.firstText(); !ok {
				return errors.New("empty " + c.name)
			}
		case "alias":
			a := fcAlias{}
			for _, k := range c.children {
				var fams []string
				for _, g := range k.children {
					if g.name == "family" {
						if t, ok := g.firstText(); ok {
							fams = append(fams, t)
						}
					}
				}
				switch k.name {
				case "family":
					t, ok := k.firstText()
					if !ok {
						return errors.New("empty family")
					}
					a.alias = t
				case "prefer":
					a.prefer = append(a.prefer, fams...)
				case "accept":
					a.accept = append(a.accept, fams...)
				case "default":
					a.defalts = append(a.defalts, fams...)
				}
			}
			fc.aliases = append(fc.aliases, a)
		case "dir":
			prefix, err := prefixOf(c)
			if err != nil {
				return err
			}
			t, ok := c.firstText()
			if !ok {
				return errors.New("empty dir")
			}
			p, _ := fcPath(t, prefix, "cwd", "XDG_DATA_HOME", "~/.local/share", path)
			fc.dirs = append(fc.dirs, p)
		case "reset-dirs":
			fc.dirs = nil
		case "include":
			prefix, err := prefixOf(c)
			if err != nil {
				return err
			}
			t, ok := c.firstText()
			if !ok {
				return errors.New("empty include")
			}
			p, _ := fcPath(t, prefix, "config", "XDG_CONFIG_HOME", "~/.config", path)
			fc.include(p)
		}
	}
	return nil
}

// include merges a file, or every file of a directory in name order; failures are ignored.
func (fc *fontconfig) include(p string) {
	st, err := os.Stat(p)
	if err != nil {
		return
	}
	if st.Mode().IsRegular() {
		fc.merge(p)
		return
	}
	if !st.IsDir() {
		return
	}
	d, err := os.Open(p)
	if err != nil {
		return
	}
	entries, _ := d.ReadDir(-1)
	d.Close()
	var files []string
	for _, e := range entries {
		if e.Type().IsRegular() || e.Type()&os.ModeSymlink != 0 {
			files = append(files, filepath.Join(p, e.Name()))
		}
	}
	sort.Strings(files)
	for _, f := range files {
		fc.merge(f)
	}
}

// loadFontconfig loads the fontconfig directories and generic families (fontdb `load_fontconfig`);
// false when the config names no directory.
func (db *fontDB) loadFontconfig() bool {
	fc := &fontconfig{files: map[string]bool{}}
	if file, ok := os.LookupEnv("FONTCONFIG_FILE"); ok {
		fc.merge(file)
	} else {
		readGlobal := true
		xdg, ok := os.LookupEnv("XDG_CONFIG_HOME")
		if !ok {
			if home, ok2 := os.LookupEnv("HOME"); ok2 {
				xdg, ok = filepath.Join(home, ".config"), true
			}
		}
		if ok {
			readGlobal = fc.merge(filepath.Join(xdg, "fontconfig/fonts.conf")) != nil
		}
		if readGlobal {
			fc.merge("/etc/fonts/local.conf")
		}
		fc.merge("/etc/fonts/fonts.conf")
	}
	for _, a := range fc.aliases {
		var name string
		switch {
		case len(a.prefer) > 0:
			name = a.prefer[0]
		case len(a.accept) > 0:
			name = a.accept[0]
		case len(a.defalts) > 0:
			name = a.defalts[0]
		default:
			continue
		}
		switch strings.ToLower(a.alias) {
		case "serif":
			db.serif = name
		case "sans-serif", "sans serif":
			db.sansSerif = name
		}
	}
	if len(fc.dirs) == 0 {
		return false
	}
	seen := map[string]bool{}
	for _, d := range fc.dirs {
		db.loadDir(d, seen)
	}
	return true
}
