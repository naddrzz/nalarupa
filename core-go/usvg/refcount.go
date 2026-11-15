package usvg

// Arc strong-count emulation. update_paint_servers uses Arc::get_mut, which succeeds only for an Arc
// with no other strong reference, to update gradients, patterns and masks in place (keeping their id)
// instead of copying them (with a generated id); which branch runs decides ids and output. The cache's
// rc map holds the strong count of each tracked gradient, pattern and mask:
//
//   - After conversion, rcInit counts every reference reachable from the tree, which is all that Rust
//     holds once the cache is cleared. Clip paths and filters are not tracked: they are never updated
//     in place and hold nothing that is.
//   - During conversion (marker context paint in convertPath) the cache, the state and the parent path
//     hold every existing paint server, so only objects processPaint itself creates are unshared:
//     convertPath empties rc first, and anything untracked counts as shared.
//
// Every operation that clones or drops an Arc in Rust adjusts the counts: replacing a paint, cloning a
// pattern root (cloneGroup) and dropping a fill or stroke; an object whose count reaches zero drops
// its contents.

// getMut is Arc::get_mut's condition: x is tracked with a single strong reference.
func (c *converterCache) getMut(x any) bool { return c.rc[x] == 1 }

// trackable is a reference whose count matters: a gradient, pattern or mask (as any, nil otherwise).
func trackable(x any) any {
	switch v := x.(type) {
	case *LinearGradient:
		if v != nil {
			return v
		}
	case *RadialGradient:
		if v != nil {
			return v
		}
	case *Pattern:
		if v != nil {
			return v
		}
	case *Mask:
		if v != nil {
			return v
		}
	}
	return nil
}

// incRef is an Arc clone.
func (c *converterCache) incRef(x any) {
	if k := trackable(x); k != nil {
		if n, ok := c.rc[k]; ok {
			c.rc[k] = n + 1
		}
	}
}

// decRef is an Arc drop; the last one drops the object's contents.
func (c *converterCache) decRef(x any) {
	k := trackable(x)
	if k == nil {
		return
	}
	n, ok := c.rc[k]
	if !ok {
		return
	}
	if n > 1 {
		c.rc[k] = n - 1
		return
	}
	delete(c.rc, k)
	switch v := k.(type) {
	case *Pattern:
		c.groupRefs(&v.root, c.decRef)
	case *Mask:
		c.decRef(v.mask)
		c.groupRefs(&v.root, c.decRef)
	}
}

// replacePaint is `*paint = new`: new starts with one reference and the old paint is dropped.
func (c *converterCache) replacePaint(paint *Paint, replacement Paint) {
	if k := trackable(replacement); k != nil {
		c.rc[k] = 1
	}
	old := *paint
	*paint = replacement
	c.decRef(old)
}

// groupRefs calls f for each Arc g holds directly or through its boxed children (groups, paths, images
// and their trees), but not inside those Arcs: what cloning or dropping the Group touches.
func (c *converterCache) groupRefs(g *Group, f func(any)) {
	if g.mask != nil {
		f(g.mask)
	}
	for _, child := range g.children {
		switch n := child.(type) {
		case *Group:
			c.groupRefs(n, f)
		case *Path:
			if n.fill != nil {
				f(n.fill.paint)
			}
			if n.stroke != nil {
				f(n.stroke.paint)
			}
		case *Image:
			if svg, ok := n.kind.(ImageKindSVG); ok {
				c.treeRefs(svg.Tree, f)
			}
		case *Text:
			c.textRefs(n, f)
		}
	}
}

// textRefs calls f for each Arc a text node holds: the fills and strokes of its chunk spans and their
// decorations, of its laid-out spans and decoration paths, and those under its flattened group.
func (c *converterCache) textRefs(t *Text, f func(any)) {
	style := func(fill *Fill, stroke *Stroke) {
		if fill != nil {
			f(fill.paint)
		}
		if stroke != nil {
			f(stroke.paint)
		}
	}
	for ci := range t.chunks {
		for si := range t.chunks[ci].spans {
			sp := &t.chunks[ci].spans[si]
			style(sp.fill, sp.stroke)
			for _, d := range []*TextDecorationStyle{sp.decoration.underline, sp.decoration.overline, sp.decoration.lineThrough} {
				if d != nil {
					style(d.fill, d.stroke)
				}
			}
		}
	}
	for si := range t.layouted {
		sp := &t.layouted[si]
		style(sp.fill, sp.stroke)
		for _, p := range []*Path{sp.underline, sp.overline, sp.lineThrough} {
			if p != nil {
				style(p.fill, p.stroke)
			}
		}
	}
	if t.flattened != nil {
		c.groupRefs(t.flattened, f)
	}
}

// cloneText is Text::clone: a deep copy of the chunks, laid-out spans and flattened group, sharing the
// Arcs, whose counts go up.
func (c *converterCache) cloneText(t *Text) *Text {
	nt := *t
	cloneFill := func(fill *Fill) *Fill {
		if fill == nil {
			return nil
		}
		x := *fill
		c.incRef(x.paint)
		return &x
	}
	cloneStroke := func(stroke *Stroke) *Stroke {
		if stroke == nil {
			return nil
		}
		x := *stroke
		c.incRef(x.paint)
		return &x
	}
	cloneDeco := func(d *TextDecorationStyle) *TextDecorationStyle {
		if d == nil {
			return nil
		}
		return &TextDecorationStyle{fill: cloneFill(d.fill), stroke: cloneStroke(d.stroke)}
	}
	clonePath := func(p *Path) *Path {
		if p == nil {
			return nil
		}
		x := *p
		x.fill, x.stroke = cloneFill(p.fill), cloneStroke(p.stroke)
		return &x
	}
	nt.chunks = make([]TextChunk, len(t.chunks))
	for ci, ch := range t.chunks {
		ch.spans = append([]TextSpan(nil), ch.spans...)
		for si := range ch.spans {
			sp := &ch.spans[si]
			sp.fill, sp.stroke = cloneFill(sp.fill), cloneStroke(sp.stroke)
			sp.decoration = TextDecoration{
				underline:   cloneDeco(sp.decoration.underline),
				overline:    cloneDeco(sp.decoration.overline),
				lineThrough: cloneDeco(sp.decoration.lineThrough),
			}
		}
		nt.chunks[ci] = ch
	}
	nt.layouted = append([]Span(nil), t.layouted...)
	for si := range nt.layouted {
		sp := &nt.layouted[si]
		sp.fill, sp.stroke = cloneFill(sp.fill), cloneStroke(sp.stroke)
		sp.underline, sp.overline, sp.lineThrough = clonePath(sp.underline), clonePath(sp.overline), clonePath(sp.lineThrough)
	}
	if t.flattened != nil {
		fg := c.cloneGroup(t.flattened)
		nt.flattened = &fg
	}
	return &nt
}

// treeRefs is groupRefs for a whole tree: its root and its collected lists.
func (c *converterCache) treeRefs(t *Tree, f func(any)) {
	c.groupRefs(&t.root, f)
	for _, g := range t.linearGradients {
		f(g)
	}
	for _, g := range t.radialGradients {
		f(g)
	}
	for _, p := range t.patterns {
		f(p)
	}
	for _, m := range t.masks {
		f(m)
	}
}

// rcInit counts the references reachable from tree; the contents of each object are counted once.
func (c *converterCache) rcInit(tree *Tree) {
	clear(c.rc)
	var visit func(x any)
	visit = func(x any) {
		k := trackable(x)
		if k == nil {
			return
		}
		if n, ok := c.rc[k]; ok {
			c.rc[k] = n + 1
			return
		}
		c.rc[k] = 1
		switch v := k.(type) {
		case *Pattern:
			c.groupRefs(&v.root, visit)
		case *Mask:
			visit(v.mask)
			c.groupRefs(&v.root, visit)
		}
	}
	c.groupRefs(&tree.root, visit)
}

// cloneGroup is Group::clone: a deep copy of the boxed children (paths get their own fill and stroke,
// images their own tree) sharing the Arcs, whose counts go up.
func (c *converterCache) cloneGroup(g *Group) Group {
	out := *g
	out.filters = append([]*Filter(nil), g.filters...)
	if g.mask != nil {
		c.incRef(g.mask)
	}
	out.children = make([]Node, len(g.children))
	for i, child := range g.children {
		switch n := child.(type) {
		case *Group:
			ng := c.cloneGroup(n)
			out.children[i] = &ng
		case *Path:
			np := *n
			if n.fill != nil {
				f := *n.fill
				np.fill = &f
				c.incRef(f.paint)
			}
			if n.stroke != nil {
				s := *n.stroke
				np.stroke = &s
				c.incRef(s.paint)
			}
			out.children[i] = &np
		case *Image:
			ni := *n
			if svg, ok := n.kind.(ImageKindSVG); ok {
				ni.kind = ImageKindSVG{Tree: c.cloneTree(svg.Tree)}
			}
			out.children[i] = &ni
		case *Text:
			out.children[i] = c.cloneText(n)
		default:
			out.children[i] = child
		}
	}
	return out
}

// cloneTree is Tree::clone.
func (c *converterCache) cloneTree(t *Tree) *Tree {
	out := *t
	out.root = c.cloneGroup(&t.root)
	out.linearGradients = append([]*LinearGradient(nil), t.linearGradients...)
	out.radialGradients = append([]*RadialGradient(nil), t.radialGradients...)
	out.patterns = append([]*Pattern(nil), t.patterns...)
	out.clipPaths = append([]*ClipPath(nil), t.clipPaths...)
	out.masks = append([]*Mask(nil), t.masks...)
	out.filters = append([]*Filter(nil), t.filters...)
	for _, g := range t.linearGradients {
		c.incRef(g)
	}
	for _, g := range t.radialGradients {
		c.incRef(g)
	}
	for _, p := range t.patterns {
		c.incRef(p)
	}
	for _, m := range t.masks {
		c.incRef(m)
	}
	return &out
}
