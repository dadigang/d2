// Package d2ir implements a tree data structure to keep track of the resolved value of D2
// keys.
package d2ir

import (
	"errors"
	"fmt"
	"strings"

	"oss.terrastruct.com/util-go/go2"

	"oss.terrastruct.com/d2/d2ast"
	"oss.terrastruct.com/d2/d2format"
	"oss.terrastruct.com/d2/d2graph"
	"oss.terrastruct.com/d2/d2parser"
)

// Most errors returned by a node should be created with d2parser.Errorf
// to indicate the offending AST node.
type Node interface {
	node()
	Copy(newParent Node) Node
	Parent() Node
	Primary() *Scalar
	Map() *Map
	Equal(n2 Node) bool

	AST() d2ast.Node
	fmt.Stringer

	LastRef() Reference
	LastPrimaryKey() *d2ast.Key
}

var _ Node = &Scalar{}
var _ Node = &Field{}
var _ Node = &Edge{}
var _ Node = &Array{}
var _ Node = &Map{}

type Value interface {
	Node
	value()
}

var _ Value = &Scalar{}
var _ Value = &Array{}
var _ Value = &Map{}

type Composite interface {
	Node
	Value
	composite()
}

var _ Composite = &Array{}
var _ Composite = &Map{}

func (n *Scalar) node() {}
func (n *Field) node()  {}
func (n *Edge) node()   {}
func (n *Array) node()  {}
func (n *Map) node()    {}

func (n *Scalar) Parent() Node { return n.parent }
func (n *Field) Parent() Node  { return n.parent }
func (n *Edge) Parent() Node   { return n.parent }
func (n *Array) Parent() Node  { return n.parent }
func (n *Map) Parent() Node    { return n.parent }

func (n *Scalar) Primary() *Scalar { return n }
func (n *Field) Primary() *Scalar  { return n.Primary_ }
func (n *Edge) Primary() *Scalar   { return n.Primary_ }
func (n *Array) Primary() *Scalar  { return nil }
func (n *Map) Primary() *Scalar    { return nil }

func (n *Scalar) Map() *Map { return nil }
func (n *Field) Map() *Map {
	if n == nil {
		return nil
	}
	if n.Composite == nil {
		return nil
	}
	return n.Composite.Map()
}
func (n *Edge) Map() *Map {
	if n == nil {
		return nil
	}
	return n.Map_
}
func (n *Array) Map() *Map { return nil }
func (n *Map) Map() *Map   { return n }

func (n *Scalar) value() {}
func (n *Array) value()  {}
func (n *Map) value()    {}

func (n *Array) composite() {}
func (n *Map) composite()   {}

func (n *Scalar) String() string { return d2format.Format(n.AST()) }
func (n *Field) String() string  { return d2format.Format(n.AST()) }
func (n *Edge) String() string   { return d2format.Format(n.AST()) }
func (n *Array) String() string  { return d2format.Format(n.AST()) }
func (n *Map) String() string    { return d2format.Format(n.AST()) }

func (n *Scalar) LastRef() Reference { return parentRef(n) }
func (n *Map) LastRef() Reference    { return parentRef(n) }
func (n *Array) LastRef() Reference  { return parentRef(n) }

func (n *Scalar) LastPrimaryKey() *d2ast.Key { return parentPrimaryKey(n) }
func (n *Map) LastPrimaryKey() *d2ast.Key    { return parentPrimaryKey(n) }
func (n *Array) LastPrimaryKey() *d2ast.Key  { return parentPrimaryKey(n) }

type Reference interface {
	reference()
	// Most specific AST node for the reference.
	AST() d2ast.Node
	Primary() bool
}

var _ Reference = &FieldReference{}
var _ Reference = &EdgeReference{}

func (r *FieldReference) reference() {}
func (r *EdgeReference) reference()  {}

type Scalar struct {
	parent Node
	Value  d2ast.Scalar `json:"value"`
}

func (s *Scalar) Copy(newParent Node) Node {
	tmp := *s
	s = &tmp

	s.parent = newParent
	return s
}

func (s *Scalar) Equal(n2 Node) bool {
	s2 := n2.(*Scalar)
	if _, ok := s.Value.(d2ast.String); ok {
		if _, ok = s2.Value.(d2ast.String); ok {
			return s.Value.ScalarString() == s2.Value.ScalarString()
		}
	}
	return s.Value.Type() == s2.Value.Type() && s.Value.ScalarString() == s2.Value.ScalarString()
}

type Map struct {
	parent Node
	Fields []*Field `json:"fields"`
	Edges  []*Edge  `json:"edges"`
}

func (m *Map) initRoot() {
	m.parent = &Field{
		Name: "root",
		References: []*FieldReference{{
			Context: &RefContext{
				ScopeMap: m,
			},
		}},
	}
}

func (m *Map) Copy(newParent Node) Node {
	tmp := *m
	m = &tmp

	m.parent = newParent
	pfields := m.Fields
	m.Fields = make([]*Field, 0, len(pfields))
	for _, f := range pfields {
		m.Fields = append(m.Fields, f.Copy(m).(*Field))
	}
	m.Edges = append([]*Edge(nil), m.Edges...)
	for i := range m.Edges {
		m.Edges[i] = m.Edges[i].Copy(m).(*Edge)
	}
	if m.parent == nil {
		m.initRoot()
	}
	return m
}

// CopyBase copies the map m without layers/scenarios/steps.
func (m *Map) CopyBase(newParent Node) *Map {
	if m == nil {
		return (&Map{}).Copy(newParent).(*Map)
	}

	layers := m.DeleteField("layers")
	scenarios := m.DeleteField("scenarios")
	steps := m.DeleteField("steps")

	m2 := m.Copy(newParent).(*Map)
	if layers != nil {
		m.Fields = append(m.Fields, layers)
	}
	if scenarios != nil {
		m.Fields = append(m.Fields, scenarios)
	}
	if steps != nil {
		m.Fields = append(m.Fields, steps)
	}
	return m2
}

// Root reports whether the Map is the root of the D2 tree.
func (m *Map) Root() bool {
	// m.parent exists even on the root map as we store the root AST in
	// m.parent.References[0].Context.Map for reporting error messages about the whole IR.
	// Or if otherwise needed.
	f, ok := m.parent.(*Field)
	if !ok {
		return false
	}
	return f.Root()
}

func (f *Field) Root() bool {
	return f.parent == nil
}

type BoardKind string

const (
	BoardLayer    BoardKind = "layer"
	BoardScenario BoardKind = "scenario"
	BoardStep     BoardKind = "step"
)

// NodeBoardKind reports whether n represents the root of a board.
// n should be *Field or *Map
func NodeBoardKind(n Node) BoardKind {
	var f *Field
	switch n := n.(type) {
	case *Field:
		if n.parent == nil {
			return BoardLayer
		}
		f = ParentField(n)
	case *Map:
		f = ParentField(n)
		if f.Root() {
			return BoardLayer
		}
		f = ParentField(f)
	}
	if f == nil {
		return ""
	}
	switch f.Name {
	case "layers":
		return BoardLayer
	case "scenarios":
		return BoardScenario
	case "steps":
		return BoardStep
	default:
		return ""
	}
}

type Field struct {
	// *Map.
	parent Node

	Name string `json:"name"`

	// Primary_ to avoid clashing with Primary(). We need to keep it exported for
	// encoding/json to marshal it so cannot prefix _ instead.
	Primary_  *Scalar   `json:"primary,omitempty"`
	Composite Composite `json:"composite,omitempty"`

	References []*FieldReference `json:"references,omitempty"`
}

func (f *Field) Copy(newParent Node) Node {
	tmp := *f
	f = &tmp

	f.parent = newParent
	f.References = append([]*FieldReference(nil), f.References...)
	if f.Primary_ != nil {
		f.Primary_ = f.Primary_.Copy(f).(*Scalar)
	}
	if f.Composite != nil {
		f.Composite = f.Composite.Copy(f).(Composite)
	}
	return f
}

func (f *Field) lastPrimaryRef() *FieldReference {
	for i := len(f.References) - 1; i >= 0; i-- {
		if f.References[i].Primary() {
			return f.References[i]
		}
	}
	return nil
}

func (f *Field) LastPrimaryKey() *d2ast.Key {
	fr := f.lastPrimaryRef()
	if fr == nil {
		return nil
	}
	return fr.Context.Key
}

func (f *Field) LastRef() Reference {
	return f.References[len(f.References)-1]
}

type EdgeID struct {
	SrcPath  []string `json:"src_path"`
	SrcArrow bool     `json:"src_arrow"`

	DstPath  []string `json:"dst_path"`
	DstArrow bool     `json:"dst_arrow"`

	// If nil, then any EdgeID with equal src/dst/arrows matches.
	Index *int `json:"index"`
	Glob  bool `json:"glob"`
}

func NewEdgeIDs(k *d2ast.Key) (eida []*EdgeID) {
	for _, ke := range k.Edges {
		eid := &EdgeID{
			SrcPath:  ke.Src.IDA(),
			SrcArrow: ke.SrcArrow == "<",
			DstPath:  ke.Dst.IDA(),
			DstArrow: ke.DstArrow == ">",
		}
		if k.EdgeIndex != nil {
			eid.Index = k.EdgeIndex.Int
			eid.Glob = k.EdgeIndex.Glob
		}
		eida = append(eida, eid)
	}
	return eida
}

func (eid *EdgeID) Copy() *EdgeID {
	tmp := *eid
	eid = &tmp

	eid.SrcPath = append([]string(nil), eid.SrcPath...)
	eid.DstPath = append([]string(nil), eid.DstPath...)
	return eid
}

func (eid *EdgeID) Match(eid2 *EdgeID) bool {
	if eid.Index != nil && eid2.Index != nil {
		if *eid.Index != *eid2.Index {
			return false
		}
	}

	if len(eid.SrcPath) != len(eid2.SrcPath) {
		return false
	}
	if eid.SrcArrow != eid2.SrcArrow {
		return false
	}
	for i, s := range eid.SrcPath {
		if !strings.EqualFold(s, eid2.SrcPath[i]) {
			return false
		}
	}

	if len(eid.DstPath) != len(eid2.DstPath) {
		return false
	}
	if eid.DstArrow != eid2.DstArrow {
		return false
	}
	for i, s := range eid.DstPath {
		if !strings.EqualFold(s, eid2.DstPath[i]) {
			return false
		}
	}

	return true
}

// resolve resolves both underscores and commons in eid.
// It returns the new eid, containing map adjusted for underscores and common ida.
func (eid *EdgeID) resolve(m *Map) (_ *EdgeID, _ *Map, common []string, _ error) {
	eid = eid.Copy()
	maxUnderscores := go2.Max(countUnderscores(eid.SrcPath), countUnderscores(eid.DstPath))
	for i := 0; i < maxUnderscores; i++ {
		if eid.SrcPath[0] == "_" {
			eid.SrcPath = eid.SrcPath[1:]
		} else {
			mf := ParentField(m)
			eid.SrcPath = append([]string{mf.Name}, eid.SrcPath...)
		}
		if eid.DstPath[0] == "_" {
			eid.DstPath = eid.DstPath[1:]
		} else {
			mf := ParentField(m)
			eid.DstPath = append([]string{mf.Name}, eid.DstPath...)
		}
		m = ParentMap(m)
		if m == nil {
			return nil, nil, nil, errors.New("invalid underscore")
		}
	}

	for len(eid.SrcPath) > 1 && len(eid.DstPath) > 1 {
		if !strings.EqualFold(eid.SrcPath[0], eid.DstPath[0]) {
			return eid, m, common, nil
		}
		common = append(common, eid.SrcPath[0])
		eid.SrcPath = eid.SrcPath[1:]
		eid.DstPath = eid.DstPath[1:]
	}

	return eid, m, common, nil
}

type Edge struct {
	// *Map
	parent Node

	ID *EdgeID `json:"edge_id"`

	Primary_ *Scalar `json:"primary,omitempty"`
	Map_     *Map    `json:"map,omitempty"`

	References []*EdgeReference `json:"references,omitempty"`
}

func (e *Edge) Copy(newParent Node) Node {
	tmp := *e
	e = &tmp

	e.parent = newParent
	e.References = append([]*EdgeReference(nil), e.References...)
	if e.Primary_ != nil {
		e.Primary_ = e.Primary_.Copy(e).(*Scalar)
	}
	if e.Map_ != nil {
		e.Map_ = e.Map_.Copy(e).(*Map)
	}
	return e
}

func (e *Edge) lastPrimaryRef() *EdgeReference {
	for i := len(e.References) - 1; i >= 0; i-- {
		fr := e.References[i]
		if fr.Context.Key.EdgeKey == nil {
			return fr
		}
	}
	return nil
}

func (e *Edge) LastPrimaryKey() *d2ast.Key {
	er := e.lastPrimaryRef()
	if er == nil {
		return nil
	}
	return er.Context.Key
}

func (e *Edge) LastRef() Reference {
	return e.References[len(e.References)-1]
}

type Array struct {
	parent Node
	Values []Value `json:"values"`
}

func (a *Array) Copy(newParent Node) Node {
	tmp := *a
	a = &tmp

	a.parent = newParent
	a.Values = append([]Value(nil), a.Values...)
	for i := range a.Values {
		a.Values[i] = a.Values[i].Copy(a).(Value)
	}
	return a
}

type FieldReference struct {
	String  d2ast.String   `json:"string"`
	KeyPath *d2ast.KeyPath `json:"key_path"`

	Context *RefContext `json:"context"`
}

// Primary returns true if the Value in Context.Key.Value corresponds to the Field
// represented by String.
func (fr *FieldReference) Primary() bool {
	if fr.KeyPath == fr.Context.Key.Key {
		return len(fr.Context.Key.Edges) == 0 && fr.KeyPathIndex() == len(fr.KeyPath.Path)-1
	} else if fr.KeyPath == fr.Context.Key.EdgeKey {
		return len(fr.Context.Key.Edges) == 1 && fr.KeyPathIndex() == len(fr.KeyPath.Path)-1
	}
	return false
}

func (fr *FieldReference) KeyPathIndex() int {
	for i, sb := range fr.KeyPath.Path {
		if sb.Unbox() == fr.String {
			return i
		}
	}
	panic("d2ir.KeyReference.KeyPathIndex: String not in KeyPath?")
}

func (fr *FieldReference) EdgeDest() bool {
	return fr.KeyPath == fr.Context.Edge.Dst
}

func (fr *FieldReference) InEdge() bool {
	return fr.Context.Edge != nil
}

func (fr *FieldReference) AST() d2ast.Node {
	if fr.String == nil {
		// Root map.
		return fr.Context.Scope
	}
	return fr.String
}

type EdgeReference struct {
	Context *RefContext `json:"context"`
}

func (er *EdgeReference) AST() d2ast.Node {
	return er.Context.Edge
}

// Primary returns true if the Value in Context.Key.Value corresponds to the *Edge
// represented by Context.Edge
func (er *EdgeReference) Primary() bool {
	return len(er.Context.Key.Edges) == 1 && er.Context.Key.EdgeKey == nil
}

type RefContext struct {
	Edge     *d2ast.Edge `json:"edge"`
	Key      *d2ast.Key  `json:"key"`
	Scope    *d2ast.Map  `json:"-"`
	ScopeMap *Map        `json:"-"`
	ScopeAST *d2ast.Map  `json:"-"`
}

func (rc *RefContext) Copy() *RefContext {
	tmp := *rc
	return &tmp
}

func (rc *RefContext) EdgeIndex() int {
	for i, e := range rc.Key.Edges {
		if e == rc.Edge {
			return i
		}
	}
	return -1
}

func (m *Map) FieldCountRecursive() int {
	if m == nil {
		return 0
	}
	acc := len(m.Fields)
	for _, f := range m.Fields {
		if f.Map() != nil {
			acc += f.Map().FieldCountRecursive()
		}
	}
	for _, e := range m.Edges {
		if e.Map_ != nil {
			acc += e.Map_.FieldCountRecursive()
		}
	}
	return acc
}

func (m *Map) IsContainer() bool {
	if m == nil {
		return false
	}
	for _, f := range m.Fields {
		_, isReserved := d2graph.ReservedKeywords[f.Name]
		if !isReserved {
			return true
		}
	}
	return false
}

func (m *Map) EdgeCountRecursive() int {
	if m == nil {
		return 0
	}
	acc := len(m.Edges)
	for _, f := range m.Fields {
		if f.Map() != nil {
			acc += f.Map().EdgeCountRecursive()
		}
	}
	for _, e := range m.Edges {
		if e.Map_ != nil {
			acc += e.Map_.EdgeCountRecursive()
		}
	}
	return acc
}

func (m *Map) GetClassMap(name string) *Map {
	root := RootMap(m)
	classes := root.Map().GetField("classes")
	if classes != nil && classes.Map() != nil {
		class := classes.Map().GetField(name)
		if class != nil && class.Map() != nil {
			return class.Map()
		}
	}
	return nil
}

func (m *Map) GetField(ida ...string) *Field {
	for len(ida) > 0 && ida[0] == "_" {
		m = ParentMap(m)
		if m == nil {
			return nil
		}
	}
	return m.getField(ida)
}

func (m *Map) getField(ida []string) *Field {
	if len(ida) == 0 {
		return nil
	}

	s := ida[0]
	rest := ida[1:]

	if s == "_" {
		return nil
	}

	for _, f := range m.Fields {
		if !strings.EqualFold(f.Name, s) {
			continue
		}
		if len(rest) == 0 {
			return f
		}
		if f.Map() != nil {
			return f.Map().getField(rest)
		}
	}
	return nil
}

// EnsureField is a bit of a misnomer. It's more of a Query/Ensure combination function at this point.
func (m *Map) EnsureField(kp *d2ast.KeyPath, refctx *RefContext, create bool) ([]*Field, error) {
	i := 0
	for kp.Path[i].Unbox().ScalarString() == "_" {
		m = ParentMap(m)
		if m == nil {
			return nil, d2parser.Errorf(kp.Path[i].Unbox(), "invalid underscore: no parent")
		}
		if i+1 == len(kp.Path) {
			return nil, d2parser.Errorf(kp.Path[i].Unbox(), "field key must contain more than underscores")
		}
		i++
	}

	var fa []*Field
	err := m.ensureField(i, kp, refctx, create, &fa)
	return fa, err
}

func (m *Map) ensureField(i int, kp *d2ast.KeyPath, refctx *RefContext, create bool, fa *[]*Field) error {
	us, ok := kp.Path[i].Unbox().(*d2ast.UnquotedString)
	if ok && us.Pattern != nil {
		fa2, ok := m.doubleGlob(us.Pattern)
		if ok {
			if i == len(kp.Path)-1 {
				*fa = append(*fa, fa2...)
			} else {
				for _, f := range fa2 {
					if f.Map() == nil {
						f.Composite = &Map{
							parent: f,
						}
					}
					err := f.Map().ensureField(i+1, kp, refctx, create, fa)
					if err != nil {
						return err
					}
				}
			}
			return nil
		}
		for _, f := range m.Fields {
			if matchPattern(f.Name, us.Pattern) {
				if i == len(kp.Path)-1 {
					*fa = append(*fa, f)
				} else {
					if f.Map() == nil {
						f.Composite = &Map{
							parent: f,
						}
					}
					err := f.Map().ensureField(i+1, kp, refctx, create, fa)
					if err != nil {
						return err
					}
				}
			}
		}
		return nil
	}

	head := kp.Path[i].Unbox().ScalarString()

	if _, ok := d2graph.ReservedKeywords[strings.ToLower(head)]; ok {
		head = strings.ToLower(head)
		if _, ok := d2graph.CompositeReservedKeywords[head]; !ok && i < len(kp.Path)-1 {
			return d2parser.Errorf(kp.Path[i].Unbox(), fmt.Sprintf(`"%s" must be the last part of the key`, head))
		}
	}

	if head == "_" {
		return d2parser.Errorf(kp.Path[i].Unbox(), `parent "_" can only be used in the beginning of paths, e.g. "_.x"`)
	}

	if head == "classes" && NodeBoardKind(m) == "" {
		return d2parser.Errorf(kp.Path[i].Unbox(), "%s is only allowed at a board root", head)
	}

	if findBoardKeyword(head) != -1 && NodeBoardKind(m) == "" {
		return d2parser.Errorf(kp.Path[i].Unbox(), "%s is only allowed at a board root", head)
	}

	for _, f := range m.Fields {
		if !strings.EqualFold(f.Name, head) {
			continue
		}

		// Don't add references for fake common KeyPath from trimCommon in CreateEdge.
		if refctx != nil {
			f.References = append(f.References, &FieldReference{
				String:  kp.Path[i].Unbox(),
				KeyPath: kp,
				Context: refctx,
			})
		}

		if i+1 == len(kp.Path) {
			*fa = append(*fa, f)
			return nil
		}
		if _, ok := f.Composite.(*Array); ok {
			return d2parser.Errorf(kp.Path[i].Unbox(), "cannot index into array")
		}
		if f.Map() == nil {
			f.Composite = &Map{
				parent: f,
			}
		}
		return f.Map().ensureField(i+1, kp, refctx, create, fa)
	}

	if !create {
		return nil
	}
	f := &Field{
		parent: m,
		Name:   head,
	}
	// Don't add references for fake common KeyPath from trimCommon in CreateEdge.
	if refctx != nil {
		f.References = append(f.References, &FieldReference{
			String:  kp.Path[i].Unbox(),
			KeyPath: kp,
			Context: refctx,
		})
	}
	m.Fields = append(m.Fields, f)
	if i+1 == len(kp.Path) {
		*fa = append(*fa, f)
		return nil
	}
	f.Composite = &Map{
		parent: f,
	}
	return f.Map().ensureField(i+1, kp, refctx, create, fa)
}

func (m *Map) DeleteEdge(eid *EdgeID) *Edge {
	if eid == nil {
		return nil
	}

	for i, e := range m.Edges {
		if e.ID.Match(eid) {
			m.Edges = append(m.Edges[:i], m.Edges[i+1:]...)
			return e
		}
	}
	return nil
}

func (m *Map) DeleteField(ida ...string) *Field {
	if len(ida) == 0 {
		return nil
	}

	s := ida[0]
	rest := ida[1:]

	for i, f := range m.Fields {
		if !strings.EqualFold(f.Name, s) {
			continue
		}
		if len(rest) == 0 {
			for _, fr := range f.References {
				for _, e := range m.Edges {
					for _, er := range e.References {
						if er.Context == fr.Context {
							m.DeleteEdge(e.ID)
							break
						}
					}
				}
			}
			m.Fields = append(m.Fields[:i], m.Fields[i+1:]...)

			// If a field was deleted from a keyword-holder keyword and that holder is empty,
			// then that holder becomes meaningless and should be deleted too
			parent := ParentField(f)
			for keywordHolder := range d2graph.ReservedKeywordHolders {
				if parent != nil && parent.Name == keywordHolder && len(parent.Map().Fields) == 0 {
					keywordHolderParentMap := ParentMap(parent)
					for i, f := range keywordHolderParentMap.Fields {
						if f.Name == keywordHolder {
							keywordHolderParentMap.Fields = append(keywordHolderParentMap.Fields[:i], keywordHolderParentMap.Fields[i+1:]...)
							break
						}
					}
				}
			}
			return f
		}
		if f.Map() != nil {
			return f.Map().DeleteField(rest...)
		}
	}
	return nil
}

func (m *Map) GetEdges(eid *EdgeID, refctx *RefContext) []*Edge {
	if refctx != nil {
		var ea []*Edge
		m.getEdges(eid, refctx, &ea)
		return ea
	}

	eid, m, common, err := eid.resolve(m)
	if err != nil {
		return nil
	}
	if len(common) > 0 {
		f := m.GetField(common...)
		if f == nil {
			return nil
		}
		if f.Map() != nil {
			return f.Map().GetEdges(eid, nil)
		}
		return nil
	}

	var ea []*Edge
	for _, e := range m.Edges {
		if e.ID.Match(eid) {
			ea = append(ea, e)
		}
	}
	return ea
}

func (m *Map) getEdges(eid *EdgeID, refctx *RefContext, ea *[]*Edge) error {
	eid, m, common, err := eid.resolve(m)
	if err != nil {
		return err
	}

	if len(common) > 0 {
		commonKP := d2ast.MakeKeyPath(common)
		lastMatch := 0
		for i, el := range commonKP.Path {
			for j := lastMatch; j < len(refctx.Edge.Src.Path); j++ {
				realEl := refctx.Edge.Src.Path[j]
				if el.ScalarString() == realEl.ScalarString() {
					commonKP.Path[i] = realEl
					lastMatch += j + 1
				}
			}
		}
		fa, err := m.EnsureField(commonKP, nil, false)
		if err != nil {
			return nil
		}
		for _, f := range fa {
			if _, ok := f.Composite.(*Array); ok {
				return d2parser.Errorf(refctx.Edge.Src, "cannot index into array")
			}
			if f.Map() == nil {
				f.Composite = &Map{
					parent: f,
				}
			}
			err = f.Map().getEdges(eid, refctx, ea)
			if err != nil {
				return err
			}
		}
		return nil
	}

	srcFA, err := refctx.ScopeMap.EnsureField(refctx.Edge.Src, nil, false)
	if err != nil {
		return err
	}
	dstFA, err := refctx.ScopeMap.EnsureField(refctx.Edge.Dst, nil, false)
	if err != nil {
		return err
	}

	for _, src := range srcFA {
		for _, dst := range dstFA {
			eid2 := eid.Copy()
			eid2.SrcPath = RelIDA(m, src)
			eid2.DstPath = RelIDA(m, dst)

			ea2 := m.GetEdges(eid2, nil)
			*ea = append(*ea, ea2...)
		}
	}
	return nil
}

func (m *Map) CreateEdge(eid *EdgeID, refctx *RefContext) ([]*Edge, error) {
	var ea []*Edge
	return ea, m.createEdge(eid, refctx, &ea)
}

func (m *Map) createEdge(eid *EdgeID, refctx *RefContext, ea *[]*Edge) error {
	if ParentEdge(m) != nil {
		return d2parser.Errorf(refctx.Edge, "cannot create edge inside edge")
	}

	eid, m, common, err := eid.resolve(m)
	if err != nil {
		return d2parser.Errorf(refctx.Edge, err.Error())
	}
	if len(common) > 0 {
		commonKP := d2ast.MakeKeyPath(common)
		lastMatch := 0
		for i, el := range commonKP.Path {
			for j := lastMatch; j < len(refctx.Edge.Src.Path); j++ {
				realEl := refctx.Edge.Src.Path[j]
				if el.ScalarString() == realEl.ScalarString() {
					commonKP.Path[i] = realEl
					lastMatch += j + 1
				}
			}
		}
		fa, err := m.EnsureField(commonKP, nil, true)
		if err != nil {
			return err
		}
		for _, f := range fa {
			if _, ok := f.Composite.(*Array); ok {
				return d2parser.Errorf(refctx.Edge.Src, "cannot index into array")
			}
			if f.Map() == nil {
				f.Composite = &Map{
					parent: f,
				}
			}
			err = f.Map().createEdge(eid, refctx, ea)
			if err != nil {
				return err
			}
		}
		return nil
	}

	ij := findProhibitedEdgeKeyword(eid.SrcPath...)
	if ij != -1 {
		return d2parser.Errorf(refctx.Edge.Src.Path[ij].Unbox(), "reserved keywords are prohibited in edges")
	}
	ij = findBoardKeyword(eid.SrcPath...)
	if ij == len(eid.SrcPath)-1 {
		return d2parser.Errorf(refctx.Edge.Src.Path[ij].Unbox(), "edge with board keyword alone doesn't make sense")
	}

	ij = findProhibitedEdgeKeyword(eid.DstPath...)
	if ij != -1 {
		return d2parser.Errorf(refctx.Edge.Dst.Path[ij].Unbox(), "reserved keywords are prohibited in edges")
	}
	ij = findBoardKeyword(eid.DstPath...)
	if ij == len(eid.DstPath)-1 {
		return d2parser.Errorf(refctx.Edge.Dst.Path[ij].Unbox(), "edge with board keyword alone doesn't make sense")
	}

	srcFA, err := refctx.ScopeMap.EnsureField(refctx.Edge.Src, refctx, true)
	if err != nil {
		return err
	}
	dstFA, err := refctx.ScopeMap.EnsureField(refctx.Edge.Dst, refctx, true)
	if err != nil {
		return err
	}

	for _, src := range srcFA {
		for _, dst := range dstFA {
			if src == dst && (refctx.Edge.Src.HasGlob() || refctx.Edge.Dst.HasGlob()) {
				// Globs do not make self edges.
				continue
			}

			if refctx.Edge.Src.HasDoubleGlob() {
				// If src has a double glob we only select leafs, those without children.
				if src.Map().IsContainer() {
					continue
				}
				if ParentBoard(src) != ParentBoard(dst) {
					continue
				}
			}
			if refctx.Edge.Dst.HasDoubleGlob() {
				// If dst has a double glob we only select leafs, those without children.
				if dst.Map().IsContainer() {
					continue
				}
				if ParentBoard(src) != ParentBoard(dst) {
					continue
				}
			}

			eid2 := eid.Copy()
			eid2.SrcPath = RelIDA(m, src)
			eid2.DstPath = RelIDA(m, dst)
			e, err := m.createEdge2(eid2, refctx, src, dst)
			if err != nil {
				return err
			}
			*ea = append(*ea, e)
		}
	}
	return nil
}

func (m *Map) createEdge2(eid *EdgeID, refctx *RefContext, src, dst *Field) (*Edge, error) {
	if NodeBoardKind(src) != "" {
		return nil, d2parser.Errorf(refctx.Edge.Src, "cannot create edges between boards")
	}
	if NodeBoardKind(dst) != "" {
		return nil, d2parser.Errorf(refctx.Edge.Dst, "cannot create edges between boards")
	}
	if ParentBoard(src) != ParentBoard(dst) {
		return nil, d2parser.Errorf(refctx.Edge, "cannot create edges between boards")
	}

	eid.Index = nil
	eid.Glob = true
	ea := m.GetEdges(eid, nil)
	index := len(ea)
	eid.Index = &index
	eid.Glob = false
	e := &Edge{
		parent: m,
		ID:     eid,
		References: []*EdgeReference{{
			Context: refctx,
		}},
	}
	m.Edges = append(m.Edges, e)

	return e, nil
}

func (s *Scalar) AST() d2ast.Node {
	return s.Value
}

func (f *Field) AST() d2ast.Node {
	k := &d2ast.Key{
		Key: &d2ast.KeyPath{
			Path: []*d2ast.StringBox{
				d2ast.MakeValueBox(d2ast.RawString(f.Name, true)).StringBox(),
			},
		},
	}

	if f.Primary_ != nil {
		k.Primary = d2ast.MakeValueBox(f.Primary_.AST().(d2ast.Value)).ScalarBox()
	}
	if f.Composite != nil {
		k.Value = d2ast.MakeValueBox(f.Composite.AST().(d2ast.Value))
	}

	return k
}

func (e *Edge) AST() d2ast.Node {
	astEdge := &d2ast.Edge{}

	astEdge.Src = d2ast.MakeKeyPath(e.ID.SrcPath)
	if e.ID.SrcArrow {
		astEdge.SrcArrow = "<"
	}
	astEdge.Dst = d2ast.MakeKeyPath(e.ID.DstPath)
	if e.ID.DstArrow {
		astEdge.DstArrow = ">"
	}

	k := &d2ast.Key{
		Edges: []*d2ast.Edge{astEdge},
	}

	if e.Primary_ != nil {
		k.Primary = d2ast.MakeValueBox(e.Primary_.AST().(d2ast.Value)).ScalarBox()
	}
	if e.Map_ != nil {
		k.Value = d2ast.MakeValueBox(e.Map_.AST().(*d2ast.Map))
	}

	return k
}

func (a *Array) AST() d2ast.Node {
	if a == nil {
		return nil
	}
	astArray := &d2ast.Array{}
	for _, av := range a.Values {
		astArray.Nodes = append(astArray.Nodes, d2ast.MakeArrayNodeBox(av.AST().(d2ast.ArrayNode)))
	}
	return astArray
}

func (m *Map) AST() d2ast.Node {
	if m == nil {
		return nil
	}
	astMap := &d2ast.Map{}
	if m.Root() {
		astMap.Range = d2ast.MakeRange(",0:0:0-1:0:0")
	} else {
		astMap.Range = d2ast.MakeRange(",1:0:0-2:0:0")
	}
	for _, f := range m.Fields {
		astMap.Nodes = append(astMap.Nodes, d2ast.MakeMapNodeBox(f.AST().(d2ast.MapNode)))
	}
	for _, e := range m.Edges {
		astMap.Nodes = append(astMap.Nodes, d2ast.MakeMapNodeBox(e.AST().(d2ast.MapNode)))
	}
	return astMap
}

func (m *Map) appendFieldReferences(i int, kp *d2ast.KeyPath, refctx *RefContext) {
	sb := kp.Path[i]
	f := m.GetField(sb.Unbox().ScalarString())
	if f == nil {
		return
	}

	f.References = append(f.References, &FieldReference{
		String:  sb.Unbox(),
		KeyPath: kp,
		Context: refctx,
	})
	if i+1 == len(kp.Path) {
		return
	}
	if f.Map() != nil {
		f.Map().appendFieldReferences(i+1, kp, refctx)
	}
}

func RootMap(m *Map) *Map {
	if m.Root() {
		return m
	}
	return RootMap(ParentMap(m))
}

func ParentMap(n Node) *Map {
	for {
		n = n.Parent()
		if n == nil {
			return nil
		}
		if m, ok := n.(*Map); ok {
			return m
		}
	}
}

func ParentField(n Node) *Field {
	for {
		n = n.Parent()
		if n == nil {
			return nil
		}
		if f, ok := n.(*Field); ok {
			return f
		}
	}
}

func IsVar(n Node) bool {
	for {
		if n == nil {
			return false
		}
		if NodeBoardKind(n) != "" {
			return false
		}
		if f, ok := n.(*Field); ok && f.Name == "vars" {
			return true
		}
		n = n.Parent()
	}
}

func ParentBoard(n Node) Node {
	for {
		n = n.Parent()
		if n == nil {
			return nil
		}
		if NodeBoardKind(n) != "" {
			return n
		}
	}
}

func ParentEdge(n Node) *Edge {
	for {
		n = n.Parent()
		if n == nil {
			return nil
		}
		if e, ok := n.(*Edge); ok {
			return e
		}
	}
}

func countUnderscores(p []string) int {
	for i, el := range p {
		if el != "_" {
			return i
		}
	}
	return 0
}

func findBoardKeyword(ida ...string) int {
	for i := range ida {
		if _, ok := d2graph.BoardKeywords[ida[i]]; ok {
			return i
		}
	}
	return -1
}

func findProhibitedEdgeKeyword(ida ...string) int {
	for i := range ida {
		if _, ok := d2graph.SimpleReservedKeywords[ida[i]]; ok {
			return i
		}
		if _, ok := d2graph.ReservedKeywordHolders[ida[i]]; ok {
			return i
		}
	}
	return -1
}

func parentRef(n Node) Reference {
	f := ParentField(n)
	if f != nil {
		return f.LastRef()
	}
	e := ParentEdge(n)
	if e != nil {
		return e.LastRef()
	}
	return nil
}

func parentPrimaryKey(n Node) *d2ast.Key {
	f := ParentField(n)
	if f != nil {
		return f.LastPrimaryKey()
	}
	e := ParentEdge(n)
	if e != nil {
		return e.LastPrimaryKey()
	}
	return nil
}

// BoardIDA returns the absolute path to n from the nearest board root.
func BoardIDA(n Node) (ida []string) {
	for {
		f, ok := n.(*Field)
		if ok {
			if f.Root() || NodeBoardKind(f) != "" {
				reverseIDA(ida)
				return ida
			}
			ida = append(ida, f.Name)
		}
		f = ParentField(n)
		if f == nil {
			reverseIDA(ida)
			return ida
		}
		n = f
	}
}

// IDA returns the absolute path to n.
func IDA(n Node) (ida []string) {
	for {
		f, ok := n.(*Field)
		if ok {
			ida = append(ida, f.Name)
			if f.Root() {
				reverseIDA(ida)
				return ida
			}
		}
		f = ParentField(n)
		if f == nil {
			reverseIDA(ida)
			return ida
		}
		n = f
	}
}

// RelIDA returns the path to n relative to p.
func RelIDA(p, n Node) (ida []string) {
	for {
		f, ok := n.(*Field)
		if ok {
			ida = append(ida, f.Name)
			if f.Root() {
				reverseIDA(ida)
				return ida
			}
		}
		f = ParentField(n)
		if f == nil || f.Root() || f == p || f.Composite == p {
			reverseIDA(ida)
			return ida
		}
		n = f
	}
}

func reverseIDA(ida []string) {
	for i := 0; i < len(ida)/2; i++ {
		tmp := ida[i]
		ida[i] = ida[len(ida)-i-1]
		ida[len(ida)-i-1] = tmp
	}
}

func (f *Field) Equal(n2 Node) bool {
	f2 := n2.(*Field)

	if f.Name != f2.Name {
		return false
	}
	if !f.Primary_.Equal(f2.Primary_) {
		return false
	}
	if !f.Composite.Equal(f2.Composite) {
		return false
	}
	return true
}

func (e *Edge) Equal(n2 Node) bool {
	e2 := n2.(*Edge)

	if !e.ID.Match(e2.ID) {
		return false
	}
	if !e.Primary_.Equal(e2.Primary_) {
		return false
	}
	if !e.Map_.Equal(e2.Map_) {
		return false
	}
	return true
}

func (a *Array) Equal(n2 Node) bool {
	a2 := n2.(*Array)

	if len(a.Values) != len(a2.Values) {
		return false
	}

	for i := range a.Values {
		if !a.Values[i].Equal(a2.Values[i]) {
			return false
		}
	}

	return true
}

func (m *Map) Equal(n2 Node) bool {
	m2 := n2.(*Map)

	if len(m.Fields) != len(m2.Fields) {
		return false
	}
	if len(m.Edges) != len(m2.Edges) {
		return false
	}

	for i := range m.Fields {
		if !m.Fields[i].Equal(m2.Fields[i]) {
			return false
		}
	}
	for i := range m.Edges {
		if !m.Edges[i].Equal(m2.Edges[i]) {
			return false
		}
	}

	return true
}

func (m *Map) InClass(key *d2ast.Key) bool {
	classes := m.Map().GetField("classes")
	if classes == nil || classes.Map() == nil {
		return false
	}

	for _, class := range classes.Map().Fields {
		if class.Map() == nil {
			continue
		}
		classF := class.Map().GetField(key.Key.IDA()...)
		if classF == nil {
			continue
		}

		for _, ref := range classF.References {
			if ref.Context.Key == key {
				return true
			}
		}
	}
	return false
}

func (m *Map) IsClass() bool {
	parentBoard := ParentBoard(m)
	if parentBoard.Map() == nil {
		return false
	}
	classes := parentBoard.Map().GetField("classes")
	if classes == nil || classes.Map() == nil {
		return false
	}

	for _, class := range classes.Map().Fields {
		if class.Map() == m {
			return true
		}
	}
	return false
}
