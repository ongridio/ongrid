// This file is forked from https://github.com/mattn/docx2md.
// Compared with upstream, it fixes heading-level detection and table-header
// rendering, and adds zero-disk image handling and XML decompression limits
// for uploaded documents.
package docextract

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"path"
	"strconv"
	"strings"

	"github.com/mattn/go-runewidth"
)

// Relationship is
type Relationship struct {
	Text       string `xml:",chardata"`
	ID         string `xml:"Id,attr"`
	Type       string `xml:"Type,attr"`
	Target     string `xml:"Target,attr"`
	TargetMode string `xml:"TargetMode,attr"`
}

// Relationships is
type Relationships struct {
	XMLName      xml.Name       `xml:"Relationships"`
	Text         string         `xml:",chardata"`
	Xmlns        string         `xml:"xmlns,attr"`
	Relationship []Relationship `xml:"Relationship"`
}

// TextVal is
type TextVal struct {
	Text string `xml:",chardata"`
	Val  string `xml:"val,attr"`
}

// NumberingLvl is
type NumberingLvl struct {
	Text      string  `xml:",chardata"`
	Ilvl      string  `xml:"ilvl,attr"`
	Tplc      string  `xml:"tplc,attr"`
	Tentative string  `xml:"tentative,attr"`
	Start     TextVal `xml:"start"`
	NumFmt    TextVal `xml:"numFmt"`
	LvlText   TextVal `xml:"lvlText"`
	LvlJc     TextVal `xml:"lvlJc"`
	PPr       struct {
		Text string `xml:",chardata"`
		Ind  struct {
			Text    string `xml:",chardata"`
			Left    string `xml:"left,attr"`
			Hanging string `xml:"hanging,attr"`
		} `xml:"ind"`
	} `xml:"pPr"`
	RPr struct {
		Text string `xml:",chardata"`
		U    struct {
			Text string `xml:",chardata"`
			Val  string `xml:"val,attr"`
		} `xml:"u"`
		RFonts struct {
			Text string `xml:",chardata"`
			Hint string `xml:"hint,attr"`
		} `xml:"rFonts"`
	} `xml:"rPr"`
}

// Numbering is
type Numbering struct {
	XMLName     xml.Name `xml:"numbering"`
	Text        string   `xml:",chardata"`
	Wpc         string   `xml:"wpc,attr"`
	Cx          string   `xml:"cx,attr"`
	Cx1         string   `xml:"cx1,attr"`
	Mc          string   `xml:"mc,attr"`
	O           string   `xml:"o,attr"`
	R           string   `xml:"r,attr"`
	M           string   `xml:"m,attr"`
	V           string   `xml:"v,attr"`
	Wp14        string   `xml:"wp14,attr"`
	Wp          string   `xml:"wp,attr"`
	W10         string   `xml:"w10,attr"`
	W           string   `xml:"w,attr"`
	W14         string   `xml:"w14,attr"`
	W15         string   `xml:"w15,attr"`
	W16se       string   `xml:"w16se,attr"`
	Wpg         string   `xml:"wpg,attr"`
	Wpi         string   `xml:"wpi,attr"`
	Wne         string   `xml:"wne,attr"`
	Wps         string   `xml:"wps,attr"`
	Ignorable   string   `xml:"Ignorable,attr"`
	AbstractNum []struct {
		Text                       string         `xml:",chardata"`
		AbstractNumID              string         `xml:"abstractNumId,attr"`
		RestartNumberingAfterBreak string         `xml:"restartNumberingAfterBreak,attr"`
		Nsid                       TextVal        `xml:"nsid"`
		MultiLevelType             TextVal        `xml:"multiLevelType"`
		Tmpl                       TextVal        `xml:"tmpl"`
		Lvl                        []NumberingLvl `xml:"lvl"`
	} `xml:"abstractNum"`
	Num []struct {
		Text          string  `xml:",chardata"`
		NumID         string  `xml:"numId,attr"`
		AbstractNumID TextVal `xml:"abstractNumId"`
	} `xml:"num"`
}

// Styles holds paragraph outline levels keyed by Word style ID.
type Styles struct {
	Style []struct {
		Type    string `xml:"type,attr"`
		StyleID string `xml:"styleId,attr"`
		PPr     struct {
			OutlineLvl TextVal `xml:"outlineLvl"`
		} `xml:"pPr"`
	} `xml:"style"`
}

// Config holds conversion options.
type Config struct {
	HTMLTable bool
}

// maxDOCXXMLBytes bounds each XML part after ZIP decompression. The upload
// limit only bounds compressed bytes, so it does not protect against ZIP bombs.
const maxDOCXXMLBytes uint64 = 100 << 20

type file struct {
	rels   Relationships
	num    Numbering
	styles map[string]int
	cfg    Config
	list   map[string]int
}

// Node is
type Node struct {
	XMLName xml.Name
	Attrs   []xml.Attr `xml:"-"`
	Content string     `xml:",chardata"`
	Nodes   []Node     `xml:",any"`
}

// UnmarshalXML is
func (n *Node) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	n.XMLName = start.Name
	n.Attrs = start.Attr
	var content []byte
	for {
		tok, err := d.Token()
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			var child Node
			if err := child.UnmarshalXML(d, t); err != nil {
				return err
			}
			n.Nodes = append(n.Nodes, child)
		case xml.CharData:
			content = append(content, t...)
		case xml.EndElement:
			n.Content = string(content)
			return nil
		}
	}
}

func escape(s, set string) string {
	if !strings.ContainsAny(s, set) {
		return s
	}
	var sb strings.Builder
	sb.Grow(len(s) + 8)
	for _, r := range s {
		if strings.ContainsRune(set, r) {
			sb.WriteByte('\\')
		}
		sb.WriteRune(r)
	}
	return sb.String()
}

func onOff(attrs []xml.Attr) bool {
	val, ok := attr(attrs, "val")
	if !ok {
		return true
	}
	switch val {
	case "0", "false", "none", "off":
		return false
	}
	return true
}

func attr(attrs []xml.Attr, name string) (string, bool) {
	for _, attr := range attrs {
		if attr.Name.Local == name {
			return attr.Value, true
		}
	}
	return "", false
}

func (zf *file) walk(node *Node, w io.Writer) error {
	switch node.XMLName.Local {
	case "hyperlink":
		var cbuf bytes.Buffer
		for i := range node.Nodes {
			if err := zf.walk(&node.Nodes[i], &cbuf); err != nil {
				return err
			}
		}
		target := ""
		if id, ok := attr(node.Attrs, "id"); ok {
			for _, rel := range zf.rels.Relationship {
				if id == rel.ID {
					target = rel.Target
					break
				}
			}
		} else if anchor, ok := attr(node.Attrs, "anchor"); ok {
			target = "#" + anchor
		}
		if target == "" {
			fmt.Fprint(w, cbuf.String())
		} else {
			fmt.Fprintf(w, "[%s](%s)",
				escape(cbuf.String(), "[]"), escape(target, "()"))
		}
	case "t":
		fmt.Fprint(w, node.Content)
	case "br", "cr":
		fmt.Fprint(w, "\n")
	case "tab":
		fmt.Fprint(w, "\t")
	case "tabs":
		// tab stop definitions in paragraph properties, not content
	case "pPr":
		code := false
		hasNumPr := false
		for _, n := range node.Nodes {
			if n.XMLName.Local == "numPr" {
				hasNumPr = true
				break
			}
		}
		for _, n := range node.Nodes {
			switch n.XMLName.Local {
			case "ind":
				if !hasNumPr {
					if left, ok := attr(n.Attrs, "left"); ok {
						if i, err := strconv.Atoi(left); err == nil && i > 0 {
							fmt.Fprint(w, strings.Repeat("  ", i/360))
						}
					}
				}
			case "pStyle":
				if val, ok := attr(n.Attrs, "val"); ok {
					if level, ok := zf.styles[val]; ok {
						fmt.Fprint(w, "\n"+strings.Repeat("#", level)+" ")
					} else if strings.HasPrefix(val, "Heading") {
						if i, err := strconv.Atoi(val[7:]); err == nil && i > 0 {
							fmt.Fprint(w, "\n"+strings.Repeat("#", i)+" ")
						}
					} else if val == "Code" {
						code = true
					} else {
						if i, err := strconv.Atoi(val); err == nil && i > 0 {
							fmt.Fprint(w, "\n"+strings.Repeat("#", i)+" ")
						}
					}
				}
			case "numPr":
				numID := ""
				ilvl := ""
				ilvlNum := 0
				numFmt := ""
				start := 1
				for _, nn := range n.Nodes {
					if nn.XMLName.Local == "numId" {
						if val, ok := attr(nn.Attrs, "val"); ok {
							numID = val
						}
					}
					if nn.XMLName.Local == "ilvl" {
						if val, ok := attr(nn.Attrs, "val"); ok {
							ilvl = val
							if i, err := strconv.Atoi(val); err == nil {
								ilvlNum = i
							}
						}
					}
				}
				for _, num := range zf.num.Num {
					if numID != num.NumID {
						continue
					}
					for _, abnum := range zf.num.AbstractNum {
						if abnum.AbstractNumID != num.AbstractNumID.Val {
							continue
						}
						for _, ablvl := range abnum.Lvl {
							if ablvl.Ilvl != ilvl {
								continue
							}
							if i, err := strconv.Atoi(ablvl.Start.Val); err == nil {
								start = i
							}
							numFmt = ablvl.NumFmt.Val
							break
						}
						break
					}
					break
				}

				key := fmt.Sprintf("%s:%d", numID, ilvlNum)
				if _, ok := zf.list[key]; !ok {
					fmt.Fprint(w, "\n")
				}
				fmt.Fprint(w, strings.Repeat("  ", ilvlNum))
				switch numFmt {
				case "decimal", "aiueoFullWidth":
					cur, ok := zf.list[key]
					if !ok {
						zf.list[key] = start
					} else {
						zf.list[key] = cur + 1
					}
					fmt.Fprintf(w, "%d. ", zf.list[key])
				case "bullet":
					if _, ok := zf.list[key]; !ok {
						zf.list[key] = 1
					}
					fmt.Fprint(w, "* ")
				}
			}
		}
		if code {
			fmt.Fprint(w, "`")
		}
		for i := range node.Nodes {
			if err := zf.walk(&node.Nodes[i], w); err != nil {
				return err
			}
		}
		if code {
			fmt.Fprint(w, "`")
		}
	case "tbl":
		fmt.Fprint(w, "\n")

		type cellInfo struct {
			content  string
			gridSpan int
			vMerge   string // "restart", "continue", or ""
		}

		var cellRows [][]cellInfo
		for _, tr := range node.Nodes {
			if tr.XMLName.Local != "tr" {
				continue
			}
			var cells []cellInfo
			for _, tc := range tr.Nodes {
				if tc.XMLName.Local != "tc" {
					continue
				}
				ci := cellInfo{gridSpan: 1}
				for _, n := range tc.Nodes {
					if n.XMLName.Local != "tcPr" {
						continue
					}
					for _, nn := range n.Nodes {
						switch nn.XMLName.Local {
						case "gridSpan":
							if val, ok := attr(nn.Attrs, "val"); ok {
								if v, err := strconv.Atoi(val); err == nil {
									ci.gridSpan = v
								}
							}
						case "vMerge":
							if val, ok := attr(nn.Attrs, "val"); ok {
								ci.vMerge = val
							} else {
								ci.vMerge = "continue"
							}
						}
					}
				}
				var cbuf bytes.Buffer
				if err := zf.walk(&tc, &cbuf); err != nil {
					return err
				}
				ci.content = strings.Replace(cbuf.String(), "\n", "", -1)
				cells = append(cells, ci)
			}
			cellRows = append(cellRows, cells)
		}

		// Check if table has any merged cells
		hasMerge := false
		for _, cells := range cellRows {
			for _, ci := range cells {
				if ci.gridSpan > 1 || ci.vMerge != "" {
					hasMerge = true
					break
				}
			}
			if hasMerge {
				break
			}
		}

		if hasMerge && zf.cfg.HTMLTable {
			// Calculate rowspan for vMerge cells
			type htmlCell struct {
				content string
				colspan int
				rowspan int
				gridCol int
				skip    bool
			}
			htmlRows := make([][]htmlCell, len(cellRows))
			for i, cells := range cellRows {
				htmlRows[i] = make([]htmlCell, len(cells))
				col := 0
				for j, ci := range cells {
					htmlRows[i][j] = htmlCell{
						content: ci.content,
						colspan: ci.gridSpan,
						rowspan: 1,
						gridCol: col,
					}
					if ci.vMerge == "continue" {
						htmlRows[i][j].skip = true
						// Find the restart cell above in the same grid
						// column and increment its rowspan
					search:
						for k := i - 1; k >= 0; k-- {
							for m := range htmlRows[k] {
								if htmlRows[k][m].gridCol == col {
									if !htmlRows[k][m].skip {
										htmlRows[k][m].rowspan++
										break search
									}
									break
								}
							}
						}
					}
					col += ci.gridSpan
				}
			}

			fmt.Fprint(w, "<table>\n")
			for i, row := range htmlRows {
				fmt.Fprint(w, "  <tr>\n")
				tag := "td"
				if i == 0 {
					tag = "th"
				}
				for _, cell := range row {
					if cell.skip {
						continue
					}
					fmt.Fprintf(w, "    <%s", tag)
					if cell.colspan > 1 {
						fmt.Fprintf(w, " colspan=\"%d\"", cell.colspan)
					}
					if cell.rowspan > 1 {
						fmt.Fprintf(w, " rowspan=\"%d\"", cell.rowspan)
					}
					fmt.Fprintf(w, ">%s</%s>\n", html.EscapeString(cell.content), tag)
				}
				fmt.Fprint(w, "  </tr>\n")
			}
			fmt.Fprint(w, "</table>\n")
		} else {
			// Plain markdown table (no merged cells)
			var rows [][]string
			for _, cells := range cellRows {
				var cols []string
				for _, ci := range cells {
					cols = append(cols, escape(ci.content, "|"))
				}
				rows = append(rows, cols)
			}
			maxcol := 0
			for _, cols := range rows {
				if len(cols) > maxcol {
					maxcol = len(cols)
				}
			}
			widths := make([]int, maxcol)
			for _, row := range rows {
				for i := 0; i < maxcol; i++ {
					if i < len(row) {
						width := runewidth.StringWidth(row[i])
						if widths[i] < width {
							widths[i] = width
						}
					}
				}
			}
			for i, row := range rows {
				for j := 0; j < maxcol; j++ {
					fmt.Fprint(w, "|")
					if j < len(row) {
						width := runewidth.StringWidth(row[j])
						fmt.Fprint(w, row[j])
						fmt.Fprint(w, strings.Repeat(" ", widths[j]-width))
					} else {
						fmt.Fprint(w, strings.Repeat(" ", widths[j]))
					}
				}
				fmt.Fprint(w, "|\n")
				if i == 0 {
					for j := 0; j < maxcol; j++ {
						fmt.Fprint(w, "|")
						fmt.Fprint(w, strings.Repeat("-", widths[j]))
					}
					fmt.Fprint(w, "|\n")
				}
			}
		}
		fmt.Fprint(w, "\n")
	case "r":
		bold := false
		italic := false
		strike := false
		for _, n := range node.Nodes {
			if n.XMLName.Local != "rPr" {
				continue
			}
			for _, nn := range n.Nodes {
				switch nn.XMLName.Local {
				case "b":
					bold = onOff(nn.Attrs)
				case "i":
					italic = onOff(nn.Attrs)
				case "strike":
					strike = onOff(nn.Attrs)
				}
			}
		}
		var cbuf bytes.Buffer
		for i := range node.Nodes {
			if err := zf.walk(&node.Nodes[i], &cbuf); err != nil {
				return err
			}
		}
		content := escape(cbuf.String(), `*~\`)
		if content == "" {
			break
		}
		if strike {
			fmt.Fprint(w, "~~")
		}
		if bold {
			fmt.Fprint(w, "**")
		}
		if italic {
			fmt.Fprint(w, "*")
		}
		fmt.Fprint(w, content)
		if italic {
			fmt.Fprint(w, "*")
		}
		if bold {
			fmt.Fprint(w, "**")
		}
		if strike {
			fmt.Fprint(w, "~~")
		}
	case "p":
		var pbuf bytes.Buffer
		for i := range node.Nodes {
			if err := zf.walk(&node.Nodes[i], &pbuf); err != nil {
				return err
			}
		}
		content := pbuf.String()
		if strings.TrimSpace(content) != "" {
			fmt.Fprint(w, content)
			fmt.Fprintln(w)
		}
	case "blip":
		// Images are intentionally omitted. Uploaded documents are converted to
		// text in memory; extracting media would write untrusted artifacts to the
		// process working directory and is not useful for knowledge chunking.
	case "Fallback":
	case "txbxContent":
		var cbuf bytes.Buffer
		for i := range node.Nodes {
			if err := zf.walk(&node.Nodes[i], &cbuf); err != nil {
				return err
			}
		}
		fmt.Fprintln(w, "\n```\n"+cbuf.String()+"```")
	default:
		for i := range node.Nodes {
			if err := zf.walk(&node.Nodes[i], w); err != nil {
				return err
			}
		}
	}

	return nil
}

func decodeXMLFile(f *zip.File, dst interface{}) error {
	if f.UncompressedSize64 > maxDOCXXMLBytes {
		return fmt.Errorf("DOCX XML part %q exceeds %d-byte limit", f.Name, maxDOCXXMLBytes)
	}

	rc, err := f.Open()
	if err != nil {
		return fmt.Errorf("open DOCX XML part %q: %w", f.Name, err)
	}
	defer rc.Close()

	limited := &io.LimitedReader{R: rc, N: int64(maxDOCXXMLBytes) + 1}
	if err := xml.NewDecoder(limited).Decode(dst); err != nil {
		if limited.N == 0 {
			return fmt.Errorf("DOCX XML part %q exceeds %d-byte limit", f.Name, maxDOCXXMLBytes)
		}
		return fmt.Errorf("decode DOCX XML part %q: %w", f.Name, err)
	}
	return nil
}

func findFile(files []*zip.File, target string) *zip.File {
	for _, f := range files {
		if ok, _ := path.Match(target, f.Name); ok {
			return f
		}
	}
	return nil
}

func docx2md(data []byte) (string, error) {
	cfg := Config{
		HTMLTable: false,
	}

	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", err
	}

	var rels Relationships
	var num Numbering
	var styles Styles

	for _, f := range r.File {
		switch f.Name {
		case "word/_rels/document.xml.rels", "word/_rels/document2.xml.rels":
			if err := decodeXMLFile(f, &rels); err != nil {
				return "", err
			}
		case "word/numbering.xml":
			if err := decodeXMLFile(f, &num); err != nil {
				return "", err
			}
		case "word/styles.xml":
			if err := decodeXMLFile(f, &styles); err != nil {
				return "", err
			}
		}
	}

	f := findFile(r.File, "word/document*.xml")
	if f == nil {
		return "", errors.New("incorrect document")
	}
	var node Node
	if err := decodeXMLFile(f, &node); err != nil {
		return "", err
	}

	var buf bytes.Buffer
	headingLevels := make(map[string]int)
	for _, style := range styles.Style {
		if style.Type != "paragraph" {
			continue
		}
		if level, err := strconv.Atoi(style.PPr.OutlineLvl.Val); err == nil && level >= 0 {
			headingLevels[style.StyleID] = level + 1
		}
	}

	zf := &file{
		rels:   rels,
		num:    num,
		styles: headingLevels,
		cfg:    cfg,
		list:   make(map[string]int),
	}
	err = zf.walk(&node, &buf)
	if err != nil {
		return "", err
	}

	return buf.String(), nil
}
