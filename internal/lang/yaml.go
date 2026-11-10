package lang

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// decodeYAML reads one document, or a list for a multi-document stream.
// Aliases expand, and every node an alias reaches is charged again, so a
// small file that expands past 8 MiB is refused before it is built. Merge
// keys, binary, and tags outside the core schema are refused. Errors do not
// echo the input.
func decodeYAML(b []byte) (any, error) {
	dec := yaml.NewDecoder(bytes.NewReader(b))
	c := &evalCtx{budget: dataMaxBytes}
	var docs []any
	for {
		var n yaml.Node
		err := dec.Decode(&n)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("yaml syntax")
		}
		d, err := yamlNode(&n, 0, c)
		if err != nil {
			return nil, err
		}
		docs = append(docs, d)
	}
	switch len(docs) {
	case 0:
		return nil, nil
	case 1:
		return docs[0], nil
	}
	return docs, nil
}

func yamlNode(n *yaml.Node, depth int, c *evalCtx) (any, error) {
	if depth > exprDepthMax {
		return nil, fmt.Errorf("yaml too deep")
	}
	if err := charge(c, 16+len(n.Value)); err != nil {
		return nil, fmt.Errorf("yaml too large")
	}
	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) == 0 {
			return nil, nil
		}
		return yamlNode(n.Content[0], depth, c)
	case yaml.AliasNode:
		if n.Alias == nil {
			return nil, fmt.Errorf("yaml alias")
		}
		return yamlNode(n.Alias, depth+1, c)
	case yaml.SequenceNode:
		out := make([]any, 0, len(n.Content))
		for _, e := range n.Content {
			d, err := yamlNode(e, depth+1, c)
			if err != nil {
				return nil, err
			}
			out = append(out, d)
		}
		return out, nil
	case yaml.MappingNode:
		o := newOmap()
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i]
			if k.Kind == yaml.AliasNode && k.Alias != nil {
				k = k.Alias
			}
			if k.Kind != yaml.ScalarNode {
				return nil, fmt.Errorf("yaml key")
			}
			if k.ShortTag() == "!!merge" {
				return nil, fmt.Errorf("yaml merge keys are not supported")
			}
			v, err := yamlNode(n.Content[i+1], depth+1, c)
			if err != nil {
				return nil, err
			}
			o.set(k.Value, v)
		}
		return o, nil
	case yaml.ScalarNode:
		return yamlScalar(n)
	}
	return nil, fmt.Errorf("yaml node")
}

func yamlScalar(n *yaml.Node) (any, error) {
	switch n.ShortTag() {
	case "!!str", "!!timestamp":
		return n.Value, nil
	case "!!null":
		return nil, nil
	case "!!bool":
		var b bool
		if err := n.Decode(&b); err != nil {
			return nil, fmt.Errorf("yaml bool")
		}
		return b, nil
	case "!!int":
		var i int64
		if err := n.Decode(&i); err != nil {
			return nil, fmt.Errorf("yaml int")
		}
		return i, nil
	case "!!float":
		var f float64
		if err := n.Decode(&f); err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
			return nil, fmt.Errorf("yaml float")
		}
		return f, nil
	}
	return nil, fmt.Errorf("yaml tag %s", safeTag(n.ShortTag()))
}

// safeTag keeps a tag printable and short.
func safeTag(t string) string {
	if len(t) > 32 {
		t = t[:32]
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return '?'
		}
		return r
	}, t)
}

// encodeYAML writes a value as YAML in key order.
func encodeYAML(d any) ([]byte, error) {
	n, err := yamlOf(d, 0)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(&yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{n}}); err != nil {
		return nil, fmt.Errorf("yaml encode")
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("yaml encode")
	}
	return b.Bytes(), nil
}

func yamlOf(d any, depth int) (*yaml.Node, error) {
	if depth > exprDepthMax {
		return nil, fmt.Errorf("yaml too deep")
	}
	scalar := func(tag, v string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: v} }
	switch x := d.(type) {
	case nil:
		return scalar("!!null", "null"), nil
	case bool:
		return scalar("!!bool", strconv.FormatBool(x)), nil
	case int64:
		return scalar("!!int", strconv.FormatInt(x, 10)), nil
	case float64:
		return scalar("!!float", formatFloat(x)), nil
	case string:
		return scalar("!!str", x), nil
	case []any:
		n := &yaml.Node{Kind: yaml.SequenceNode}
		for _, e := range x {
			c, err := yamlOf(e, depth+1)
			if err != nil {
				return nil, err
			}
			n.Content = append(n.Content, c)
		}
		return n, nil
	case *omap:
		n := &yaml.Node{Kind: yaml.MappingNode}
		for _, k := range x.keys {
			c, err := yamlOf(x.m[k], depth+1)
			if err != nil {
				return nil, err
			}
			n.Content = append(n.Content, scalar("!!str", k), c)
		}
		return n, nil
	}
	return nil, fmt.Errorf("yaml encode")
}
