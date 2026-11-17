package script

// Repairing a failed call (script.rs `apply_edits` and the start of `prepare`): the agent resends {find, replace}
// patches against the snippet stored under the failed call's editId instead of the whole snippet.

import (
	"errors"
	"fmt"
	"strings"

	"github.com/naddrzz/nalarupa/core-go/jsonv"
)

// ApplyEdits applies each {find, replace, all?} patch to source in turn. A `find` also matches with its line
// breaks written as `\n`, or its `\n` as line breaks; it must match once unless `all` is set.
func ApplyEdits(source string, edits []jsonv.Value) (string, error) {
	src := source
	for i, e := range edits {
		find, ok := jsonv.Get(e, "find").(string)
		if !ok {
			return "", fmt.Errorf("edit %d: find is required", i)
		}
		replace, ok := jsonv.Get(e, "replace").(string)
		if !ok {
			return "", fmt.Errorf("edit %d: replace is required", i)
		}
		all := jsonv.IsTrue(jsonv.Get(e, "all"))
		f, found := "", false
		for _, v := range []string{find, strings.ReplaceAll(find, "\n", `\n`), strings.ReplaceAll(find, `\n`, "\n")} {
			if v != "" && strings.Contains(src, v) {
				f, found = v, true
				break
			}
		}
		if !found {
			return "", fmt.Errorf("edit %d: `find` text not found in the stored snippet", i)
		}
		if !all && strings.Count(src, f) > 1 {
			return "", fmt.Errorf("edit %d: `find` matches more than once; make it unique or set all:true", i)
		}
		if all {
			src = strings.ReplaceAll(src, f, replace)
		} else {
			src = strings.Replace(src, f, replace, 1)
		}
	}
	return src, nil
}

// source is the JavaScript an execute call runs: its `input`, or the stored snippet of the failed call `editId`
// names with its `edits` applied (editID is then that id).
func source(args jsonv.Value, failed func(string) (string, bool)) (src, editID string, err error) {
	input, _ := jsonv.Get(args, "input").(string)
	if edits := jsonv.Arr(jsonv.Get(args, "edits")); edits != nil {
		if input != "" {
			return "", "", errors.New("pass either `input` or `edits` (with `editId`), not both")
		}
		id, ok := jsonv.Get(args, "editId").(string)
		if !ok {
			return "", "", errors.New("`edits` requires the `editId` from the failed call")
		}
		var stored string
		if failed != nil {
			stored, ok = failed(id)
		} else {
			ok = false
		}
		if !ok {
			return "", "", fmt.Errorf("unknown editId '%s'", id)
		}
		src, err := ApplyEdits(stored, edits.Items)
		if err != nil {
			return "", "", err
		}
		return src, id, nil
	}
	if strings.TrimSpace(input) == "" {
		return "", "", errors.New("`input` (JavaScript) is required")
	}
	return input, "", nil
}
