package trino_test

import data.trino

# The web admin page asks which rules key decides each path. These use made-up rules so they don't change when
# rules.rego does.

deciding_rules := {
	"c": ["A"],
	"c.s.t": ["A", "B"],
	"c.s.t.col": ["A"],
}

keys_for(paths) := keys if {
	keys := trino.deciding_keys with input as {"paths": paths} with data.trino.rules as deciding_rules
}

test_deciding_keys_uses_the_most_specific_key if {
	keys_for([
		["c", "s", "t", "col"],
		["c", "s", "t", "other"],
		["c", "s", "u"],
		["c", "s"],
		["c"],
	]) == ["c.s.t.col", "c.s.t", "c", "c", "c"]
}

test_deciding_keys_is_null_for_paths_no_key_covers if {
	keys_for([["d", "s", "t"], ["c", "s"]]) == [null, "c"]
}

# No key decides a path with a "." in one of its names, so only Administrators read it.
test_deciding_keys_is_null_for_dotted_names if {
	keys_for([["c", "s", "t.col"], ["c", "s.t", "col"], ["c", "s", "t", "a.b"]]) == [null, null, null]
}

# These two paths join to the same text. An object keyed by it made OPA fail the whole request.
test_deciding_keys_answers_paths_that_join_to_the_same_text if {
	keys_for([["c", "s", "t.col"], ["c", "s", "t", "col"]]) == [null, "c.s.t.col"]
}

test_deciding_keys_keeps_repeated_paths if {
	keys_for([["c"], ["c"]]) == ["c", "c"]
}

test_deciding_keys_of_no_paths_is_empty if {
	keys_for([]) == []
}

test_deciding_keys_without_paths_is_empty if {
	trino.deciding_keys == [] with input as {} with data.trino.rules as deciding_rules
}
