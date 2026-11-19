package trino_test

import data.trino

# A rules key joins names with ".", so it can't name a schema, table or column whose name contains one: no key
# applies to it, and only Administrators read it. Made-up rules, as in policy_test.rego.

dotted_rules := {
	"cat": ["Teachers"],
	# Meant for column a of table t. It must not also grant the table "t.a".
	"cat.s.t.a": ["Analysts"],
	"cat.s.t": ["Analysts", "Teachers"],
}

test_a_column_key_does_not_grant_a_table_whose_name_matches_it if {
	not can_select(analyst, table("cat", "s", "t.a")) with data.trino.rules as dotted_rules
	visible(analyst, column("cat", "s", "t", "a", "varchar")) with data.trino.rules as dotted_rules
}

test_a_dotted_table_is_for_admins_only if {
	# The catalog's key would otherwise cover it.
	not can_select(teacher, table("cat", "s", "t.a")) with data.trino.rules as dotted_rules
	can_select(admin, table("cat", "s", "t.a")) with data.trino.rules as dotted_rules
}

test_a_dotted_schema_is_for_admins_only if {
	not can_select(teacher, table("cat", "s.x", "t")) with data.trino.rules as dotted_rules
	can_select(admin, table("cat", "s.x", "t")) with data.trino.rules as dotted_rules
}

test_a_dotted_column_of_a_readable_table_is_masked if {
	hidden(teacher, column("cat", "s", "t", "x.y", "integer")) with data.trino.rules as dotted_rules
	visible(teacher, column("cat", "s", "t", "plain", "integer")) with data.trino.rules as dotted_rules
	visible(admin, column("cat", "s", "t", "x.y", "integer")) with data.trino.rules as dotted_rules
}

test_a_dotted_table_is_not_listed if {
	not trino.allow with input as table_request("FilterTables", teacher, table("cat", "s", "t.a"))
		with data.trino.rules as dotted_rules
}
