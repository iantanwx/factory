package cli

import (
	"fmt"
	"slices"
	"sort"
)

// Principal kinds (KTD12). A call with no token and no host-admin
// credential is PrincipalNone and reaches the meta verbs only.
const (
	PrincipalNone        = "none"
	PrincipalRun         = "run"
	PrincipalRoom        = "room"
	PrincipalCoordinator = "coordinator"
	PrincipalHost        = "host"
)

// PrincipalKinds lists every kind in a fixed order.
var PrincipalKinds = []string{PrincipalNone, PrincipalRun, PrincipalRoom, PrincipalCoordinator, PrincipalHost}

// callerKinds maps each caller label of the surface to the principal kinds
// it admits. A stage label such as "reviewer run" admits every run here.
// The scheduler (U8) can narrow a label to its stage later.
var callerKinds = map[string][]string{
	"run":          {PrincipalRun},
	"reviewer run": {PrincipalRun},
	"fixer":        {PrincipalRun},
	"decomposer":   {PrincipalRun},
	"room":         {PrincipalRoom},
	"intake room":  {PrincipalRoom},
	"coordinator":  {PrincipalCoordinator},
	"host":         {PrincipalHost},
	"anyone":       PrincipalKinds,
}

// CapabilityMatrix is the deny-by-default access table (KTD12). Verbs maps
// each verb path to the principal kinds that may call it. A verb absent
// from Verbs admits nobody. ItemScoped lists the verbs whose item argument
// a run or a room must hold. HostVerbs lists the verbs that need the
// host-admin credential and a caller outside every checkout.
type CapabilityMatrix struct {
	Principals []string            `json:"principals"`
	Verbs      map[string][]string `json:"verbs"`
	ItemScoped []string            `json:"item_scoped"`
	HostVerbs  []string            `json:"host_verbs"`
}

// Allows reports whether kind may call the verb at path.
func (m *CapabilityMatrix) Allows(path, kind string) bool {
	return slices.Contains(m.Verbs[path], kind)
}

// IsItemScoped reports whether a run or a room must hold the verb's item.
func (m *CapabilityMatrix) IsItemScoped(path string) bool {
	return slices.Contains(m.ItemScoped, path)
}

// IsHostVerb reports whether the verb needs the host-admin credential.
func (m *CapabilityMatrix) IsHostVerb(path string) bool {
	return slices.Contains(m.HostVerbs, path)
}

// DeriveMatrix builds the matrix from each verb's callers. It refuses a
// caller label that names no principal kind, so a new label cannot open a
// verb by accident.
func DeriveMatrix(r *Registry) (CapabilityMatrix, error) {
	m := CapabilityMatrix{
		Principals: slices.Clone(PrincipalKinds),
		Verbs:      map[string][]string{},
		ItemScoped: []string{},
		HostVerbs:  []string{},
	}
	for _, v := range r.verbs {
		var kinds []string
		for _, c := range v.Callers {
			ks, ok := callerKinds[c]
			if !ok {
				return CapabilityMatrix{}, fmt.Errorf("verb %q: caller %q names no principal kind", v.Path, c)
			}
			for _, k := range ks {
				if !slices.Contains(kinds, k) {
					kinds = append(kinds, k)
				}
			}
		}
		sort.Slice(kinds, func(i, j int) bool {
			return slices.Index(PrincipalKinds, kinds[i]) < slices.Index(PrincipalKinds, kinds[j])
		})
		m.Verbs[v.Path] = kinds
		if slices.Contains(v.Callers, "host") {
			m.HostVerbs = append(m.HostVerbs, v.Path)
		}
		for _, a := range v.Args {
			if a.Name == "item" {
				m.ItemScoped = append(m.ItemScoped, v.Path)
				break
			}
		}
	}
	return m, nil
}

// Surface returns the registry that the binary runs: the whole surface and
// its capability matrix. The server and the CLI build the same one, so the
// schema each prints agrees.
func Surface() *Registry {
	r := New()
	for _, t := range surfaceTables {
		must(r.RegisterTransitionTable(t.kind, t.table))
	}
	m, err := DeriveMatrix(r)
	must(err)
	must(r.SetCapabilityMatrix(m))
	return r
}
