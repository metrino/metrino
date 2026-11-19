package trino

# Trino asks OPA about every operation it runs. This file applies the deployment's access rules (its
# rules.rego: the chart's accessPolicy.rules, opa/rules.rego in compose). To change who can read what,
# edit that file, not this one.
# The shape of Trino's requests: https://trino.io/docs/current/security/opa-access-control.html

# Members of this Metabase group can read every table and column.
admin_group := "Administrators"

# Metabase's own connection user. Metabase's schema sync and background scans (field values, fingerprints)
# run as this user, so it may list every table and column but reads no data. Don't let it read data to make
# those scans work: Metabase stores the values they find and shows them to everyone in filter dropdowns.
connection_user := "trino"

user := input.context.identity.user

groups := input.context.identity.groups

# Who can read what comes from the deployment's rules file, which defines rules in this package. Without
# one, only Administrators read data.
default rules := {}

operation := input.action.operation

resource := input.action.resource

# Only the operations allowed below are allowed. Everything else, such as creating, changing or dropping
# anything or impersonating another user, is denied for everyone, Administrators included. That includes
# connector functions and procedures (ExecuteFunction, ExecuteProcedure, ExecuteTableProcedure): some, like
# the PostgreSQL connector's system.query and system.execute, send raw SQL to the source and would bypass
# these rules.
default allow := false

# Running a query at all. Each table the query reads is checked separately.
allow if operation == "ExecuteQuery"

# Accessing Trino's metrics endpoint.
allow if operation == "ReadSystemInformation"

# Catalog, schema and function names are visible to everyone.
allow if operation in {
	"AccessCatalog", "FilterCatalogs",
	"ShowSchemas", "FilterSchemas",
	"ShowTables", "ShowFunctions", "FilterFunctions",
}

# Reading a table, and seeing it and its columns listed, where the rules allow it.
allow if {
	operation in {"SelectFromColumns", "FilterTables", "ShowColumns", "FilterColumns", "ShowCreateTable"}
	can_read_table(resource.table)
}

# Metadata tables can be queried by everyone. Trino filters their rows with FilterTables and FilterColumns
# above, so people only see the names of tables they can read.
allow if {
	operation == "SelectFromColumns"
	is_metadata_table(resource.table)
}

# Metabase's connection user sees every table and column listed, so Metabase can sync its schema.
allow if {
	user == connection_user
	operation in {"FilterTables", "ShowColumns", "FilterColumns"}
}

is_metadata_table(table) if table.schemaName == "information_schema"

is_metadata_table(table) if {
	table.catalogName == "system"
	table.schemaName in {"jdbc", "metadata"}
}

is_admin if admin_group in groups

# Works for a table resource and for a column resource, which has the same catalog, schema and table names.
can_read_table(table) if is_admin

can_read_table(table) if listed([table.catalogName, table.schemaName, table.tableName])

can_read_column(column) if is_admin

can_read_column(column) if listed([column.catalogName, column.schemaName, column.tableName, column.columnName])

# Whether one of the person's groups is on the most specific rule that covers path, a list of names from the
# catalog down.
listed(path) if {
	some group in object.get(rules, most_specific_key(path), [])
	group in groups
}

# The longest key in rules made of the first names in path, e.g. "cat.schema.table" for
# ["cat", "schema", "table", "column"] when the column has no key of its own. Undefined for a path with a "."
# in one of its names, so that only Administrators read it (see plain).
most_specific_key(path) := key_of(path, n) if {
	plain(path)
	n := max({n | some n in numbers.range(1, count(path)); has_rule(key_of(path, n))})
}

# Whether no name in path contains ".". A rules key joins names with ".", so a key can't tell a name with a "."
# in it from two names: "cat.s.t.a" would name column a of table t, and also the whole table "t.a" in schema s.
# Rather than guess, no key applies to a path with such a name: a schema, table or column called that way is
# for Administrators only, and the web admin page warns about it.
plain(path) if every name in path {
	not contains(name, ".")
}

# object.get rather than rules[key], so the policy also compiles when rules is empty.
has_rule(key) if object.get(rules, key, null) != null

key_of(path, n) := concat(".", array.slice(path, 0, n))

# Which rules key decides each path in input.paths (lists of names from the catalog down), as an array in the
# same order, with null for a path no key decides. This is for the web admin page only, which uses it to explain
# who can read each table and column, and Trino never asks for it. An array, not an object keyed by the joined
# path, because two paths can join to the same text (a table "t.a", and column a of table t). Defined and empty
# when there are no paths.
deciding_keys := [deciding_key(path) | some path in input.paths]

default deciding_key(_) := null

deciding_key(path) := most_specific_key(path)

# Trino asks for all of a query's column masks in one request. A column someone can't read, in a table they
# can, becomes NULL of the column's type, so queries that select it still work. Must always be defined, even
# when empty: Trino fails the query on an undefined result.
batchColumnMasks := [{"index": i, "viewExpression": {"expression": sprintf("CAST(NULL AS %s)", [column.columnType])}} |
	some i, filter_resource in input.action.filterResources
	column := filter_resource.column
	can_read_table(column)
	not can_read_column(column)
]
