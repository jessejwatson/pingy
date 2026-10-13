package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

	"pingy/internal/monitor"
)

// savedView is a group of hosts graphed together, saved under a name.
type savedView struct {
	Name  string     `json:"name"`
	Hosts []viewHost `json:"hosts"`
}

// viewHost is one host in a view. It's found again by ID, or by target
// if it's since been deleted and pinged again, so a view never needs a
// second monitor for the same host.
type viewHost struct {
	ID     string `json:"id"`
	Target string `json:"target"`
}

type viewsFile struct {
	Views []savedView `json:"views"`
}

func (a *App) viewsPath() string { return filepath.Join(a.Home, "views.json") }

// loadViews reads the saved views; none saved yet is no error.
func (a *App) loadViews() ([]savedView, error) {
	b, err := os.ReadFile(a.viewsPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f viewsFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("can't read %s: %v", a.viewsPath(), err)
	}
	return f.Views, nil
}

// writeViews replaces the saved views, all at once so a reader never sees
// half of them.
func (a *App) writeViews(vs []savedView) error {
	if err := os.MkdirAll(a.Home, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(viewsFile{Views: vs}, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(a.Home, ".views-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), a.viewsPath())
}

func findView(vs []savedView, name string) int {
	return slices.IndexFunc(vs, func(v savedView) bool { return strings.EqualFold(v.Name, name) })
}

// resolve finds v's hosts among metas, in the view's order and each once,
// and counts those no longer pinged.
func (v savedView) resolve(metas []*monitor.Meta) (found []*monitor.Meta, gone int) {
	for _, vh := range v.Hosts {
		i := slices.IndexFunc(metas, func(m *monitor.Meta) bool {
			return m.ID == vh.ID && strings.EqualFold(m.Target, vh.Target)
		})
		if i < 0 {
			i = slices.IndexFunc(metas, func(m *monitor.Meta) bool { return strings.EqualFold(m.Target, vh.Target) })
		}
		switch {
		case i < 0:
			gone++
		case !slices.Contains(found, metas[i]):
			found = append(found, metas[i])
		}
	}
	return found, gone
}

// checkViewName reports why name can't name a view, other than the one
// now called old.
func (a *App) checkViewName(vs []savedView, name, old string) error {
	if name == "" {
		return errors.New("give the view a name")
	}
	if strings.ContainsFunc(name, unicode.IsSpace) {
		return fmt.Errorf("a view's name can't have spaces, so pingy -g can open it; try %s", strings.Join(strings.Fields(name), "-"))
	}
	if strings.HasPrefix(name, "-") {
		return errors.New("a view's name can't start with -")
	}
	if err := monitor.CheckName(a.root(), name, ""); err != nil {
		return err
	}
	for _, m := range monitor.List(a.root()) {
		if strings.EqualFold(m.Target, name) || m.ID == name {
			return fmt.Errorf("%q is already taken by monitor %s", name, m.ID)
		}
	}
	if i := findView(vs, name); i >= 0 && !strings.EqualFold(vs[i].Name, old) {
		return fmt.Errorf("there's already a view called %q", vs[i].Name)
	}
	return nil
}

// checkHostName is monitor.CheckName, also keeping clear of views' names.
func (a *App) checkHostName(name, id string) error {
	if err := monitor.CheckName(a.root(), name, id); err != nil {
		return err
	}
	vs, _ := a.loadViews()
	if i := findView(vs, name); name != "" && i >= 0 {
		return fmt.Errorf("%q is already the name of a view", vs[i].Name)
	}
	return nil
}

// saveView saves hosts as the view name, replacing the view called old
// (which renames it when the names differ), or adding it when old is "".
func (a *App) saveView(name, old string, hosts []*host) error {
	vs, err := a.loadViews()
	if err != nil {
		return err
	}
	name = strings.TrimSpace(name)
	if err := a.checkViewName(vs, name, old); err != nil {
		return err
	}
	if len(hosts) == 0 {
		return errors.New("there are no hosts here to save")
	}
	v := savedView{Name: name}
	for _, h := range hosts {
		v.Hosts = append(v.Hosts, viewHost{ID: h.meta.ID, Target: h.meta.Target})
	}
	if i := findView(vs, old); old != "" && i >= 0 {
		vs[i] = v
	} else {
		vs = append(vs, v)
	}
	return a.writeViews(vs)
}

// updateView makes the view name hold just hosts, in their order. Its
// hosts no longer pinged are kept at the end, so they can still come back.
func (a *App) updateView(name string, hosts []*host) error {
	vs, err := a.loadViews()
	if err != nil {
		return err
	}
	i := findView(vs, name)
	if i < 0 {
		return fmt.Errorf("the view %s was deleted elsewhere; w saves it again", name)
	}
	if len(hosts) == 0 {
		return errors.New("a view needs at least one host; ctrl-x in the panel deletes it")
	}
	var out []viewHost
	for _, h := range hosts {
		out = append(out, viewHost{ID: h.meta.ID, Target: h.meta.Target})
	}
	all := monitor.List(a.root())
	for _, vh := range vs[i].Hosts {
		if found, _ := (savedView{Hosts: []viewHost{vh}}).resolve(all); len(found) == 0 {
			out = append(out, vh)
		}
	}
	vs[i].Hosts = out
	return a.writeViews(vs)
}

func (a *App) deleteView(name string) error {
	vs, err := a.loadViews()
	if err != nil {
		return err
	}
	if i := findView(vs, name); i >= 0 {
		return a.writeViews(slices.Delete(vs, i, i+1))
	}
	return nil
}

// expand turns hosts and view names, as given to pingy -g or -l, into the
// monitors they mean, each once. A view's name wins over a host's target.
func (a *App) expand(args []string) (list []*monitor.Meta, views []string, err error) {
	root := a.root()
	all := monitor.List(root)
	vs, err := a.loadViews()
	if err != nil {
		return nil, nil, err
	}
	add := func(m *monitor.Meta) {
		if !slices.ContainsFunc(list, func(o *monitor.Meta) bool { return o.ID == m.ID }) {
			list = append(list, m)
		}
	}
	for _, t := range args {
		if i := findView(vs, t); i >= 0 {
			found, _ := vs[i].resolve(all)
			if len(found) == 0 {
				return nil, nil, fmt.Errorf("none of the view %s's hosts are being pinged any more; open pingy to pick new ones", vs[i].Name)
			}
			for _, m := range found {
				add(m)
			}
			views = append(views, vs[i].Name)
			continue
		}
		m := monitor.Find(root, t)
		if m == nil {
			return nil, nil, fmt.Errorf("nothing called %q is being pinged; pingy -l lists them", t)
		}
		add(m)
	}
	return list, views, nil
}
