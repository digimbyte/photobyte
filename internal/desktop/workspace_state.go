package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"
	"unicode/utf8"

	"digiphoto/internal/publish"
)

type dockSide int

const (
	dockLeft dockSide = iota
	dockRight
	dockBottom
	dockCount
)

var dockNames = [dockCount]string{"Left", "Right", "Bottom"}

const (
	panelLayers      = "Layers"
	panelProperties  = "Properties"
	panelHistory     = "History"
	panelAssets      = "Assets"
	panelTimeline    = "Timeline"
	defaultWorkspace = "Default"
	maxWorkspaces    = 12
)

var panelNames = []string{panelLayers, panelProperties, panelHistory, panelAssets, panelTimeline}

type panelTab struct {
	Name   string `json:"name"`
	Hidden bool   `json:"hidden,omitempty"`
}

type panelGroup struct {
	ID        int        `json:"id"`
	Tabs      []panelTab `json:"tabs"`
	Active    string     `json:"active"`
	Collapsed bool       `json:"collapsed,omitempty"`
	Weight    int        `json:"weight"`
}

type workspaceDock struct {
	Extent int          `json:"extent_dp"`
	Groups []panelGroup `json:"groups,omitempty"`
}

type workspaceLayout [dockCount]workspaceDock

type workspaceState struct {
	Version int                        `json:"version"`
	Current workspaceLayout            `json:"current"`
	Preset  string                     `json:"preset"`
	Saved   map[string]workspaceLayout `json:"saved"`
}

func defaultLayout() workspaceLayout {
	return workspaceLayout{
		dockLeft: {Extent: 300},
		dockRight: {Extent: 300, Groups: []panelGroup{
			{ID: 1, Tabs: []panelTab{{Name: panelProperties}}, Active: panelProperties, Weight: 45},
			{ID: 2, Tabs: []panelTab{{Name: panelLayers}, {Name: panelHistory}, {Name: panelAssets}}, Active: panelLayers, Weight: 55},
		}},
		dockBottom: {Extent: 136, Groups: []panelGroup{
			{ID: 3, Tabs: []panelTab{{Name: panelTimeline}}, Active: panelTimeline, Weight: 100},
		}},
	}
}

func defaultWorkspaceState() workspaceState {
	return workspaceState{Version: 1, Current: defaultLayout(), Preset: defaultWorkspace, Saved: make(map[string]workspaceLayout)}
}

func (l workspaceLayout) clone() workspaceLayout {
	for i := range l {
		l[i].Groups = slices.Clone(l[i].Groups)
		for j := range l[i].Groups {
			l[i].Groups[j].Tabs = slices.Clone(l[i].Groups[j].Tabs)
		}
	}
	return l
}

func (g *panelGroup) normalize() {
	for _, t := range g.Tabs {
		if !t.Hidden && t.Name == g.Active {
			return
		}
	}
	g.Active = ""
	for _, t := range g.Tabs {
		if !t.Hidden {
			g.Active = t.Name
			return
		}
	}
}

func (l *workspaceLayout) group(id int) (dockSide, *panelGroup) {
	for side := dockLeft; side < dockCount; side++ {
		for i := range l[side].Groups {
			if l[side].Groups[i].ID == id {
				return side, &l[side].Groups[i]
			}
		}
	}
	return dockLeft, nil
}

func (l *workspaceLayout) panel(name string) (dockSide, *panelGroup, int) {
	for side := dockLeft; side < dockCount; side++ {
		for i := range l[side].Groups {
			g := &l[side].Groups[i]
			for j, t := range g.Tabs {
				if t.Name == name {
					return side, g, j
				}
			}
		}
	}
	return dockLeft, nil, -1
}

func (l *workspaceLayout) activate(name string) bool {
	_, g, i := l.panel(name)
	if g == nil || (g.Active == name && !g.Collapsed && !g.Tabs[i].Hidden) {
		return false
	}
	g.Tabs[i].Hidden, g.Collapsed, g.Active = false, false, name
	return true
}

func (l *workspaceLayout) hide(name string) {
	if _, g, i := l.panel(name); g != nil {
		g.Tabs[i].Hidden = true
		g.normalize()
	}
}

type dockDrop struct {
	side        dockSide
	group       int    // merge into this group
	beforeTab   string // insert before this tab when merging
	beforeGroup int    // insert before this group when stacking
}

// move transfers one tab or a complete group. Destination anchors are stable
// even when removing the source changes indices.
func (l *workspaceLayout) move(groupID int, panel string, to dockDrop) bool {
	side, from := l.group(groupID)
	if from == nil || to.side < dockLeft || to.side >= dockCount {
		return false
	}
	index := -1
	if panel != "" {
		index = slices.IndexFunc(from.Tabs, func(t panelTab) bool { return t.Name == panel && !t.Hidden })
		if index < 0 {
			return false
		}
	}
	if to.group == groupID && (panel == "" || to.beforeTab == panel) {
		return false
	}
	if to.beforeGroup == groupID && (panel == "" || len(from.Tabs) == 1) {
		return false
	}
	if to.group != 0 {
		destSide, target := l.group(to.group)
		if target == nil || destSide != to.side {
			return false
		}
	}
	moving := *from
	if index >= 0 {
		moving.Tabs = []panelTab{from.Tabs[index]}
		moving.Active, moving.Collapsed = panel, false
		from.Tabs = slices.Delete(from.Tabs, index, index+1)
		from.normalize()
		if len(from.Tabs) != 0 {
			moving.ID = 1
			for _, other := l.group(moving.ID); other != nil; _, other = l.group(moving.ID) {
				moving.ID++
			}
		}
	}
	if index < 0 || len(from.Tabs) == 0 {
		l[side].Groups = slices.DeleteFunc(l[side].Groups, func(g panelGroup) bool { return g.ID == groupID })
		if len(l[side].Groups) == 0 {
			l[side].Groups = nil
		}
	}
	if to.group != 0 {
		_, target := l.group(to.group)
		at := slices.IndexFunc(target.Tabs, func(t panelTab) bool { return t.Name == to.beforeTab })
		if at < 0 {
			at = len(target.Tabs)
		}
		target.Tabs = slices.Insert(target.Tabs, at, moving.Tabs...)
		target.Active, target.Collapsed = moving.Active, false
		target.normalize()
	} else {
		groups := &l[to.side].Groups
		at := slices.IndexFunc(*groups, func(g panelGroup) bool { return g.ID == to.beforeGroup })
		if at < 0 {
			at = len(*groups)
		}
		*groups = slices.Insert(*groups, at, moving)
	}
	return true
}

func (s *workspaceState) save(name string) error {
	name = strings.TrimSpace(name)
	if !workspaceNameOK(name) {
		return errors.New("use a name of 1 to 36 characters other than Default")
	}
	for existing := range s.Saved {
		if strings.EqualFold(existing, name) {
			name = existing
			break
		}
	}
	if _, exists := s.Saved[name]; !exists && len(s.Saved) >= maxWorkspaces {
		return fmt.Errorf("keep up to %d saved workspaces; remove one first", maxWorkspaces)
	}
	s.Saved[name], s.Preset = s.Current.clone(), name
	return nil
}

func (s *workspaceState) restore(name string) {
	if name == defaultWorkspace {
		s.Current = defaultLayout()
	} else if saved, ok := s.Saved[name]; ok {
		s.Current = saved.clone()
	} else {
		return
	}
	s.Preset = name
}

func workspaceNameOK(name string) bool {
	n := utf8.RuneCountInString(name)
	return n > 0 && n <= 36 && strings.TrimSpace(name) == name && !strings.EqualFold(name, defaultWorkspace)
}

func (l workspaceLayout) valid() bool {
	panels, groups := make(map[string]bool), make(map[int]bool)
	for _, dock := range l {
		if dock.Extent < 80 || dock.Extent > 1400 {
			return false
		}
		for _, g := range dock.Groups {
			if g.ID <= 0 || g.ID > 64 || groups[g.ID] || len(g.Tabs) == 0 || g.Weight < 1 || g.Weight > 1000 {
				return false
			}
			groups[g.ID] = true
			active := g.Active
			g.normalize()
			if active != g.Active {
				return false
			}
			for _, tab := range g.Tabs {
				if panels[tab.Name] || !slices.Contains(panelNames, tab.Name) {
					return false
				}
				panels[tab.Name] = true
			}
		}
	}
	return len(panels) == len(panelNames)
}

func readWorkspace(path string) (workspaceState, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return defaultWorkspaceState(), nil
	}
	if err != nil {
		return defaultWorkspaceState(), err
	}
	defer f.Close()
	var s workspaceState
	decoder := json.NewDecoder(io.LimitReader(f, 64<<10))
	if err := decoder.Decode(&s); err != nil {
		return defaultWorkspaceState(), err
	}
	if s.Version != 1 || !s.Current.valid() || len(s.Saved) > maxWorkspaces {
		return defaultWorkspaceState(), errors.New("invalid or unsupported workspace layout")
	}
	for name, l := range s.Saved {
		if !workspaceNameOK(name) || !l.valid() {
			return defaultWorkspaceState(), errors.New("invalid saved workspace")
		}
	}
	if _, ok := s.Saved[s.Preset]; !ok && s.Preset != defaultWorkspace {
		return defaultWorkspaceState(), errors.New("active workspace is missing")
	}
	if s.Saved == nil {
		s.Saved = make(map[string]workspaceLayout)
	}
	return s, nil
}

// A single writer persists the newest queued snapshot off the UI thread.
// Named layouts are immutable once inserted, so the queue only clones their map.
type workspaceWriter struct {
	updates chan workspaceState
	results chan error
	done    chan struct{}
}

func startWorkspaceWriter(path string, notify func()) *workspaceWriter {
	w := &workspaceWriter{make(chan workspaceState, 1), make(chan error, 1), make(chan struct{})}
	go func() {
		defer close(w.done)
		for s := range w.updates {
			err := publish.ReplaceFile(context.Background(), path, func(f *os.File) error {
				return json.NewEncoder(f).Encode(s)
			})
			select {
			case <-w.results:
			default:
			}
			w.results <- err
			notify()
		}
	}()
	return w
}

func (w *workspaceWriter) save(s workspaceState) {
	if w == nil {
		return
	}
	s.Current, s.Saved = s.Current.clone(), maps.Clone(s.Saved)
	select {
	case <-w.updates:
	default:
	}
	w.updates <- s
}

func (w *workspaceWriter) close() error {
	close(w.updates)
	<-w.done
	select {
	case err := <-w.results:
		return err
	default:
		return nil
	}
}
