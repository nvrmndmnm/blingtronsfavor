package client

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// errBuildInfoHeader is returned when .build.info has no header row.
var errBuildInfoHeader = errors.New(".build.info: missing header row")

// buildRow is one data row of .build.info keyed by lower-case column name.
type buildRow map[string]string

// parseBuildInfo parses the pipe-delimited .build.info table. The header row
// carries type suffixes ("Version!STRING:32") which are stripped, so column
// order is irrelevant. The first non-empty line is the header.
func parseBuildInfo(data string) ([]buildRow, error) {
	var header []string
	var rows []buildRow
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		cells := strings.Split(line, "|")
		if header == nil {
			header = make([]string, len(cells))
			for i, cell := range cells {
				name := cell
				if cut := strings.IndexByte(name, '!'); cut >= 0 {
					name = name[:cut]
				}
				header[i] = strings.ToLower(strings.TrimSpace(name))
			}
			continue
		}
		row := make(buildRow, len(cells))
		for i, cell := range cells {
			if i < len(header) {
				row[header[i]] = strings.TrimSpace(cell)
			}
		}
		rows = append(rows, row)
	}
	if header == nil {
		return nil, errBuildInfoHeader
	}
	return rows, nil
}

// selectBuildRow picks the .build.info row with Active == 1 whose Product
// equals the .flavor.info token. When the token is unknown, rows are matched
// by flavor instead. If several rows qualify, the highest version wins.
func selectBuildRow(rows []buildRow, product string, flavor Flavor) (Version, error) {
	matches := func(row buildRow) bool {
		if row["active"] != "1" {
			return false
		}
		rowProduct := strings.TrimSpace(row["product"])
		if product != "" {
			return strings.EqualFold(rowProduct, product)
		}
		rowFlavor, _ := flavorFromToken(rowProduct)
		return rowFlavor == flavor
	}
	var (
		best  Version
		found bool
	)
	for _, row := range rows {
		if !matches(row) {
			continue
		}
		version, err := parseVersion(row["version"])
		if err != nil {
			return Version{}, fmt.Errorf("build.info: product %q: %w", row["product"], err)
		}
		if !found || version.newerThan(best) {
			best, found = version, true
		}
	}
	if !found {
		if product != "" {
			return Version{}, fmt.Errorf("build.info: no Active row for product %q", product)
		}
		return Version{}, fmt.Errorf("build.info: no Active row for flavor %q", flavor)
	}
	return best, nil
}

// parseVersion parses 3-, 4- or 5-segment dotted versions such as "12.1.0",
// "1.60.1.58900" or "1.15.9.58123.1". Segments beyond the fourth are ignored
// and a missing build segment defaults to 0.
func parseVersion(s string) (Version, error) {
	parts := strings.Split(strings.TrimSpace(s), ".")
	if len(parts) < 3 {
		return Version{}, fmt.Errorf("bad version %q", s)
	}
	var v Version
	targets := []*int{&v.Major, &v.Minor, &v.Patch, &v.Build}
	for i := range targets {
		n, err := strconv.Atoi(parts[i])
		if err != nil || n < 0 {
			return Version{}, fmt.Errorf("bad version %q", s)
		}
		*targets[i] = n
	}
	return v, nil
}
