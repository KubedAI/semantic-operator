package v1alpha1

// DialectPreference is the order in which expression dialects are selected
// for compilation. STARROCKS is an operator extension dialect; ANSI_SQL is
// the Ossie default and is passed through to StarRocks unchanged.
var DialectPreference = []string{"STARROCKS", "ANSI_SQL"}

// Select returns the expression body for the most preferred dialect present.
func (e Expression) Select() (string, bool) {
	for _, d := range DialectPreference {
		for _, de := range e.Dialects {
			if de.Dialect == d {
				return de.Expression, true
			}
		}
	}
	return "", false
}

// FindDataset returns the dataset with the given logical name.
func (m *OssieModel) FindDataset(name string) *Dataset {
	for i := range m.Datasets {
		if m.Datasets[i].Name == name {
			return &m.Datasets[i]
		}
	}
	return nil
}

// FindField returns the field with the given name.
func (d *Dataset) FindField(name string) *Field {
	for i := range d.Fields {
		if d.Fields[i].Name == name {
			return &d.Fields[i]
		}
	}
	return nil
}

// FindMetric returns the metric with the given name.
func (m *OssieModel) FindMetric(name string) *Metric {
	for i := range m.Metrics {
		if m.Metrics[i].Name == name {
			return &m.Metrics[i]
		}
	}
	return nil
}

// Role returns the policy for the named role.
func (g *GovernanceSpec) Role(name string) *RolePolicy {
	if g == nil {
		return nil
	}
	for i := range g.Roles {
		if g.Roles[i].Name == name {
			return &g.Roles[i]
		}
	}
	return nil
}
