package webaccess

import (
	"bytes"
	"errors"
	"net/url"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"
	"golang.org/x/text/encoding"
)

func (page Page) Text() (string, string, error) {
	isHTML := page.Type == "text/html" || page.Type == "application/xhtml+xml"
	if !isHTML && !isText(page.Type) {
		return "", "", errors.New("this media type cannot be read as text")
	}
	codec, _ := charset.Lookup(page.Charset)
	if codec == nil && page.Charset == "" && isHTML {
		detected, _, certain := charset.DetermineEncoding(page.Body, "")
		if certain {
			codec = detected
		}
	}
	content := page.Body
	if codec != nil && codec != encoding.Nop {
		if decoded, err := codec.NewDecoder().Bytes(content); err == nil {
			content = decoded
		}
	}
	if !isHTML {
		return "", strings.ToValidUTF8(string(content), "�"), nil
	}
	doc, err := html.Parse(bytes.NewReader(content))
	if err != nil {
		return "", "", err
	}
	base, _ := url.Parse(page.URL)
	var title string
	var result strings.Builder
	var visit func(*html.Node, int)
	visit = func(node *html.Node, depth int) {
		if depth > 128 || result.Len() >= maxBody {
			return
		}
		if node.Type == html.TextNode {
			words := strings.Join(strings.Fields(node.Data), " ")
			if words != "" {
				result.WriteString(words)
				result.WriteByte(' ')
			}
			return
		}
		if node.Type == html.ElementNode {
			switch node.Data {
			case "title":
				if node.FirstChild != nil && node.FirstChild.Type == html.TextNode {
					title = strings.TrimSpace(node.FirstChild.Data)
				}
				return
			case "script", "style", "noscript", "iframe", "svg", "template":
				return
			case "br", "p", "div", "article", "section", "tr", "ul", "ol", "header", "footer", "blockquote":
				result.WriteByte('\n')
			case "h1", "h2", "h3", "h4", "h5", "h6":
				result.WriteString("\n" + strings.Repeat("#", int(node.Data[1]-'0')) + " ")
			case "li":
				result.WriteString("\n- ")
			case "img":
				if link := linkOf(base, attribute(node, "src")); link != "" {
					result.WriteString("[" + attribute(node, "alt") + "] (" + link + ") ")
				}
				return
			case "pre":
				if node.FirstChild != nil {
					pre := node.FirstChild
					if pre.Data == "code" && pre.FirstChild != nil {
						pre = pre.FirstChild
					}
					if pre.Type == html.TextNode {
						result.WriteString("\n```\n" + pre.Data + "\n```\n")
						return
					}
				}
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			visit(child, depth+1)
		}
		if node.Type == html.ElementNode && node.Data == "a" {
			if link := linkOf(base, attribute(node, "href")); link != "" {
				result.WriteString("(" + link + ") ")
			}
		}
	}
	visit(doc, 0)
	lines := []string{}
	for line := range strings.SplitSeq(result.String(), "\n") {
		line = strings.TrimSpace(line)
		if line != "" || len(lines) > 0 && lines[len(lines)-1] != "" {
			lines = append(lines, line)
		}
	}
	return title, strings.TrimSpace(strings.Join(lines, "\n")), nil
}

func isText(media string) bool {
	if strings.HasPrefix(media, "text/") || strings.HasSuffix(media, "+json") || strings.HasSuffix(media, "+xml") {
		return true
	}
	switch media {
	case "application/json", "application/xml", "application/javascript", "application/ecmascript", "application/x-yaml", "application/yaml", "application/toml", "application/x-sh", "application/csv":
		return true
	}
	return false
}

func attribute(node *html.Node, key string) string {
	for _, attr := range node.Attr {
		if attr.Key == key && len(attr.Val) <= 4096 {
			return attr.Val
		}
	}
	return ""
}

func linkOf(base *url.URL, address string) string {
	if base == nil || address == "" {
		return ""
	}
	link, err := base.Parse(address)
	if err != nil || publicURL(link) != nil {
		return ""
	}
	return link.String()
}
