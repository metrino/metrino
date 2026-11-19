package trino_test

import data.trino

# How policy.rego applies rules, against made-up rules so these tests don't change when rules.rego does.
# What the real rules promise is in rules_test.rego.

made_up_rules := {
	"cat": ["Analysts"],
	"cat.restricted": ["Analysts", "Teachers"],
	"cat.restricted.private": ["Analysts"],
	"cat.public.open": ["Analysts", "Teachers"],
	"cat.public.open.secret": ["Analysts"],
	"cat.public.open.shared": ["Analysts", "Teachers"],
	"cat.public.closed": [],
}

test_a_catalog_rule_covers_its_tables if {
	can_select(analyst, table("cat", "public", "anything")) with data.trino.rules as made_up_rules
	not can_select(teacher, table("cat", "public", "anything")) with data.trino.rules as made_up_rules
}

test_a_schema_rule_overrides_the_catalog_rule if {
	can_select(teacher, table("cat", "restricted", "anything")) with data.trino.rules as made_up_rules
}

test_a_table_rule_overrides_the_schema_rule if {
	not can_select(teacher, table("cat", "restricted", "private")) with data.trino.rules as made_up_rules
	can_select(analyst, table("cat", "restricted", "private")) with data.trino.rules as made_up_rules
	can_select(teacher, table("cat", "public", "open")) with data.trino.rules as made_up_rules
}

test_a_rule_with_no_groups_denies_everyone_but_admins if {
	not can_select(analyst, table("cat", "public", "closed")) with data.trino.rules as made_up_rules
	can_select(admin, table("cat", "public", "closed")) with data.trino.rules as made_up_rules
}

test_only_admins_read_what_no_rule_covers if {
	not can_select(analyst, table("other", "public", "t")) with data.trino.rules as made_up_rules
	can_select(admin, table("other", "public", "t")) with data.trino.rules as made_up_rules
}

# A deployment without a rules file.
test_with_no_rules_only_admins_read if {
	not can_select(analyst, table("cat", "public", "open")) with data.trino.rules as {}
	can_select(admin, table("cat", "public", "open")) with data.trino.rules as {}
}

test_any_one_listed_group_is_enough if {
	both := {"user": "both@example.com", "groups": ["Somebody", "Teachers"]}
	can_select(both, table("cat", "public", "open")) with data.trino.rules as made_up_rules
}

test_group_names_match_exactly if {
	lowercase := {"user": "t@example.com", "groups": ["teachers"]}
	not can_select(lowercase, table("cat", "public", "open")) with data.trino.rules as made_up_rules
}

test_someone_with_no_groups_reads_nothing if {
	not can_select(no_groups, table("cat", "public", "open")) with data.trino.rules as made_up_rules
	not trino.allow with input as table_request("FilterTables", no_groups, table("cat", "public", "open"))
		with data.trino.rules as made_up_rules
}

test_tables_are_only_listed_where_they_can_be_read if {
	every operation in ["FilterTables", "ShowColumns", "FilterColumns", "ShowCreateTable"] {
		trino.allow with input as table_request(operation, teacher, table("cat", "public", "open"))
			with data.trino.rules as made_up_rules
		not trino.allow with input as table_request(operation, teacher, table("cat", "public", "other"))
			with data.trino.rules as made_up_rules
	}
}

test_a_column_rule_hides_the_column if {
	hidden(teacher, column("cat", "public", "open", "secret", "varchar(5)")) with data.trino.rules as made_up_rules
	visible(analyst, column("cat", "public", "open", "secret", "varchar(5)")) with data.trino.rules as made_up_rules
}

test_columns_without_a_rule_follow_their_table if {
	visible(teacher, column("cat", "public", "open", "id", "bigint")) with data.trino.rules as made_up_rules
}

test_hidden_columns_become_null_of_their_type if {
	masks(teacher, [column("cat", "public", "open", "secret", "decimal(10,2)")]) == {"secret": "CAST(NULL AS decimal(10,2))"}
		with data.trino.rules as made_up_rules
}

test_one_request_masks_only_the_hidden_columns if {
	columns := [
		column("cat", "public", "open", "id", "bigint"),
		column("cat", "public", "open", "secret", "varchar"),
		column("cat", "public", "open", "shared", "varchar"),
	]
	masks(teacher, columns) == {"secret": "CAST(NULL AS varchar)"} with data.trino.rules as made_up_rules
}

test_a_column_rule_cannot_open_a_table if {
	rules := {"cat": ["Analysts"], "cat.public.t.c": ["Teachers"]}
	not can_select(teacher, table("cat", "public", "t")) with data.trino.rules as rules
}

test_admins_see_no_masks if {
	visible(admin, column("cat", "public", "open", "secret", "varchar")) with data.trino.rules as made_up_rules
}

# Trino rejects an undefined batchColumnMasks result and fails the query.
test_masks_are_defined_when_nothing_is_hidden if {
	trino.batchColumnMasks == [] with input as request(admin, {"operation": "GetColumnMask", "filterResources": []})
}

test_nobody_writes_or_impersonates if {
	every operation in ["CreateTable", "InsertIntoTable", "DeleteFromTable", "UpdateTableColumns", "DropTable"] {
		not trino.allow with input as table_request(operation, admin, table("cat", "public", "open"))
	}
	not trino.allow with input as request(admin, {"operation": "ImpersonateUser", "resource": {"user": {"user": "analyst@example.com"}}})
	not trino.allow with input as request(admin, {"operation": "CreateSchema", "resource": {"schema": {"catalogName": "cat", "schemaName": "s"}}})
}

test_nobody_runs_connector_functions_or_procedures if {
	every operation in ["ExecuteFunction", "ExecuteProcedure", "ExecuteTableProcedure"] {
		not trino.allow with input as request(admin, {
			"operation": operation,
			"resource": {"function": {"catalogName": "cat", "schemaName": "system", "functionName": "query"}},
		})
	}
}

test_catalog_and_schema_names_are_visible_to_everyone if {
	trino.allow with input as request(no_groups, {"operation": "AccessCatalog", "resource": {"catalog": {"name": "cat"}}})
	trino.allow with input as request(no_groups, {"operation": "FilterSchemas", "resource": {"schema": {"catalogName": "cat", "schemaName": "s"}}})
	trino.allow with input as request(no_groups, {"operation": "ExecuteQuery"})
}

test_everyone_can_query_metadata_tables if {
	every t in [
		table("cat", "information_schema", "tables"),
		table("system", "jdbc", "tables"),
		table("system", "metadata", "table_comments"),
	] {
		can_select(no_groups, t) with data.trino.rules as made_up_rules
	}
	not can_select(no_groups, table("system", "runtime", "queries")) with data.trino.rules as made_up_rules
}

test_metadata_columns_are_not_masked if {
	visible(teacher, column("cat", "information_schema", "tables", "table_name", "varchar"))
		with data.trino.rules as made_up_rules
}

test_the_connection_user_lists_everything_but_reads_no_data if {
	every operation in ["FilterTables", "ShowColumns", "FilterColumns"] {
		trino.allow with input as table_request(operation, connection_user, table("other", "public", "t"))
			with data.trino.rules as made_up_rules
	}
	not can_select(connection_user, table("cat", "public", "open")) with data.trino.rules as made_up_rules
	can_select(connection_user, table("system", "jdbc", "columns")) with data.trino.rules as made_up_rules
}
