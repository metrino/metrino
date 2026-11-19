package trino_test

import data.trino

# Requests shaped like the ones Trino sends to OPA, for policy_test.rego and rules_test.rego.
# See https://trino.io/docs/current/security/opa-access-control.html

admin := {"user": "admin@example.com", "groups": ["Administrators"]}

analyst := {"user": "analyst@example.com", "groups": ["Analysts"]}

teacher := {"user": "teacher@example.com", "groups": ["Teachers"]}

# Metabase's connection user.
connection_user := {"user": "trino", "groups": []}

# Someone in Metabase with no role.
no_groups := {"user": "nobody@example.com", "groups": []}

request(identity, action) := {
	"context": {
		"identity": identity,
		"softwareStack": {"trinoVersion": "484"},
	},
	"action": action,
}

table(catalog, schema, name) := {"catalogName": catalog, "schemaName": schema, "tableName": name, "columns": []}

column(catalog, schema, table_name, name, type) := {
	"catalogName": catalog,
	"schemaName": schema,
	"tableName": table_name,
	"columnName": name,
	"columnType": type,
}

# An operation on a table (SelectFromColumns, FilterTables, ShowColumns, FilterColumns, CreateTable, ...).
table_request(operation, identity, t) := request(identity, {"operation": operation, "resource": {"table": t}})

can_select(identity, t) if trino.allow with input as table_request("SelectFromColumns", identity, t)

# The masks Trino gets for these columns, as {column name: expression}.
masks(identity, columns) := {columns[mask.index].columnName: mask.viewExpression.expression |
	some mask in trino.batchColumnMasks with input as request(identity, {
		"operation": "GetColumnMask",
		"filterResources": [{"column": c} | some c in columns],
	})
}

hidden(identity, c) if masks(identity, [c])[c.columnName] == sprintf("CAST(NULL AS %s)", [c.columnType])

visible(identity, c) if {
	can_select(identity, table(c.catalogName, c.schemaName, c.tableName))
	count(masks(identity, [c])) == 0
}
